//go:build integration

package user_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

const hash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"

func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := user.NewPostgresStore(db.Tenancy)
	a, b := db.CreateTenant(t), db.CreateTenant(t)

	// in runs fn in a transaction of tenant, failing the test on errors.
	in := func(t *testing.T, tenant uuid.UUID, fn func(user.Repository) error) {
		t.Helper()
		if err := store.WithTenantTx(t.Context(), tenant, fn); err != nil {
			t.Fatalf("tenant transaction: %v", err)
		}
	}

	t.Run("creates users and rejects taken emails in any tenant", func(t *testing.T) {
		phone := "0901 234 567"
		in(t, a.ID, func(repo user.Repository) error {
			created, err := repo.Create(t.Context(), a.ID, user.NewUser{
				Email: "Driver.One@Fixture.example", FullName: "Driver One", Phone: &phone, Role: identity.RoleDriver,
			}, hash)
			if err != nil || !created.IsActive || created.Role != identity.RoleDriver || *created.Phone != phone ||
				created.CreatedAt.IsZero() {
				t.Errorf("Create() = %+v, %v", created, err)
			}
			return nil
		})
		// A failed statement aborts the transaction, so the error ends it, as in the service.
		err := store.WithTenantTx(t.Context(), b.ID, func(repo user.Repository) error {
			_, err := repo.Create(t.Context(), b.ID, user.NewUser{Email: "driver.one@fixture.example", FullName: "X", Role: identity.RoleDriver}, hash)
			return err
		})
		if !errors.Is(err, user.ErrEmailTaken) {
			t.Errorf("same email in another tenant: error = %v, want ErrEmailTaken", err)
		}
	})

	t.Run("pages through users newest first", func(t *testing.T) {
		tenant := db.CreateTenant(t)
		for i := range 7 {
			role := identity.RoleWarehouseManager
			if i%2 == 0 {
				role = identity.RoleDriver
			}
			db.CreateUser(t, tenant.ID, role)
		}
		var seen []uuid.UUID
		after := uuid.Nil
		for {
			var page []user.User
			in(t, tenant.ID, func(repo user.Repository) error {
				var err error
				page, err = repo.List(t.Context(), user.Filter{After: after, Limit: 3})
				return err
			})
			for _, u := range page {
				seen = append(seen, u.ID)
			}
			if len(page) < 3 {
				break
			}
			after = page[len(page)-1].ID
		}
		if len(seen) != 8 { // seven fixtures and the admin
			t.Fatalf("paged through %d users, want 8", len(seen))
		}
		for i := 1; i < len(seen); i++ {
			if strings.Compare(seen[i-1].String(), seen[i].String()) <= 0 {
				t.Errorf("users out of order at %d: %s then %s", i, seen[i-1], seen[i])
			}
		}

		driver, inactive := identity.RoleDriver, false
		in(t, tenant.ID, func(repo user.Repository) error {
			drivers, err := repo.List(t.Context(), user.Filter{Role: &driver, Limit: 100})
			if err != nil || len(drivers) != 4 {
				t.Errorf("drivers: %d, %v; want 4", len(drivers), err)
			}
			none, err := repo.List(t.Context(), user.Filter{IsActive: &inactive, Limit: 100})
			if err != nil || len(none) != 0 {
				t.Errorf("inactive users: %d, %v; want 0", len(none), err)
			}
			return nil
		})
	})

	t.Run("patches only the given fields", func(t *testing.T) {
		u := db.CreateUser(t, a.ID, identity.RoleDriver)
		phone, name, role := "+84 28 3822 1234", "Renamed", identity.RoleWarehouseManager
		in(t, a.ID, func(repo user.Repository) error {
			got, err := repo.Update(t.Context(), u.ID, user.Patch{Phone: &phone})
			if err != nil || got.Phone == nil || *got.Phone != phone || got.FullName != "Fixture User" || got.Role != identity.RoleDriver {
				t.Errorf("phone patch: %+v, %v", got, err)
			}
			got, err = repo.Update(t.Context(), u.ID, user.Patch{FullName: &name, Role: &role})
			if err != nil || got.Phone == nil || got.FullName != name || got.Role != role {
				t.Errorf("name and role patch kept the phone? %+v, %v", got, err)
			}
			got, err = repo.Update(t.Context(), u.ID, user.Patch{ClearPhone: true})
			if err != nil || got.Phone != nil {
				t.Errorf("clearing the phone: %+v, %v", got, err)
			}
			if _, err := repo.Update(t.Context(), uuid.New(), user.Patch{FullName: &name}); !errors.Is(err, user.ErrNotFound) {
				t.Errorf("missing user: error = %v, want ErrNotFound", err)
			}
			return repo.LockTenant(t.Context(), a.ID)
		})
	})

	t.Run("revokes a user's sessions", func(t *testing.T) {
		u := db.CreateUser(t, a.ID, identity.RoleDriver)
		for i := range 2 {
			token := sha256.Sum256(fmt.Appendf(nil, "session-%s-%d", u.ID, i))
			_, err := db.Owner.Exec(t.Context(), `INSERT INTO core.auth_sessions (tenant_id, user_id, family_id, token_hash, expires_at)
				VALUES ($1, $2, $3, $4, now() + interval '1 day')`, a.ID, u.ID, uuid.New(), token[:])
			if err != nil {
				t.Fatalf("seed session: %v", err)
			}
		}
		in(t, a.ID, func(repo user.Repository) error {
			return repo.RevokeSessions(t.Context(), u.ID, time.Now())
		})
		var active int
		if err := db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.auth_sessions WHERE user_id = $1 AND revoked_at IS NULL`, u.ID).Scan(&active); err != nil || active != 0 {
			t.Errorf("active sessions after revocation: %d, %v", active, err)
		}
	})

	t.Run("another tenant sees none of the users", func(t *testing.T) {
		name := "Hijacked"
		in(t, b.ID, func(repo user.Repository) error {
			if _, err := repo.Get(t.Context(), a.Admin.ID); !errors.Is(err, user.ErrNotFound) {
				t.Errorf("Get() across tenants: error = %v, want ErrNotFound", err)
			}
			if _, err := repo.Update(t.Context(), a.Admin.ID, user.Patch{FullName: &name}); !errors.Is(err, user.ErrNotFound) {
				t.Errorf("Update() across tenants: error = %v, want ErrNotFound", err)
			}
			users, err := repo.List(t.Context(), user.Filter{Limit: 100})
			for _, u := range users {
				if u.ID == a.Admin.ID {
					t.Error("List() returned a user of another tenant")
				}
			}
			return err
		})
	})
}

func TestTwoAdminsCannotDemoteEachOther(t *testing.T) {
	db := tenancytest.Start(t)
	svc := user.NewService(user.NewPostgresStore(db.Tenancy), fakeHasher{}, time.Now)
	tenant := db.CreateTenant(t)
	other := db.CreateUser(t, tenant.ID, identity.RoleAdmin)
	a := identity.Principal{UserID: tenant.Admin.ID, TenantID: tenant.ID, Role: identity.RoleAdmin}
	b := identity.Principal{UserID: other.ID, TenantID: tenant.ID, Role: identity.RoleAdmin}
	manager := identity.RoleWarehouseManager

	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, move := range []struct {
		actor  identity.Principal
		target uuid.UUID
	}{{a, b.UserID}, {b, a.UserID}} {
		go func() {
			<-start
			_, err := svc.Update(t.Context(), move.actor, move.target, user.Patch{Role: &manager})
			errs <- err
		}()
	}
	close(start)

	succeeded := 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			succeeded++
		case !errors.Is(err, user.ErrActorNotAdmin):
			t.Errorf("unexpected error: %v", err)
		}
	}
	var admins int
	if err := db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.users WHERE tenant_id = $1 AND role = 'ADMIN' AND is_active`,
		tenant.ID).Scan(&admins); err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if succeeded != 1 || admins != 1 {
		t.Errorf("%d demotions succeeded and %d active admins remain, want 1 and 1", succeeded, admins)
	}
}
