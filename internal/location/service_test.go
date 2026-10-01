package location_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// memStore is an in-memory Store of one tenant's locations.
type memStore struct {
	gcp          string
	locations    map[uuid.UUID]*location.Location
	prefixReads  int
	transactions int
}

func newMemStore() *memStore {
	return &memStore{gcp: "8930001", locations: map[uuid.UUID]*location.Location{}}
}

func (s *memStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(location.Repository) error) error {
	s.transactions++
	return fn(s)
}

func (s *memStore) List(_ context.Context, f location.Filter) ([]location.Location, error) {
	var out []location.Location
	for _, l := range s.locations {
		if f.IsActive == nil || l.IsActive == *f.IsActive {
			out = append(out, *l)
		}
	}
	return out, nil
}

func (s *memStore) Get(_ context.Context, id uuid.UUID) (location.Location, error) {
	l, ok := s.locations[id]
	if !ok {
		return location.Location{}, location.ErrNotFound
	}
	return *l, nil
}

func (s *memStore) CompanyPrefix(context.Context, uuid.UUID) (string, error) {
	s.prefixReads++
	return s.gcp, nil
}

func (s *memStore) Create(_ context.Context, _ uuid.UUID, nl location.NewLocation) (location.Location, error) {
	for _, l := range s.locations {
		if l.GLN == nl.GLN {
			return location.Location{}, location.ErrGLNTaken
		}
	}
	l := &location.Location{ID: uuid.Must(uuid.NewV7()), GLN: nl.GLN, Name: nl.Name, IsActive: true}
	s.locations[l.ID] = l
	return *l, nil
}

func (s *memStore) Update(_ context.Context, id uuid.UUID, p location.Patch) (location.Location, error) {
	l, ok := s.locations[id]
	if !ok {
		return location.Location{}, location.ErrNotFound
	}
	if p.Name != nil {
		l.Name = *p.Name
	}
	if p.IsActive != nil {
		l.IsActive = *p.IsActive
	}
	return *l, nil
}

func principal(role identity.Role) identity.Principal {
	return identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: role}
}

// roleDenied reports a policy denial for the caller's role.
func roleDenied(err error) bool {
	var d *policy.DenialError
	return errors.As(err, &d) && d.Reason == policy.ReasonRole
}

func TestCreate(t *testing.T) {
	store := newMemStore()
	svc := location.NewService(store)
	admin := principal(identity.RoleAdmin)

	created, err := svc.Create(t.Context(), admin, location.NewLocation{GLN: "8930001000018", Name: "Cold Store"})
	if err != nil || created.GLN != "8930001000018" {
		t.Fatalf("Create() = %+v, %v", created, err)
	}
	if _, err := svc.Create(t.Context(), admin, location.NewLocation{GLN: "8930001000018"}); !errors.Is(err, location.ErrGLNTaken) {
		t.Errorf("same GLN again: error = %v, want ErrGLNTaken", err)
	}

	// A valid GLN under another company prefix fails the policy's prefix check.
	_, err = svc.Create(t.Context(), admin, location.NewLocation{GLN: "8934567000017"})
	var invalid *gs1.InvalidKeyError
	if !errors.As(err, &invalid) || invalid.Reason != gs1.ReasonPrefixMismatch {
		t.Errorf("foreign GLN: error = %v, want a PREFIX_MISMATCH key error", err)
	}

	// Only admins manage locations, and the prefix is read only for them.
	reads := store.prefixReads
	for _, role := range []identity.Role{identity.RoleWarehouseManager, identity.RoleDriver, identity.RoleInspector} {
		if _, err := svc.Create(t.Context(), principal(role), location.NewLocation{GLN: "8930001001015"}); !roleDenied(err) {
			t.Errorf("%s: error = %v, want a role denial", role, err)
		}
	}
	if store.prefixReads != reads {
		t.Errorf("the prefix was read %d more times for denied callers", store.prefixReads-reads)
	}
}

func TestReadsAndUpdates(t *testing.T) {
	store := newMemStore()
	svc := location.NewService(store)
	admin := principal(identity.RoleAdmin)
	created, err := svc.Create(t.Context(), admin, location.NewLocation{GLN: "8930001000018", Name: "Cold Store"})
	if err != nil {
		t.Fatal(err)
	}

	manager := principal(identity.RoleWarehouseManager)
	if got, err := svc.Get(t.Context(), manager, created.ID); err != nil || got.ID != created.ID {
		t.Errorf("manager Get() = %+v, %v", got, err)
	}
	if got, err := svc.List(t.Context(), manager, location.Filter{Limit: 10}); err != nil || len(got) != 1 {
		t.Errorf("manager List() = %d locations, %v", len(got), err)
	}
	if _, err := svc.Get(t.Context(), manager, uuid.New()); !errors.Is(err, location.ErrNotFound) {
		t.Errorf("missing location: error = %v, want ErrNotFound", err)
	}

	// Drivers and inspectors do not read the catalog, and nothing is opened for them.
	before := store.transactions
	for _, role := range []identity.Role{identity.RoleDriver, identity.RoleInspector} {
		if _, err := svc.List(t.Context(), principal(role), location.Filter{Limit: 10}); !roleDenied(err) {
			t.Errorf("%s List(): error = %v, want a role denial", role, err)
		}
		if _, err := svc.Get(t.Context(), principal(role), created.ID); !roleDenied(err) {
			t.Errorf("%s Get(): error = %v, want a role denial", role, err)
		}
	}
	if store.transactions != before {
		t.Errorf("%d transactions opened for denied reads", store.transactions-before)
	}

	inactive, name := false, "Old Cold Store"
	updated, err := svc.Update(t.Context(), admin, created.ID, location.Patch{Name: &name, IsActive: &inactive})
	if err != nil || updated.Name != name || updated.IsActive {
		t.Errorf("Update() = %+v, %v", updated, err)
	}
	if _, err := svc.Update(t.Context(), manager, created.ID, location.Patch{Name: &name}); !roleDenied(err) {
		t.Errorf("manager Update(): error = %v, want a role denial", err)
	}
	if _, err := svc.Update(t.Context(), admin, uuid.New(), location.Patch{Name: &name}); !errors.Is(err, location.ErrNotFound) {
		t.Errorf("missing location: error = %v, want ErrNotFound", err)
	}
}
