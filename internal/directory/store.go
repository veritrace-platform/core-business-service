package directory

import (
	"context"
	"fmt"

	"github.com/veritrace-platform/core-business-service/internal/directory/queries"
)

// PostgresStore looks up entries through the security-definer functions of the core schema, which read every
// tenant without a tenant context and return public fields only.
type PostgresStore struct {
	pool queries.DBTX
}

// NewPostgresStore returns a store that calls the lookup functions through pool, a pool of the runtime role.
func NewPostgresStore(pool queries.DBTX) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// LocationByGLN calls core.lookup_location_by_gln, which returns NULL in every column for a GLN without an
// active location.
func (s *PostgresStore) LocationByGLN(ctx context.Context, gln string) (Location, error) {
	row, err := queries.New(s.pool).LookupLocationByGLN(ctx, gln)
	if err != nil {
		return Location{}, fmt.Errorf("look up GLN: %w", err)
	}
	if row.Gln == nil || row.Name == nil || row.Address == nil || row.City == nil || row.CountryCode == nil ||
		row.Latitude == nil || row.Longitude == nil || row.GeoFenceRadiusMeters == nil ||
		row.TenantID == nil || row.TenantCode == nil || row.TenantLegalName == nil {
		return Location{}, ErrNotFound
	}
	return Location{
		GLN:                  *row.Gln,
		Name:                 *row.Name,
		Address:              *row.Address,
		City:                 *row.City,
		CountryCode:          *row.CountryCode,
		Latitude:             *row.Latitude,
		Longitude:            *row.Longitude,
		GeoFenceRadiusMeters: int(*row.GeoFenceRadiusMeters),
		Tenant:               Tenant{ID: *row.TenantID, Code: *row.TenantCode, LegalName: *row.TenantLegalName},
	}, nil
}

// TenantByCode calls core.lookup_tenant_by_code, which returns NULL in every column for a code without an
// active tenant.
func (s *PostgresStore) TenantByCode(ctx context.Context, code string) (Tenant, error) {
	row, err := queries.New(s.pool).LookupTenantByCode(ctx, code)
	if err != nil {
		return Tenant{}, fmt.Errorf("look up tenant code: %w", err)
	}
	if row.TenantID == nil || row.Code == nil || row.LegalName == nil {
		return Tenant{}, ErrNotFound
	}
	return Tenant{ID: *row.TenantID, Code: *row.Code, LegalName: *row.LegalName}, nil
}
