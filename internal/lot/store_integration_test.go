//go:build integration

package lot_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/lot"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

func TestCommissionWithPostgres(t *testing.T) {
	db := tenancytest.Start(t)
	svc := lot.NewService(lot.NewPostgresStore(db.Tenancy))
	owner, other := db.CreateTenant(t), db.CreateTenant(t)
	product := db.CreateProduct(t, owner)
	plant := db.CreateLocation(t, owner)
	manager := db.CreateUser(t, owner.ID, identity.RoleWarehouseManager)
	caller := identity.Principal{UserID: manager.ID, TenantID: owner.ID, Role: identity.RoleWarehouseManager}
	nl := lot.NewLot{
		ProductID: product.ID, LotNumber: "L2026-09", ProductionDate: calendar.New(2026, time.September, 30),
		ExpirationDate: calendar.New(2026, time.October, 14), Quantity: 500, LocationID: plant.ID,
	}

	created, err := svc.Commission(t.Context(), caller, nl)
	if err != nil || created.OwnerTenantID != owner.ID || created.GTIN != product.GTIN || created.ProductName != "Fixture Product" ||
		created.MinTempCelsius != 2 || created.MaxTempCelsius != 8 || created.ExpirationDate != nl.ExpirationDate ||
		created.QuantityCommissioned != 500 || created.Status != policy.LotActive || created.RecalledAt != nil {
		t.Fatalf("Commission() = %+v, %v", created, err)
	}
	var balance, delta int
	var reason string
	var createdBy uuid.UUID
	if err := db.Owner.QueryRow(t.Context(), `
		SELECT b.quantity_on_hand, m.quantity_delta, m.reason, m.created_by
		FROM core.inventory_balances b
		JOIN core.inventory_movements m ON m.location_id = b.location_id AND m.lot_id = b.lot_id
		WHERE b.location_id = $1 AND b.lot_id = $2`, plant.ID, created.ID).Scan(&balance, &delta, &reason, &createdBy); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if balance != 500 || delta != 500 || reason != "COMMISSIONED" || createdBy != manager.ID {
		t.Errorf("ledger = balance %d, movement %d %s by %s", balance, delta, reason, createdBy)
	}

	// Changing the product later leaves the lot's copy as it was.
	if _, err := db.Owner.Exec(t.Context(), `UPDATE core.products SET name = 'Renamed', max_temp_celsius = 4 WHERE id = $1`, product.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Get(t.Context(), caller, created.ID); err != nil || got.ProductName != "Fixture Product" || got.MaxTempCelsius != 8 {
		t.Errorf("Get() after a product change = %+v, %v", got, err)
	}

	if _, err := svc.Commission(t.Context(), caller, nl); !errors.Is(err, lot.ErrLotNumberTaken) {
		t.Errorf("same lot number again: error = %v, want ErrLotNumberTaken", err)
	}
	foreignProduct, foreignLocation := db.CreateProduct(t, other), db.CreateLocation(t, other)
	for name, tt := range map[string]struct {
		product, location uuid.UUID
		check             policy.Check
	}{
		"product of another tenant":  {foreignProduct.ID, plant.ID, policy.ProductOwned},
		"location of another tenant": {product.ID, foreignLocation.ID, policy.LocationOwned},
	} {
		bad := nl
		bad.LotNumber, bad.ProductID, bad.LocationID = "L-OTHER", tt.product, tt.location
		var d *policy.DenialError
		if _, err := svc.Commission(t.Context(), caller, bad); !errors.As(err, &d) || d.Check != tt.check {
			t.Errorf("%s: error = %v, want the %s check to fail", name, err, tt.check)
		}
	}
	if _, err := db.Owner.Exec(t.Context(), `UPDATE core.locations SET is_active = false WHERE id = $1`, plant.ID); err != nil {
		t.Fatal(err)
	}
	nl.LotNumber = "L2026-10"
	if _, err := svc.Commission(t.Context(), caller, nl); !errors.Is(err, lot.ErrLocationInactive) {
		t.Errorf("inactive location: error = %v, want ErrLocationInactive", err)
	}
	var lots int
	if err := db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.lots WHERE tenant_id = $1`, owner.ID).Scan(&lots); err != nil || lots != 1 {
		t.Errorf("%d lots after the failed commissions, %v; want 1", lots, err)
	}
}

// A lot is visible to its owner and to every tenant with a balance of it, even an empty one; only the owner
// changes it (ADR-0002).
func TestLotVisibility(t *testing.T) {
	db := tenancytest.Start(t)
	store := lot.NewPostgresStore(db.Tenancy)
	owner, holder, former, stranger := db.CreateTenant(t), db.CreateTenant(t), db.CreateTenant(t), db.CreateTenant(t)
	product := db.CreateProduct(t, owner)
	shipped := db.CommissionLot(t, owner, product, owner.Headquarters, 100)
	kept := db.CommissionLot(t, owner, product, owner.Headquarters, 100)
	db.AddBalance(t, holder, db.CreateLocation(t, holder), shipped.ID, 40)
	db.AddBalance(t, former, db.CreateLocation(t, former), shipped.ID, 0)

	visible := func(tenant uuid.UUID) []uuid.UUID {
		t.Helper()
		var ids []uuid.UUID
		if err := store.WithTenantTx(t.Context(), tenant, func(repo lot.Repository) error {
			lots, err := repo.List(t.Context(), lot.Filter{Limit: 100})
			for _, l := range lots {
				ids = append(ids, l.ID)
			}
			return err
		}); err != nil {
			t.Fatalf("list: %v", err)
		}
		return ids
	}
	if got := visible(owner.ID); len(got) != 2 || got[0] != kept.ID || got[1] != shipped.ID {
		t.Errorf("owner sees %v, want both lots newest first", got)
	}
	for name, tenant := range map[string]uuid.UUID{"holder": holder.ID, "former holder": former.ID} {
		if got := visible(tenant); len(got) != 1 || got[0] != shipped.ID {
			t.Errorf("%s sees %v, want only the shipped lot", name, got)
		}
		err := store.WithTenantTx(t.Context(), tenant, func(repo lot.Repository) error {
			l, err := repo.Get(t.Context(), shipped.ID)
			if err == nil && (l.OwnerTenantID != owner.ID || l.GTIN != product.GTIN) {
				t.Errorf("%s reads %+v", name, l)
			}
			return err
		})
		if err != nil {
			t.Errorf("%s Get(): %v", name, err)
		}
		db.AssertReadOnly(t, tenant, tenancytest.Rows{Table: "core.lots", Where: map[string]any{"id": shipped.ID}})
	}
	if got := visible(stranger.ID); len(got) != 0 {
		t.Errorf("stranger sees %v", got)
	}
	db.AssertHidden(t, stranger.ID, tenancytest.Rows{Table: "core.lots", Where: map[string]any{"id": shipped.ID}})
	db.AssertHidden(t, holder.ID, tenancytest.Rows{Table: "core.lots", Where: map[string]any{"id": kept.ID}})
	db.AssertDenied(t, stranger.ID, `DELETE FROM core.lots WHERE id = $1`, shipped.ID)

	t.Run("filters by product and status", func(t *testing.T) {
		if _, err := db.Owner.Exec(t.Context(), `UPDATE core.lots SET status = 'RECALLED', recalled_at = now() WHERE id = $1`, kept.ID); err != nil {
			t.Fatal(err)
		}
		recalled, other := policy.LotRecalled, uuid.New()
		err := store.WithTenantTx(t.Context(), owner.ID, func(repo lot.Repository) error {
			got, err := repo.List(t.Context(), lot.Filter{Status: &recalled, ProductID: &product.ID, Limit: 10})
			if err != nil || len(got) != 1 || got[0].ID != kept.ID || got[0].RecalledAt == nil {
				t.Errorf("recalled lots = %+v, %v", got, err)
			}
			if got, err := repo.List(t.Context(), lot.Filter{ProductID: &other, Limit: 10}); err != nil || len(got) != 0 {
				t.Errorf("lots of another product = %+v, %v", got, err)
			}
			if got, err := repo.List(t.Context(), lot.Filter{After: kept.ID, Limit: 10}); err != nil || len(got) != 1 || got[0].ID != shipped.ID {
				t.Errorf("page after the newest lot = %+v, %v", got, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
