package location

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/location/queries"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// PostgresStore keeps locations in PostgreSQL.
type PostgresStore struct {
	db *tenancy.DB
}

// NewPostgresStore returns a store that runs tenant-scoped work through db.
func NewPostgresStore(db *tenancy.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// WithTenantTx runs fn with a repository bound to one transaction of tenantID.
func (s *PostgresStore) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error {
	return s.db.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return fn(repository{q: queries.New(tx)})
	})
}

type repository struct {
	q *queries.Queries
}

func (r repository) List(ctx context.Context, f Filter) ([]Location, error) {
	params := queries.ListLocationsParams{IsActive: f.IsActive, RowLimit: int32(f.Limit)} //nolint:gosec // limit is at most 101
	if f.After != uuid.Nil {
		params.After = &f.After
	}
	rows, err := r.q.ListLocations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	locations := make([]Location, len(rows))
	for i, row := range rows {
		locations[i] = fromRow(queries.GetLocationRow(row))
	}
	return locations, nil
}

func (r repository) Get(ctx context.Context, id uuid.UUID) (Location, error) {
	row, err := r.q.GetLocation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Location{}, ErrNotFound
	}
	if err != nil {
		return Location{}, fmt.Errorf("read location: %w", err)
	}
	return fromRow(row), nil
}

func (r repository) CompanyPrefix(ctx context.Context, tenantID uuid.UUID) (string, error) {
	gcp, err := r.q.GetCompanyPrefix(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("read company prefix: %w", err)
	}
	return gcp, nil
}

func (r repository) Create(ctx context.Context, tenantID uuid.UUID, nl NewLocation) (Location, error) {
	row, err := r.q.CreateLocation(ctx, queries.CreateLocationParams{
		TenantID: tenantID, Gln: nl.GLN, Name: nl.Name, Address: nl.Address, City: nl.City,
		CountryCode: nl.CountryCode, Latitude: nl.Latitude, Longitude: nl.Longitude,
		GeoFenceRadiusMeters: int32(nl.GeoFenceRadiusMeters), //nolint:gosec // validated to 50–5000
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "locations_gln_key" {
		return Location{}, ErrGLNTaken
	}
	if err != nil {
		return Location{}, fmt.Errorf("create location: %w", err)
	}
	return fromRow(queries.GetLocationRow(row)), nil
}

func (r repository) Update(ctx context.Context, id uuid.UUID, p Patch) (Location, error) {
	params := queries.UpdateLocationParams{
		ID: id, Name: p.Name, Address: p.Address, City: p.City, CountryCode: p.CountryCode,
		Latitude: p.Latitude, Longitude: p.Longitude, IsActive: p.IsActive,
	}
	if p.GeoFenceRadiusMeters != nil {
		radius := int32(*p.GeoFenceRadiusMeters) //nolint:gosec // validated to 50–5000
		params.GeoFenceRadiusMeters = &radius
	}
	row, err := r.q.UpdateLocation(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return Location{}, ErrNotFound
	}
	if err != nil {
		return Location{}, fmt.Errorf("update location: %w", err)
	}
	return fromRow(queries.GetLocationRow(row)), nil
}

func fromRow(row queries.GetLocationRow) Location {
	return Location{
		ID: row.ID, GLN: row.Gln, Name: row.Name, Address: row.Address, City: row.City, CountryCode: row.CountryCode,
		Latitude: row.Latitude, Longitude: row.Longitude, GeoFenceRadiusMeters: int(row.GeoFenceRadiusMeters),
		IsHeadquarters: row.IsHeadquarters, IsActive: row.IsActive, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
