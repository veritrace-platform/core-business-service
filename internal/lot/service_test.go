package lot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/lot"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// memStore is an in-memory Store of one tenant's products, locations, and lots.
type memStore struct {
	products  map[uuid.UUID]lot.Product
	locations map[uuid.UUID]bool // active flag
	lots      map[uuid.UUID]lot.Lot
	movements []inventory.Movement
	locks     int
}

func newMemStore() *memStore {
	return &memStore{products: map[uuid.UUID]lot.Product{}, locations: map[uuid.UUID]bool{}, lots: map[uuid.UUID]lot.Lot{}}
}

func (s *memStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(lot.Repository) error) error {
	return fn(s)
}

func (s *memStore) List(context.Context, lot.Filter) ([]lot.Lot, error) {
	var out []lot.Lot
	for _, l := range s.lots {
		out = append(out, l)
	}
	return out, nil
}

func (s *memStore) Get(_ context.Context, id uuid.UUID) (lot.Lot, error) {
	l, ok := s.lots[id]
	if !ok {
		return lot.Lot{}, lot.ErrNotFound
	}
	return l, nil
}

func (s *memStore) LockProduct(_ context.Context, id uuid.UUID) (lot.Product, error) {
	s.locks++
	p, ok := s.products[id]
	if !ok {
		return lot.Product{}, lot.ErrNotOwned
	}
	return p, nil
}

func (s *memStore) LockLocation(_ context.Context, id uuid.UUID) (bool, error) {
	s.locks++
	active, ok := s.locations[id]
	if !ok {
		return false, lot.ErrNotOwned
	}
	return active, nil
}

func (s *memStore) Create(_ context.Context, tenantID, _ uuid.UUID, nl lot.NewLot, p lot.Product) (lot.Lot, error) {
	for _, l := range s.lots {
		if l.ProductID == p.ID && l.LotNumber == nl.LotNumber {
			return lot.Lot{}, lot.ErrLotNumberTaken
		}
	}
	l := lot.Lot{
		ID: uuid.Must(uuid.NewV7()), OwnerTenantID: tenantID, ProductID: p.ID, GTIN: p.GTIN, ProductName: p.Name,
		LotNumber: nl.LotNumber, QuantityCommissioned: nl.Quantity, Status: policy.LotActive,
	}
	s.lots[l.ID] = l
	return l, nil
}

func (s *memStore) ApplyMovement(_ context.Context, m inventory.Movement) error {
	s.movements = append(s.movements, m)
	return nil
}

func principal(role identity.Role) identity.Principal {
	return identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: role}
}

func failedCheck(err error, check policy.Check) bool {
	var d *policy.DenialError
	return errors.As(err, &d) && d.Reason == policy.ReasonCheck && d.Check == check
}

func TestCommission(t *testing.T) {
	store := newMemStore()
	svc := lot.NewService(store)
	milk := lot.Product{ID: uuid.New(), GTIN: "08930001000018", Name: "Chilled milk", MinTempCelsius: 2, MaxTempCelsius: 6, IsActive: true}
	retired := lot.Product{ID: uuid.New(), GTIN: "08930001000025", Name: "Old milk", MinTempCelsius: 2, MaxTempCelsius: 6}
	store.products[milk.ID], store.products[retired.ID] = milk, retired
	plant, closed := uuid.New(), uuid.New()
	store.locations[plant], store.locations[closed] = true, false

	manager := principal(identity.RoleWarehouseManager)
	nl := lot.NewLot{
		ProductID: milk.ID, LotNumber: "L2026-09", ProductionDate: calendar.New(2026, time.September, 30),
		ExpirationDate: calendar.New(2026, time.October, 14), Quantity: 500, LocationID: plant,
	}
	created, err := svc.Commission(t.Context(), manager, nl)
	if err != nil || created.GTIN != milk.GTIN || created.ProductName != milk.Name || created.Status != policy.LotActive {
		t.Fatalf("Commission() = %+v, %v", created, err)
	}
	want := inventory.Movement{
		TenantID: manager.TenantID, LocationID: plant, LotID: created.ID, Delta: 500,
		Reason: inventory.ReasonCommissioned, CreatedBy: manager.UserID,
	}
	if len(store.movements) != 1 || store.movements[0] != want {
		t.Errorf("movements = %+v, want %+v", store.movements, want)
	}
	if _, err := svc.Commission(t.Context(), manager, nl); !errors.Is(err, lot.ErrLotNumberTaken) {
		t.Errorf("same lot number again: error = %v, want ErrLotNumberTaken", err)
	}

	tests := []struct {
		name    string
		product uuid.UUID
		place   uuid.UUID
		failed  func(error) bool
	}{
		{"product of another tenant", uuid.New(), plant, func(err error) bool { return failedCheck(err, policy.ProductOwned) }},
		{"location of another tenant", milk.ID, uuid.New(), func(err error) bool { return failedCheck(err, policy.LocationOwned) }},
		{"inactive product", retired.ID, plant, func(err error) bool { return errors.Is(err, lot.ErrProductInactive) }},
		{"inactive location", milk.ID, closed, func(err error) bool { return errors.Is(err, lot.ErrLocationInactive) }},
	}
	for _, tt := range tests {
		bad := nl
		bad.ProductID, bad.LocationID, bad.LotNumber = tt.product, tt.place, "L-"+tt.name[:3]
		if _, err := svc.Commission(t.Context(), manager, bad); !tt.failed(err) {
			t.Errorf("%s: error = %v", tt.name, err)
		}
	}

	// Drivers are denied before anything is locked.
	locks := store.locks
	var d *policy.DenialError
	if _, err := svc.Commission(t.Context(), principal(identity.RoleDriver), nl); !errors.As(err, &d) || d.Reason != policy.ReasonRole {
		t.Errorf("driver: error = %v, want a role denial", err)
	}
	if store.locks != locks {
		t.Errorf("%d rows locked for a denied caller", store.locks-locks)
	}
	if len(store.movements) != 1 {
		t.Errorf("%d movements after failed commissions, want 1", len(store.movements))
	}
}

func TestReads(t *testing.T) {
	store := newMemStore()
	svc := lot.NewService(store)
	id := uuid.New()
	store.lots[id] = lot.Lot{ID: id, Status: policy.LotActive}

	for _, role := range []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager} {
		if got, err := svc.Get(t.Context(), principal(role), id); err != nil || got.ID != id {
			t.Errorf("%s Get() = %+v, %v", role, got, err)
		}
		if got, err := svc.List(t.Context(), principal(role), lot.Filter{Limit: 10}); err != nil || len(got) != 1 {
			t.Errorf("%s List() = %d lots, %v", role, len(got), err)
		}
	}
	if _, err := svc.Get(t.Context(), principal(identity.RoleAdmin), uuid.New()); !errors.Is(err, lot.ErrNotFound) {
		t.Errorf("missing lot: error = %v, want ErrNotFound", err)
	}
	for _, role := range []identity.Role{identity.RoleDriver, identity.RoleInspector} {
		var d *policy.DenialError
		if _, err := svc.List(t.Context(), principal(role), lot.Filter{Limit: 10}); !errors.As(err, &d) || d.Reason != policy.ReasonRole {
			t.Errorf("%s List(): error = %v, want a role denial", role, err)
		}
		if _, err := svc.Get(t.Context(), principal(role), id); !errors.As(err, &d) || d.Reason != policy.ReasonRole {
			t.Errorf("%s Get(): error = %v, want a role denial", role, err)
		}
	}
}
