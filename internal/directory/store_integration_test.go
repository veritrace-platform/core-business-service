//go:build integration

package directory_test

import (
	"errors"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/directory"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// The lookups run as the runtime role without a tenant context, as the service calls them.
func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := directory.NewPostgresStore(db.App)
	a := db.CreateTenant(t)
	warehouse := db.CreateLocation(t, a)

	t.Run("resolves a GLN to the location's public fields and its tenant", func(t *testing.T) {
		got, err := store.LocationByGLN(t.Context(), warehouse.GLN)
		want := directory.Location{
			GLN: warehouse.GLN, Name: "Fixture Warehouse", Address: "2 Fixture Street", City: "Ho Chi Minh City",
			CountryCode: "VN", Latitude: 10.8, Longitude: 106.65, GeoFenceRadiusMeters: 200,
			Tenant: directory.Tenant{ID: a.ID, Code: a.Code, LegalName: got.Tenant.LegalName},
		}
		if err != nil || got != want || got.Tenant.LegalName == "" {
			t.Errorf("LocationByGLN() = %+v, %v; want %+v", got, err, want)
		}
		if _, err := store.LocationByGLN(t.Context(), "4006381333931"); !errors.Is(err, directory.ErrNotFound) {
			t.Errorf("unknown GLN: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("resolves a tenant code", func(t *testing.T) {
		got, err := store.TenantByCode(t.Context(), a.Code)
		if err != nil || got.ID != a.ID || got.Code != a.Code || got.LegalName == "" {
			t.Errorf("TenantByCode() = %+v, %v", got, err)
		}
		if _, err := store.TenantByCode(t.Context(), "NO_SUCH_TENANT"); !errors.Is(err, directory.ErrNotFound) {
			t.Errorf("unknown code: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("hides inactive locations", func(t *testing.T) {
		closed := db.CreateLocation(t, a)
		if _, err := db.Owner.Exec(t.Context(), `UPDATE core.locations SET is_active = false WHERE id = $1`, closed.ID); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
		if _, err := store.LocationByGLN(t.Context(), closed.GLN); !errors.Is(err, directory.ErrNotFound) {
			t.Errorf("inactive location: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("hides suspended tenants and their locations", func(t *testing.T) {
		suspended := db.CreateTenant(t)
		if _, err := db.Owner.Exec(t.Context(), `UPDATE core.tenants SET status = 'SUSPENDED' WHERE id = $1`, suspended.ID); err != nil {
			t.Fatalf("suspend: %v", err)
		}
		if _, err := store.TenantByCode(t.Context(), suspended.Code); !errors.Is(err, directory.ErrNotFound) {
			t.Errorf("suspended tenant: error = %v, want ErrNotFound", err)
		}
		if _, err := store.LocationByGLN(t.Context(), suspended.Headquarters.GLN); !errors.Is(err, directory.ErrNotFound) {
			t.Errorf("location of a suspended tenant: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("the runtime role cannot read the tables the lookups read", func(t *testing.T) {
		b := db.CreateTenant(t)
		db.AssertHidden(t, b.ID, tenancytest.Rows{Table: "core.locations", Where: map[string]any{"id": warehouse.ID}})
		db.AssertHidden(t, tenancytest.NoTenant, tenancytest.Rows{Table: "core.tenants", Where: map[string]any{"id": a.ID}})
	})
}
