package user_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// memStore is an in-memory Store of one tenant's users.
type memStore struct {
	users   map[uuid.UUID]*user.User
	revoked []uuid.UUID
	locked  int
}

func (s *memStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(user.Repository) error) error {
	return fn(s)
}

func (s *memStore) List(_ context.Context, f user.Filter) ([]user.User, error) {
	var out []user.User
	for _, u := range s.users {
		if (f.Role == nil || u.Role == *f.Role) && (f.IsActive == nil || u.IsActive == *f.IsActive) {
			out = append(out, *u)
		}
	}
	slices.SortFunc(out, func(a, b user.User) int { return strings.Compare(b.ID.String(), a.ID.String()) })
	return out, nil
}

func (s *memStore) Get(_ context.Context, id uuid.UUID) (user.User, error) {
	u, ok := s.users[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return *u, nil
}

func (s *memStore) Create(_ context.Context, _ uuid.UUID, nu user.NewUser, hash string) (user.User, error) {
	for _, u := range s.users {
		if strings.EqualFold(u.Email, nu.Email) {
			return user.User{}, user.ErrEmailTaken
		}
	}
	if !strings.HasPrefix(hash, "hash:") {
		panic("the password was not hashed")
	}
	u := &user.User{ID: uuid.Must(uuid.NewV7()), Email: nu.Email, FullName: nu.FullName, Phone: nu.Phone, Role: nu.Role, IsActive: true}
	s.users[u.ID] = u
	return *u, nil
}

func (s *memStore) Update(_ context.Context, id uuid.UUID, p user.Patch) (user.User, error) {
	u, ok := s.users[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	if p.FullName != nil {
		u.FullName = *p.FullName
	}
	if p.Phone != nil || p.ClearPhone {
		u.Phone = p.Phone
	}
	if p.Role != nil {
		u.Role = *p.Role
	}
	if p.IsActive != nil {
		u.IsActive = *p.IsActive
	}
	return *u, nil
}

func (s *memStore) LockTenant(context.Context, uuid.UUID) error {
	s.locked++
	return nil
}

func (s *memStore) RevokeSessions(_ context.Context, userID uuid.UUID, _ time.Time) error {
	s.revoked = append(s.revoked, userID)
	return nil
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, password string) (string, error) {
	return "hash:" + password, nil
}

type fixture struct {
	store   *memStore
	svc     *user.Service
	admin   identity.Principal
	manager identity.Principal
	driver  *user.User
}

func newFixture() *fixture {
	tenantID := uuid.New()
	store := &memStore{users: map[uuid.UUID]*user.User{}}
	add := func(role identity.Role) *user.User {
		u := &user.User{ID: uuid.Must(uuid.NewV7()), Email: strings.ToLower(string(role)) + "@x.example", Role: role, IsActive: true}
		store.users[u.ID] = u
		return u
	}
	adminUser, managerUser, driver := add(identity.RoleAdmin), add(identity.RoleWarehouseManager), add(identity.RoleDriver)
	now := func() time.Time { return time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC) }
	return &fixture{
		store:   store,
		svc:     user.NewService(store, fakeHasher{}, now),
		admin:   identity.Principal{UserID: adminUser.ID, TenantID: tenantID, Role: identity.RoleAdmin},
		manager: identity.Principal{UserID: managerUser.ID, TenantID: tenantID, Role: identity.RoleWarehouseManager},
		driver:  driver,
	}
}

func isDenial(err error) bool {
	var d *policy.DenialError
	return errors.As(err, &d)
}

func TestListIsForAdminsAndDriverListsForManagers(t *testing.T) {
	f := newFixture()
	all, err := f.svc.List(t.Context(), f.admin, user.Filter{Limit: 10})
	if err != nil || len(all) != 3 {
		t.Errorf("admin: %d users, %v", len(all), err)
	}

	driverRole := identity.RoleDriver
	drivers, err := f.svc.List(t.Context(), f.manager, user.Filter{Role: &driverRole, Limit: 10})
	if err != nil || len(drivers) != 1 || drivers[0].ID != f.driver.ID {
		t.Errorf("manager listing drivers: %+v, %v", drivers, err)
	}
	if _, err := f.svc.List(t.Context(), f.manager, user.Filter{Limit: 10}); !isDenial(err) {
		t.Errorf("manager listing everyone: error = %v, want a denial", err)
	}
	adminRole := identity.RoleAdmin
	if _, err := f.svc.List(t.Context(), f.manager, user.Filter{Role: &adminRole, Limit: 10}); !isDenial(err) {
		t.Errorf("manager listing admins: error = %v, want a denial", err)
	}
	driverPrincipal := f.manager
	driverPrincipal.Role = identity.RoleDriver
	if _, err := f.svc.List(t.Context(), driverPrincipal, user.Filter{Role: &driverRole, Limit: 10}); !isDenial(err) {
		t.Errorf("driver listing drivers: error = %v, want a denial", err)
	}
}

func TestGetAndCreateAreForAdmins(t *testing.T) {
	f := newFixture()
	if got, err := f.svc.Get(t.Context(), f.admin, f.driver.ID); err != nil || got.ID != f.driver.ID {
		t.Errorf("admin get: %+v, %v", got, err)
	}
	if _, err := f.svc.Get(t.Context(), f.admin, uuid.New()); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("missing user: error = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Get(t.Context(), f.manager, f.driver.ID); !isDenial(err) {
		t.Errorf("manager get: error = %v, want a denial", err)
	}

	nu := user.NewUser{Email: "new@x.example", Password: "a long enough password", FullName: "New", Role: identity.RoleDriver}
	created, err := f.svc.Create(t.Context(), f.admin, nu)
	if err != nil || created.Email != nu.Email || !created.IsActive {
		t.Fatalf("admin create: %+v, %v", created, err)
	}
	if f.store.locked != 1 {
		t.Errorf("tenant locked %d times, want once per creation", f.store.locked)
	}
	if _, err := f.svc.Create(t.Context(), f.admin, nu); !errors.Is(err, user.ErrEmailTaken) {
		t.Errorf("duplicate email: error = %v, want ErrEmailTaken", err)
	}
	if _, err := f.svc.Create(t.Context(), f.manager, nu); !isDenial(err) {
		t.Errorf("manager create: error = %v, want a denial", err)
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture()
	name, manager := "Renamed Driver", identity.RoleWarehouseManager
	got, err := f.svc.Update(t.Context(), f.admin, f.driver.ID, user.Patch{FullName: &name, Role: &manager})
	if err != nil || got.FullName != name || got.Role != manager {
		t.Fatalf("Update() = %+v, %v", got, err)
	}
	if f.store.locked != 1 {
		t.Errorf("tenant locked %d times, want once per update", f.store.locked)
	}
	if len(f.store.revoked) != 0 {
		t.Errorf("sessions revoked %v, want none for a role change", f.store.revoked)
	}

	inactive := false
	if _, err := f.svc.Update(t.Context(), f.admin, f.driver.ID, user.Patch{IsActive: &inactive}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if !slices.Equal(f.store.revoked, []uuid.UUID{f.driver.ID}) {
		t.Errorf("revoked %v, want the deactivated user's sessions", f.store.revoked)
	}
	if _, err := f.svc.Update(t.Context(), f.admin, f.driver.ID, user.Patch{IsActive: &inactive}); err != nil ||
		len(f.store.revoked) != 1 {
		t.Errorf("deactivating again: %v, revoked %v; want no new revocation", err, f.store.revoked)
	}
	if _, err := f.svc.Update(t.Context(), f.admin, uuid.New(), user.Patch{FullName: &name}); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("missing user: error = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Update(t.Context(), f.manager, f.driver.ID, user.Patch{FullName: &name}); !isDenial(err) {
		t.Errorf("manager update: error = %v, want a denial", err)
	}
}

func TestAdminsCannotDemoteOrDeactivateThemselves(t *testing.T) {
	f := newFixture()
	driverRole, adminRole, inactive, active := identity.RoleDriver, identity.RoleAdmin, false, true
	name := "Still Admin"

	for _, p := range []user.Patch{{Role: &driverRole}, {IsActive: &inactive}} {
		if _, err := f.svc.Update(t.Context(), f.admin, f.admin.UserID, p); !errors.Is(err, user.ErrSelfChange) {
			t.Errorf("self patch %+v: error = %v, want ErrSelfChange", p, err)
		}
	}
	// Unchanged values and other fields are fine, so a form can resend the whole user.
	if _, err := f.svc.Update(t.Context(), f.admin, f.admin.UserID, user.Patch{Role: &adminRole, IsActive: &active, FullName: &name}); err != nil {
		t.Errorf("self patch without a role or status change: %v", err)
	}
}

func TestDemotedAdminsLoseAccessBeforeTheirTokenExpires(t *testing.T) {
	f := newFixture()
	f.store.users[f.admin.UserID].Role = identity.RoleWarehouseManager // demoted by another admin

	nu := user.NewUser{Email: "escape@x.example", Password: "a long enough password", FullName: "X", Role: identity.RoleAdmin}
	if _, err := f.svc.Create(t.Context(), f.admin, nu); !errors.Is(err, user.ErrActorNotAdmin) {
		t.Errorf("create: error = %v, want ErrActorNotAdmin", err)
	}
	adminRole := identity.RoleAdmin
	if _, err := f.svc.Update(t.Context(), f.admin, f.driver.ID, user.Patch{Role: &adminRole}); !errors.Is(err, user.ErrActorNotAdmin) {
		t.Errorf("update: error = %v, want ErrActorNotAdmin", err)
	}

	delete(f.store.users, f.admin.UserID)
	if _, err := f.svc.Create(t.Context(), f.admin, nu); !errors.Is(err, user.ErrActorNotAdmin) {
		t.Errorf("create by a missing account: error = %v, want ErrActorNotAdmin", err)
	}
}
