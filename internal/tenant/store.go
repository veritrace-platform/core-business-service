package tenant

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/tenant/queries"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// Store keeps tenants in PostgreSQL.
type Store struct {
	pool queries.DBTX
}

// NewStore returns a Store that registers tenants through pool, a connection pool of the runtime role.
func NewStore(pool queries.DBTX) *Store {
	return &Store{pool: pool}
}

// conflictKeys maps the unique constraints that registration can violate to registration keys.
var conflictKeys = map[string]Key{
	"tenants_code_key":               KeyCode,
	"tenants_tax_code_key":           KeyTaxCode,
	"tenants_gs1_company_prefix_key": KeyCompanyPrefix,
	"locations_gln_key":              KeyHeadquartersGLN,
	"users_email_key":                KeyAdminEmail,
}

// RegisterTenant runs core.register_tenant, which needs no tenant context.
func (s *Store) RegisterTenant(ctx context.Context, r Registration, passwordHash string) (Registered, error) {
	hq := r.Headquarters
	row, err := queries.New(s.pool).RegisterTenant(ctx, queries.RegisterTenantParams{
		Code:                             r.Code,
		LegalName:                        r.LegalName,
		TaxCode:                          r.TaxCode,
		Gs1CompanyPrefix:                 r.GS1CompanyPrefix,
		HeadquartersGln:                  hq.GLN,
		HeadquartersName:                 hq.Name,
		HeadquartersAddress:              hq.Address,
		HeadquartersCity:                 hq.City,
		HeadquartersCountryCode:          hq.CountryCode,
		HeadquartersLatitude:             coordinate(hq.Latitude),
		HeadquartersLongitude:            coordinate(hq.Longitude),
		HeadquartersGeoFenceRadiusMeters: int32(hq.GeoFenceRadiusMeters), //nolint:gosec // validated to 50–5000
		AdminEmail:                       r.Admin.Email,
		AdminPasswordHash:                passwordHash,
		AdminFullName:                    r.Admin.FullName,
		AdminPhone:                       r.Admin.Phone,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if key, ok := conflictKeys[pgErr.ConstraintName]; ok {
				return Registered{}, &ConflictError{Key: key}
			}
		}
		return Registered{}, err
	}
	if row.TenantID == nil || row.HeadquartersLocationID == nil || row.AdminUserID == nil || row.CreatedAt == nil {
		return Registered{}, errors.New("core.register_tenant returned an incomplete result")
	}

	// Every record was created in one transaction, so they share its timestamp. The other values are the
	// validated input and the column defaults.
	created := *row.CreatedAt
	return Registered{
		Tenant: Tenant{
			ID:               *row.TenantID,
			Code:             r.Code,
			LegalName:        r.LegalName,
			TaxCode:          r.TaxCode,
			GS1CompanyPrefix: r.GS1CompanyPrefix,
			Status:           StatusActive,
			CreatedAt:        created,
			UpdatedAt:        created,
		},
		Headquarters: location.Location{
			ID:                   *row.HeadquartersLocationID,
			GLN:                  hq.GLN,
			Name:                 hq.Name,
			Address:              hq.Address,
			City:                 hq.City,
			CountryCode:          hq.CountryCode,
			Latitude:             hq.Latitude,
			Longitude:            hq.Longitude,
			GeoFenceRadiusMeters: hq.GeoFenceRadiusMeters,
			IsHeadquarters:       true,
			IsActive:             true,
			CreatedAt:            created,
			UpdatedAt:            created,
		},
		Admin: user.User{
			ID:        *row.AdminUserID,
			Email:     r.Admin.Email,
			FullName:  r.Admin.FullName,
			Phone:     r.Admin.Phone,
			Role:      identity.RoleAdmin,
			IsActive:  true,
			CreatedAt: created,
			UpdatedAt: created,
		},
	}, nil
}

// coordinate converts degrees, already rounded to six decimals, to numeric(9, 6).
func coordinate(degrees float64) pgtype.Numeric {
	var n pgtype.Numeric
	// A fixed-point decimal string is always a valid numeric.
	_ = n.Scan(location.FormatCoordinate(degrees))
	return n
}
