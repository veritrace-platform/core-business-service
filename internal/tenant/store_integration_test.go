//go:build integration

package tenant_test

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
)

const testHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"

// glnFor returns a valid GLN under prefix with location reference ref.
func glnFor(prefix string, ref int) string {
	payload := prefix + fmt.Sprintf("%0*d", 12-len(prefix), ref)
	return payload + strconv.Itoa(gs1.CheckDigit(payload))
}

// registration returns a valid registration for prefix; n makes the other unique keys distinct.
func registration(n int, prefix string) tenant.Registration {
	return tenant.Registration{
		Code:             fmt.Sprintf("TENANT_%d", n),
		LegalName:        fmt.Sprintf("Tenant %d", n),
		TaxCode:          fmt.Sprintf("%010d", 1000+n),
		GS1CompanyPrefix: prefix,
		Headquarters: tenant.Headquarters{
			GLN: glnFor(prefix, n), Name: "Headquarters", Address: "1 Test Street",
			City: "Da Nang", CountryCode: "VN", Latitude: 16.054407, Longitude: 108.202167, GeoFenceRadiusMeters: 300,
		},
		Admin: tenant.Admin{Email: fmt.Sprintf("admin%d@tenant.example", n), FullName: "Admin"},
	}
}

func TestRegisterTenant(t *testing.T) {
	db := tenancytest.Start(t)
	store := tenant.NewStore(db.App, db.Tenancy)

	t.Run("creates the tenant, its headquarters, and its admin", func(t *testing.T) {
		r := registration(1, "8930001")
		got, err := store.RegisterTenant(t.Context(), r, testHash)
		if err != nil {
			t.Fatalf("RegisterTenant() error = %v", err)
		}

		var code, status, gln, email, role, hash string
		var isHQ bool
		var lat, radius float64
		err = db.Owner.QueryRow(t.Context(), `
			SELECT t.code, t.status, l.gln, l.is_headquarters, l.latitude::float8, l.geo_fence_radius_meters,
			       u.email, u.role, u.password_hash
			FROM core.tenants t
			JOIN core.locations l ON l.tenant_id = t.id AND l.id = $2
			JOIN core.users u ON u.tenant_id = t.id AND u.id = $3
			WHERE t.id = $1`, got.Tenant.ID, got.Headquarters.ID, got.Admin.ID).
			Scan(&code, &status, &gln, &isHQ, &lat, &radius, &email, &role, &hash)
		if err != nil {
			t.Fatalf("read registered rows: %v", err)
		}
		if code != r.Code || status != "ACTIVE" || gln != r.Headquarters.GLN || !isHQ || lat != 16.054407 ||
			radius != 300 || email != r.Admin.Email || role != "ADMIN" || hash != testHash {
			t.Errorf("stored %s %s %s %t %v %v %s %s %s", code, status, gln, isHQ, lat, radius, email, role, hash)
		}
		if got.Tenant.CreatedAt.IsZero() || got.Admin.Role != identity.RoleAdmin || !got.Headquarters.IsHeadquarters {
			t.Errorf("result = %+v", got)
		}
	})

	t.Run("rejects taken keys", func(t *testing.T) {
		existing := registration(2, "8930002")
		if _, err := store.RegisterTenant(t.Context(), existing, testHash); err != nil {
			t.Fatalf("RegisterTenant() error = %v", err)
		}
		tests := []struct {
			key  tenant.Key
			edit func(*tenant.Registration)
		}{
			{tenant.KeyCode, func(r *tenant.Registration) { r.Code = existing.Code }},
			{tenant.KeyTaxCode, func(r *tenant.Registration) { r.TaxCode = existing.TaxCode }},
			{tenant.KeyCompanyPrefix, func(r *tenant.Registration) { moveTo(r, existing.GS1CompanyPrefix) }},
			{tenant.KeyCompanyPrefix, func(r *tenant.Registration) { moveTo(r, "893000") }},   // extended by 8930002
			{tenant.KeyCompanyPrefix, func(r *tenant.Registration) { moveTo(r, "89300021") }}, // extends 8930002
			{tenant.KeyAdminEmail, func(r *tenant.Registration) { r.Admin.Email = "ADMIN2@Tenant.Example" }},
		}
		for i, tt := range tests {
			r := registration(100+i, "8931"+strconv.Itoa(100+i))
			tt.edit(&r)
			_, err := store.RegisterTenant(t.Context(), r, testHash)
			var conflict *tenant.ConflictError
			if !errors.As(err, &conflict) || conflict.Key != tt.key {
				t.Errorf("case %d: error = %v, want a conflict on %s", i, err, tt.key)
			}
		}
	})

	t.Run("rejects a taken headquarters GLN", func(t *testing.T) {
		first := registration(300, "8933000")
		if _, err := store.RegisterTenant(t.Context(), first, testHash); err != nil {
			t.Fatalf("RegisterTenant() error = %v", err)
		}
		// The API rejects this GLN for the prefix first; the unique constraint is the last line of defense.
		second := registration(301, "8933001")
		second.Headquarters.GLN = first.Headquarters.GLN
		var conflict *tenant.ConflictError
		if _, err := store.RegisterTenant(t.Context(), second, testHash); !errors.As(err, &conflict) ||
			conflict.Key != tenant.KeyHeadquartersGLN {
			t.Errorf("error = %v, want a conflict on the headquarters GLN", err)
		}
	})

	t.Run("serializes registrations with overlapping prefixes", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, prefix := range []string{"8934000", "89340001"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = store.RegisterTenant(t.Context(), registration(400+i, prefix), testHash)
			}()
		}
		wg.Wait()
		succeeded := 0
		for _, err := range errs {
			var conflict *tenant.ConflictError
			switch {
			case err == nil:
				succeeded++
			case !errors.As(err, &conflict) || conflict.Key != tenant.KeyCompanyPrefix:
				t.Errorf("unexpected error: %v", err)
			}
		}
		if succeeded != 1 {
			t.Errorf("%d registrations succeeded, want exactly 1", succeeded)
		}
	})
}

