package product_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/product"
)

// memStore is an in-memory Store of one tenant's products.
type memStore struct {
	gcp          string
	products     map[uuid.UUID]*product.Product
	prefixReads  int
	transactions int
}

func newMemStore() *memStore {
	return &memStore{gcp: "8930001", products: map[uuid.UUID]*product.Product{}}
}

func (s *memStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(product.Repository) error) error {
	s.transactions++
	return fn(s)
}

func (s *memStore) List(context.Context, product.Filter) ([]product.Product, error) {
	var out []product.Product
	for _, p := range s.products {
		out = append(out, *p)
	}
	return out, nil
}

func (s *memStore) Get(_ context.Context, id uuid.UUID) (product.Product, error) {
	p, ok := s.products[id]
	if !ok {
		return product.Product{}, product.ErrNotFound
	}
	return *p, nil
}

func (s *memStore) CompanyPrefix(context.Context, uuid.UUID) (string, error) {
	s.prefixReads++
	return s.gcp, nil
}

func (s *memStore) Create(_ context.Context, _ uuid.UUID, np product.NewProduct) (product.Product, error) {
	for _, p := range s.products {
		if p.GTIN == np.GTIN {
			return product.Product{}, product.ErrGTINTaken
		}
	}
	p := &product.Product{ID: uuid.Must(uuid.NewV7()), GTIN: np.GTIN, Name: np.Name,
		MinTempCelsius: np.MinTempCelsius, MaxTempCelsius: np.MaxTempCelsius, IsActive: true}
	s.products[p.ID] = p
	return *p, nil
}

func (s *memStore) Update(_ context.Context, id uuid.UUID, patch product.Patch) (product.Product, error) {
	p, ok := s.products[id]
	if !ok {
		return product.Product{}, product.ErrNotFound
	}
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.IsActive != nil {
		p.IsActive = *patch.IsActive
	}
	return *p, nil
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
	svc := product.NewService(store)
	chilled := product.NewProduct{GTIN: "08930001000018", Name: "Chilled milk", MinTempCelsius: 2, MaxTempCelsius: 8}

	// Warehouse managers maintain products as well as admins.
	for _, role := range []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager} {
		np := chilled
		if role == identity.RoleWarehouseManager {
			np.GTIN = "18930001000015"
		}
		if created, err := svc.Create(t.Context(), principal(role), np); err != nil || created.GTIN != np.GTIN {
			t.Errorf("%s Create() = %+v, %v", role, created, err)
		}
	}
	if _, err := svc.Create(t.Context(), principal(identity.RoleAdmin), chilled); !errors.Is(err, product.ErrGTINTaken) {
		t.Errorf("same GTIN again: error = %v, want ErrGTINTaken", err)
	}

	// A valid GTIN under another company prefix fails the policy's prefix check.
	foreign := chilled
	foreign.GTIN = "08934567001014"
	_, err := svc.Create(t.Context(), principal(identity.RoleAdmin), foreign)
	var invalid *gs1.InvalidKeyError
	if !errors.As(err, &invalid) || invalid.Reason != gs1.ReasonPrefixMismatch {
		t.Errorf("foreign GTIN: error = %v, want a PREFIX_MISMATCH key error", err)
	}

	reads := store.prefixReads
	for _, role := range []identity.Role{identity.RoleDriver, identity.RoleInspector} {
		if _, err := svc.Create(t.Context(), principal(role), chilled); !roleDenied(err) {
			t.Errorf("%s: error = %v, want a role denial", role, err)
		}
	}
	if store.prefixReads != reads {
		t.Errorf("the prefix was read %d more times for denied callers", store.prefixReads-reads)
	}
}

func TestReadsAndUpdates(t *testing.T) {
	store := newMemStore()
	svc := product.NewService(store)
	manager := principal(identity.RoleWarehouseManager)
	created, err := svc.Create(t.Context(), manager, product.NewProduct{GTIN: "08930001000018", Name: "Chilled milk", MinTempCelsius: 2, MaxTempCelsius: 8})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := svc.Get(t.Context(), manager, created.ID); err != nil || got.ID != created.ID {
		t.Errorf("Get() = %+v, %v", got, err)
	}
	if got, err := svc.List(t.Context(), manager, product.Filter{Limit: 10}); err != nil || len(got) != 1 {
		t.Errorf("List() = %d products, %v", len(got), err)
	}
	if _, err := svc.Get(t.Context(), manager, uuid.New()); !errors.Is(err, product.ErrNotFound) {
		t.Errorf("missing product: error = %v, want ErrNotFound", err)
	}

	before := store.transactions
	for _, role := range []identity.Role{identity.RoleDriver, identity.RoleInspector} {
		if _, err := svc.List(t.Context(), principal(role), product.Filter{Limit: 10}); !roleDenied(err) {
			t.Errorf("%s List(): error = %v, want a role denial", role, err)
		}
		if _, err := svc.Get(t.Context(), principal(role), created.ID); !roleDenied(err) {
			t.Errorf("%s Get(): error = %v, want a role denial", role, err)
		}
		if _, err := svc.Update(t.Context(), principal(role), created.ID, product.Patch{}); !roleDenied(err) {
			t.Errorf("%s Update(): error = %v, want a role denial", role, err)
		}
	}
	if store.transactions != before {
		t.Errorf("%d transactions opened for denied callers", store.transactions-before)
	}

	inactive, name := false, "Chilled milk 1 L"
	updated, err := svc.Update(t.Context(), manager, created.ID, product.Patch{Name: &name, IsActive: &inactive})
	if err != nil || updated.Name != name || updated.IsActive {
		t.Errorf("Update() = %+v, %v", updated, err)
	}
}
