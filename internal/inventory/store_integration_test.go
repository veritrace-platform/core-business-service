//go:build integration

package inventory_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

func TestApplyMovement(t *testing.T) {
	db := tenancytest.Start(t)
	tenant := db.CreateTenant(t)
	product := db.CreateProduct(t, tenant)
	lot := db.CommissionLot(t, tenant, product, tenant.Headquarters, 10)
	store := db.CreateLocation(t, tenant)

	apply := func(m inventory.Movement) (int, error) {
		var balance int
		err := db.Tenancy.WithTenantTx(t.Context(), tenant.ID, func(tx pgx.Tx) error {
			var err error
			balance, err = inventory.ApplyMovement(t.Context(), tx, m)
			return err
		})
		return balance, err
	}
	arrival := inventory.Movement{
		TenantID: tenant.ID, LocationID: store.ID, LotID: lot.ID, Delta: 4,
		Reason: inventory.ReasonCommissioned, CreatedBy: tenant.Admin.ID,
	}
	if balance, err := apply(arrival); err != nil || balance != 4 {
		t.Fatalf("first movement: balance = %d, %v; want a new balance of 4", balance, err)
	}
	if balance, err := apply(arrival); err != nil || balance != 8 {
		t.Errorf("second movement: balance = %d, %v; want 8", balance, err)
	}

	shipment := uuid.New()
	departure := inventory.Movement{
		TenantID: tenant.ID, LocationID: store.ID, LotID: lot.ID, Delta: -9,
		Reason: inventory.ReasonShipmentCreated, ShipmentID: &shipment, CreatedBy: tenant.Admin.ID,
	}
	if _, err := apply(departure); !errors.Is(err, inventory.ErrInsufficientStock) {
		t.Errorf("taking 9 of 8: error = %v, want ErrInsufficientStock", err)
	}
	var balance, movements int
	if err := db.Owner.QueryRow(t.Context(), `
		SELECT (SELECT quantity_on_hand FROM core.inventory_balances WHERE location_id = $1 AND lot_id = $2),
		       (SELECT count(*) FROM core.inventory_movements WHERE location_id = $1 AND lot_id = $2)`,
		store.ID, lot.ID).Scan(&balance, &movements); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if balance != 8 || movements != 2 {
		t.Errorf("ledger = balance %d with %d movements, want 8 with 2: the failed movement must leave no trace", balance, movements)
	}

	// The ledger is append-only, and balances are never deleted.
	db.AssertDenied(t, tenant.ID, `UPDATE core.inventory_movements SET quantity_delta = 1 WHERE lot_id = $1`, lot.ID)
	db.AssertDenied(t, tenant.ID, `DELETE FROM core.inventory_movements WHERE lot_id = $1`, lot.ID)
	db.AssertDenied(t, tenant.ID, `DELETE FROM core.inventory_balances WHERE lot_id = $1`, lot.ID)
}