// moveTo gives a registration another company prefix and a headquarters GLN under it.
func moveTo(r *tenant.Registration, prefix string) {
	r.GS1CompanyPrefix = prefix
	r.Headquarters.GLN = glnFor(prefix, 900)
}

func TestTenantDataIsolation(t *testing.T) {
	db := tenancytest.Start(t)
	a, b := db.CreateTenant(t), db.CreateTenant(t)

	tables := []struct {
		name      string
		rowsOf    func(tenancytest.Tenant) tenancytest.Rows
		insertFor string // inserts a row for the tenant in $1
	}{
		{
			name:   "tenants",
			rowsOf: func(x tenancytest.Tenant) tenancytest.Rows { return rows("core.tenants", x.ID) },
		},
		{
			name:      "users",
			rowsOf:    func(x tenancytest.Tenant) tenancytest.Rows { return rows("core.users", x.Admin.ID) },
			insertFor: `INSERT INTO core.users (tenant_id, email, password_hash, full_name, role) VALUES ($1, 'x@y.example', '` + testHash + `', 'X', 'DRIVER')`,
		},
		{
			name:      "locations",
			rowsOf:    func(x tenancytest.Tenant) tenancytest.Rows { return rows("core.locations", x.HeadquartersID) },
			insertFor: `INSERT INTO core.locations (tenant_id, gln, name, address, city, latitude, longitude) VALUES ($1, '9999999999994', 'X', 'X', 'X', 0, 0)`,
		},
	}
	for _, tt := range tables {
		t.Run(tt.name, func(t *testing.T) {
			db.AssertVisible(t, a.ID, tt.rowsOf(a))
			db.AssertHidden(t, a.ID, tt.rowsOf(b))
			db.AssertHidden(t, tenancytest.NoTenant, tt.rowsOf(a))
			if tt.insertFor != "" {
				db.AssertDenied(t, a.ID, tt.insertFor, b.ID)
			}
		})
	}

	t.Run("tenants are created only by registration", func(t *testing.T) {
		db.AssertDenied(t, a.ID, `INSERT INTO core.tenants (id, code, legal_name, tax_code, gs1_company_prefix) VALUES ($1, 'NEW', 'New', '0000000009', '9800000000')`, a.ID)
	})

	t.Run("users and locations are never deleted", func(t *testing.T) {
		db.AssertDenied(t, a.ID, `DELETE FROM core.users WHERE id = $1`, a.Admin.ID)
		db.AssertDenied(t, a.ID, `DELETE FROM core.locations WHERE id = $1`, a.HeadquartersID)
		db.AssertDenied(t, a.ID, `DELETE FROM core.tenants WHERE id = $1`, a.ID)
	})

	t.Run("a tenant cannot move its rows to another tenant", func(t *testing.T) {
		db.AssertDenied(t, a.ID, `UPDATE core.users SET tenant_id = $1 WHERE id = $2`, b.ID, a.Admin.ID)
		db.AssertDenied(t, a.ID, `UPDATE core.locations SET tenant_id = $1 WHERE id = $2`, b.ID, a.HeadquartersID)
	})
}

func rows(table string, id any) tenancytest.Rows {
	return tenancytest.Rows{Table: table, Where: map[string]any{"id": id}}
}
