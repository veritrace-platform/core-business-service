//go:build integration

package product_test

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/product"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// gtinFor returns a base-unit GTIN-14 with item reference ref under prefix.
func gtinFor(prefix string, ref int) string {
	payload := "0" + prefix + fmt.Sprintf("%0*d", 12-len(prefix), ref)
	return payload + strconv.Itoa(gs1.CheckDigit(payload))
}

func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := product.NewPostgresStore(db.Tenancy)
	a, b := db.CreateTenant(t), db.CreateTenant(t)

	// in runs fn in a transaction of tenant, failing the test on errors.
	in := func(t *testing.T, tenant uuid.UUID, fn func(product.Repository) error) {
		t.Helper()
		if err := store.WithTenantTx(t.Context(), tenant, fn); err != nil {
			t.Fatalf("tenant transaction: %v", err)
		}
	}
	create := func(t *testing.T, tenant tenancytest.Tenant, ref int, name string) product.Product {
		t.Helper()
		var p product.Product
		in(t, tenant.ID, func(repo product.Repository) error {
			var err error
			p, err = repo.Create(t.Context(), tenant.ID, product.NewProduct{
				GTIN: gtinFor(tenant.GCP, ref), Name: name, MinTempCelsius: 2, MaxTempCelsius: 8,
			})
			return err
		})
		return p
	}

	var milk product.Product
	t.Run("creates products and rejects taken GTINs in any tenant", func(t *testing.T) {
		description := "Pasteurized, 1 L"
		in(t, a.ID, func(repo product.Repository) error {
			var err error
			milk, err = repo.Create(t.Context(), a.ID, product.NewProduct{
				GTIN: gtinFor(a.GCP, 1), Name: "Chilled milk", Description: &description, MinTempCelsius: -2.5, MaxTempCelsius: 8.25,
			})
			if err != nil || milk.GTIN != gtinFor(a.GCP, 1) || milk.MinTempCelsius != -2.5 || milk.MaxTempCelsius != 8.25 ||
				milk.Description == nil || *milk.Description != description || !milk.IsActive || milk.CreatedAt.IsZero() {
				t.Errorf("Create() = %+v, %v", milk, err)
			}
			return err
		})
		err := store.WithTenantTx(t.Context(), b.ID, func(repo product.Repository) error {
			_, err := repo.Create(t.Context(), b.ID, product.NewProduct{GTIN: milk.GTIN, Name: "Copy", MinTempCelsius: 2, MaxTempCelsius: 8})
			return err
		})
		if !errors.Is(err, product.ErrGTINTaken) {
			t.Errorf("same GTIN in another tenant: error = %v, want ErrGTINTaken", err)
		}
	})

	t.Run("searches names and GTINs", func(t *testing.T) {
		tenant := db.CreateTenant(t)
		yogurt := create(t, tenant, 2, "Strawberry Yogurt 100% natural")
		create(t, tenant, 3, "Frozen shrimp")
		search := func(text string) []product.Product {
			var got []product.Product
			in(t, tenant.ID, func(repo product.Repository) error {
				var err error
				got, err = repo.List(t.Context(), product.Filter{Search: text, Limit: 100})
				return err
			})
			return got
		}
		for text, want := range map[string]int{"yogurt": 1, "STRAWBERRY": 1, yogurt.GTIN[3:9]: 2, "100%": 1, "%": 1, "_": 0, "": 2, "milk": 0} {
			if got := search(text); len(got) != want {
				t.Errorf("search %q: %d products, want %d", text, len(got), want)
			}
		}
	})

	t.Run("pages and filters products", func(t *testing.T) {
		tenant := db.CreateTenant(t)
		var ids []uuid.UUID
		for ref := range 5 {
			ids = append(ids, create(t, tenant, 10+ref, "Item").ID)
		}
		inactive := false
		in(t, tenant.ID, func(repo product.Repository) error {
			if _, err := repo.Update(t.Context(), ids[0], product.Patch{IsActive: &inactive}); err != nil {
				return err
			}
			page, err := repo.List(t.Context(), product.Filter{After: ids[3], Limit: 10})
			if err != nil || len(page) != 3 || page[0].ID != ids[2] || page[2].ID != ids[0] {
				t.Errorf("page after the fourth product = %d products, %v; want the three older ones, newest first", len(page), err)
			}
			got, err := repo.List(t.Context(), product.Filter{IsActive: &inactive, Limit: 10})
			if err != nil || len(got) != 1 || got[0].ID != ids[0] {
				t.Errorf("inactive products = %+v, %v", got, err)
			}
			return err
		})
	})

	t.Run("patches only the given fields and keeps the range valid", func(t *testing.T) {
		name, minTemp, maxTemp := "Chilled milk 1 L", 4.0, 3.5
		in(t, a.ID, func(repo product.Repository) error {
			got, err := repo.Update(t.Context(), milk.ID, product.Patch{Name: &name, MinTempCelsius: &minTemp, ClearDescription: true})
			if err != nil || got.Name != name || got.MinTempCelsius != 4 || got.MaxTempCelsius != milk.MaxTempCelsius ||
				got.Description != nil || got.GTIN != milk.GTIN {
				t.Errorf("Update() = %+v, %v", got, err)
			}
			if _, err := repo.Update(t.Context(), uuid.New(), product.Patch{Name: &name}); !errors.Is(err, product.ErrNotFound) {
				t.Errorf("missing product: error = %v, want ErrNotFound", err)
			}
			return nil
		})
		// A maximum below the stored minimum violates the range check, which ends the transaction.
		err := store.WithTenantTx(t.Context(), a.ID, func(repo product.Repository) error {
			_, err := repo.Update(t.Context(), milk.ID, product.Patch{MaxTempCelsius: &maxTemp})
			return err
		})
		if !errors.Is(err, product.ErrTemperatureRange) {
			t.Errorf("crossing bounds: error = %v, want ErrTemperatureRange", err)
		}
	})

	t.Run("another tenant sees none of the products", func(t *testing.T) {
		name := "Hijacked"
		in(t, b.ID, func(repo product.Repository) error {
			if _, err := repo.Get(t.Context(), milk.ID); !errors.Is(err, product.ErrNotFound) {
				t.Errorf("Get() across tenants: error = %v, want ErrNotFound", err)
			}
			if _, err := repo.Update(t.Context(), milk.ID, product.Patch{Name: &name}); !errors.Is(err, product.ErrNotFound) {
				t.Errorf("Update() across tenants: error = %v, want ErrNotFound", err)
			}
			products, err := repo.List(t.Context(), product.Filter{Limit: 100})
			if len(products) != 0 {
				t.Errorf("List() returned %d products of another tenant", len(products))
			}
			return err
		})
		db.AssertHidden(t, b.ID, tenancytest.Rows{Table: "core.products", Where: map[string]any{"id": milk.ID}})
		db.AssertDenied(t, b.ID, `INSERT INTO core.products (tenant_id, gtin, name, min_temp_celsius, max_temp_celsius)
			VALUES ($1, $2, 'X', 2, 8)`, a.ID, gtinFor(a.GCP, 99))
		db.AssertDenied(t, a.ID, `DELETE FROM core.products WHERE id = $1`, milk.ID)
	})
}