func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := inventory.NewPostgresStore(db.Tenancy)
	owner, holder, stranger := db.CreateTenant(t), db.CreateTenant(t), db.CreateTenant(t)
	product := db.CreateProduct(t, owner)
	warehouse := db.CreateLocation(t, owner)
	lots := []tenancytest.Lot{
		db.CommissionLot(t, owner, product, owner.Headquarters, 50),
		db.CommissionLot(t, owner, product, owner.Headquarters, 60),
		db.CommissionLot(t, owner, product, warehouse, 70),
	}
	if _, err := db.Owner.Exec(t.Context(), `UPDATE core.inventory_balances SET quantity_on_hand = 0 WHERE lot_id = $1`, lots[1].ID); err != nil {
		t.Fatalf("empty a balance: %v", err)
	}
	dock := db.CreateLocation(t, holder)
	db.AddBalance(t, holder, dock, lots[0].ID, 20)

	list := func(tenant uuid.UUID, f inventory.Filter) []inventory.Balance {
		t.Helper()
		var balances []inventory.Balance
		if err := store.WithTenantTx(t.Context(), tenant, func(repo inventory.Repository) error {
			var err error
			balances, err = repo.List(t.Context(), f)
			return err
		}); err != nil {
			t.Fatalf("list: %v", err)
		}
		return balances
	}

	t.Run("lists balances above zero with their location and lot", func(t *testing.T) {
		got := list(owner.ID, inventory.Filter{Limit: 10})
		if len(got) != 2 {
			t.Fatalf("balances = %+v, want the two above zero", got)
		}
		// The warehouse was created after the headquarters, so its balance comes first.
		first := got[0]
		if first.Location.ID != warehouse.ID || first.Location.GLN != warehouse.GLN || first.Location.Name != "Fixture Warehouse" ||
			first.Lot.ID != lots[2].ID || first.Lot.LotNumber != lots[2].LotNumber || first.Lot.GTIN != product.GTIN ||
			first.Lot.ProductName != "Fixture Product" || first.Lot.Status != policy.LotActive || first.QuantityOnHand != 70 ||
			first.UpdatedAt.IsZero() {
			t.Errorf("first balance = %+v", first)
		}
	})

	t.Run("filters and pages", func(t *testing.T) {
		if got := list(owner.ID, inventory.Filter{LocationID: &owner.Headquarters.ID, Limit: 10}); len(got) != 1 || got[0].Lot.ID != lots[0].ID {
			t.Errorf("headquarters balances = %+v", got)
		}
		if got := list(owner.ID, inventory.Filter{LotID: &lots[2].ID, Limit: 10}); len(got) != 1 || got[0].Location.ID != warehouse.ID {
			t.Errorf("balances of the third lot = %+v", got)
		}
		first := list(owner.ID, inventory.Filter{Limit: 1})
		after := inventory.Key{LocationID: first[0].Location.ID, LotID: first[0].Lot.ID}
		if rest := list(owner.ID, inventory.Filter{After: &after, Limit: 10}); len(rest) != 1 || rest[0].Lot.ID != lots[0].ID {
			t.Errorf("page after %+v = %+v", after, rest)
		}
	})

	t.Run("a holder lists its own stock of another tenant's lot", func(t *testing.T) {
		got := list(holder.ID, inventory.Filter{Limit: 10})
		if len(got) != 1 || got[0].Location.ID != dock.ID || got[0].Lot.ID != lots[0].ID || got[0].Lot.GTIN != product.GTIN ||
			got[0].QuantityOnHand != 20 {
			t.Errorf("holder balances = %+v", got)
		}
	})

	t.Run("another tenant sees no balances", func(t *testing.T) {
		if got := list(stranger.ID, inventory.Filter{LotID: &lots[0].ID, Limit: 10}); len(got) != 0 {
			t.Errorf("stranger balances = %+v", got)
		}
		db.AssertHidden(t, stranger.ID, tenancytest.Rows{Table: "core.inventory_balances", Where: map[string]any{"lot_id": lots[0].ID}})
		db.AssertHidden(t, stranger.ID, tenancytest.Rows{Table: "core.inventory_movements", Where: map[string]any{"lot_id": lots[0].ID}})
		db.AssertDenied(t, stranger.ID, `INSERT INTO core.inventory_balances (tenant_id, location_id, lot_id, quantity_on_hand)
			VALUES ($1, $2, $3, 1000)`, owner.ID, warehouse.ID, lots[0].ID)
	})

	t.Run("a stock movement at another tenant's location is rejected", func(t *testing.T) {
		// The location belongs to the owner, so the composite key rejects the row even in the stranger's context.
		err := db.Tenancy.WithTenantTx(t.Context(), stranger.ID, func(tx pgx.Tx) error {
			_, err := inventory.ApplyMovement(t.Context(), tx, inventory.Movement{
				TenantID: stranger.ID, LocationID: warehouse.ID, LotID: lots[0].ID, Delta: 5,
				Reason: inventory.ReasonCommissioned, CreatedBy: stranger.Admin.ID,
			})
			return err
		})
		if err == nil {
			t.Error("a movement at another tenant's location succeeded")
		}
	})

}
