//go:build integration

package location_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// glnFor returns the GLN with location reference ref under prefix. Tests use references from 50, above the
// ones that fixtures take.
func glnFor(prefix string, ref int) string {
	payload := prefix + fmt.Sprintf("%0*d", 12-len(prefix), ref)
	return payload + strconv.Itoa(gs1.CheckDigit(payload))
}

func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := location.NewPostgresStore(db.Tenancy)
	a, b := db.CreateTenant(t), db.CreateTenant(t)

	// in runs fn in a transaction of tenant, failing the test on errors.
	in := func(t *testing.T, tenant uuid.UUID, fn func(location.Repository) error) {
		t.Helper()
		if err := store.WithTenantTx(t.Context(), tenant, fn); err != nil {
			t.Fatalf("tenant transaction: %v", err)
		}
	}
	newLocation := func(gln string) location.NewLocation {
		return location.NewLocation{
			GLN: gln, Name: "Cold Store", Address: "5 Tan Thuan", City: "Ho Chi Minh City", CountryCode: "VN",
			Latitude: 10.762622, Longitude: -106.742801, GeoFenceRadiusMeters: 300,
		}
	}

	var created location.Location
	t.Run("creates locations and rejects taken GLNs in any tenant", func(t *testing.T) {
		in(t, a.ID, func(repo location.Repository) error {
			var err error
			created, err = repo.Create(t.Context(), a.ID, newLocation(glnFor(a.GCP, 50)))
			if err != nil || created.GLN != glnFor(a.GCP, 50) || created.Latitude != 10.762622 ||
				created.Longitude != -106.742801 || created.GeoFenceRadiusMeters != 300 || !created.IsActive ||
				created.IsHeadquarters || created.CreatedAt.IsZero() {
				t.Errorf("Create() = %+v, %v", created, err)
			}
			got, err := repo.Get(t.Context(), created.ID)
			if err != nil || got != created {
				t.Errorf("Get() = %+v, %v; want %+v", got, err, created)
			}
			return err
		})
		// A failed statement aborts the transaction, so the error ends it, as in the service.
		err := store.WithTenantTx(t.Context(), b.ID, func(repo location.Repository) error {
			_, err := repo.Create(t.Context(), b.ID, newLocation(glnFor(a.GCP, 50)))
			return err
		})
		if !errors.Is(err, location.ErrGLNTaken) {
			t.Errorf("same GLN in another tenant: error = %v, want ErrGLNTaken", err)
		}
	})

	t.Run("reads the tenant's company prefix", func(t *testing.T) {
		in(t, a.ID, func(repo location.Repository) error {
			gcp, err := repo.CompanyPrefix(t.Context(), a.ID)
			if err != nil || gcp != a.GCP {
				t.Errorf("CompanyPrefix() = %q, %v; want %q", gcp, err, a.GCP)
			}
			return err
		})
	})

	t.Run("pages through locations newest first", func(t *testing.T) {
		tenant := db.CreateTenant(t)
		for range 4 {
			db.CreateLocation(t, tenant)
		}
		var seen []uuid.UUID
		after := uuid.Nil
		for {
			var page []location.Location
			in(t, tenant.ID, func(repo location.Repository) error {
				var err error
				page, err = repo.List(t.Context(), location.Filter{After: after, Limit: 2})
				return err
			})
			for _, l := range page {
				seen = append(seen, l.ID)
			}
			if len(page) < 2 {
				break
			}
			after = page[len(page)-1].ID
		}
		if len(seen) != 5 { // four fixtures and the headquarters
			t.Fatalf("paged through %d locations, want 5", len(seen))
		}
		for i := 1; i < len(seen); i++ {
			if strings.Compare(seen[i-1].String(), seen[i].String()) <= 0 {
				t.Errorf("locations out of order at %d: %s then %s", i, seen[i-1], seen[i])
			}
		}

		inactive := false
		in(t, tenant.ID, func(repo location.Repository) error {
			if _, err := repo.Update(t.Context(), seen[0], location.Patch{IsActive: &inactive}); err != nil {
				return err
			}
			got, err := repo.List(t.Context(), location.Filter{IsActive: &inactive, Limit: 100})
			if err != nil || len(got) != 1 || got[0].ID != seen[0] {
				t.Errorf("inactive locations = %+v, %v; want only %s", got, err, seen[0])
			}
			return err
		})
	})

	t.Run("patches only the given fields", func(t *testing.T) {
		name, lat, radius := "Renamed Store", -33.868820, 5000
		in(t, a.ID, func(repo location.Repository) error {
			got, err := repo.Update(t.Context(), created.ID, location.Patch{Name: &name, Latitude: &lat, GeoFenceRadiusMeters: &radius})
			if err != nil || got.Name != name || got.Latitude != lat || got.GeoFenceRadiusMeters != radius ||
				got.Longitude != created.Longitude || got.Address != created.Address || got.GLN != created.GLN ||
				!got.UpdatedAt.After(created.UpdatedAt) {
				t.Errorf("Update() = %+v, %v", got, err)
			}
			if _, err := repo.Update(t.Context(), uuid.New(), location.Patch{Name: &name}); !errors.Is(err, location.ErrNotFound) {
				t.Errorf("missing location: error = %v, want ErrNotFound", err)
			}
			return nil
		})
	})

	t.Run("another tenant sees none of the locations", func(t *testing.T) {
		name := "Hijacked"
		in(t, b.ID, func(repo location.Repository) error {
			if _, err := repo.Get(t.Context(), created.ID); !errors.Is(err, location.ErrNotFound) {
				t.Errorf("Get() across tenants: error = %v, want ErrNotFound", err)
			}
			if _, err := repo.Update(t.Context(), created.ID, location.Patch{Name: &name}); !errors.Is(err, location.ErrNotFound) {
				t.Errorf("Update() across tenants: error = %v, want ErrNotFound", err)
			}
			locations, err := repo.List(t.Context(), location.Filter{Limit: 100})
			for _, l := range locations {
				if l.ID == created.ID || l.ID == a.Headquarters.ID {
					t.Error("List() returned a location of another tenant")
				}
			}
			return err
		})
		db.AssertHidden(t, b.ID, tenancytest.Rows{Table: "core.locations", Where: map[string]any{"id": created.ID}})
		db.AssertDenied(t, b.ID, `INSERT INTO core.locations (tenant_id, gln, name, address, city, latitude, longitude)
			VALUES ($1, $2, 'X', 'X', 'X', 0, 0)`, a.ID, glnFor(a.GCP, 51))
	})
}
