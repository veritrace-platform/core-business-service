package product

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/product/queries"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// PostgresStore keeps products in PostgreSQL.
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

// likeEscaper makes search text match literally in a LIKE pattern, whose escape character is the backslash.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (r repository) List(ctx context.Context, f Filter) ([]Product, error) {
	params := queries.ListProductsParams{IsActive: f.IsActive, RowLimit: int32(f.Limit)} //nolint:gosec // limit is at most 101
	if f.Search != "" {
		pattern := "%" + likeEscaper.Replace(f.Search) + "%"
		params.Pattern = &pattern
	}
	if f.After != uuid.Nil {
		params.After = &f.After
	}
	rows, err := r.q.ListProducts(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	products := make([]Product, len(rows))
	for i, row := range rows {
		products[i] = fromRow(queries.GetProductRow(row))
	}
	return products, nil
}

func (r repository) Get(ctx context.Context, id uuid.UUID) (Product, error) {
	row, err := r.q.GetProduct(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("read product: %w", err)
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

func (r repository) Create(ctx context.Context, tenantID uuid.UUID, np NewProduct) (Product, error) {
	row, err := r.q.CreateProduct(ctx, queries.CreateProductParams{
		TenantID: tenantID, Gtin: np.GTIN, Name: np.Name, Description: np.Description,
		MinTempCelsius: np.MinTempCelsius, MaxTempCelsius: np.MaxTempCelsius,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "products_gtin_key" {
		return Product{}, ErrGTINTaken
	}
	if err != nil {
		return Product{}, fmt.Errorf("create product: %w", err)
	}
	return fromRow(queries.GetProductRow(row)), nil
}

func (r repository) Update(ctx context.Context, id uuid.UUID, p Patch) (Product, error) {
	row, err := r.q.UpdateProduct(ctx, queries.UpdateProductParams{
		ID: id, Name: p.Name, SetDescription: p.ClearDescription || p.Description != nil, Description: p.Description,
		MinTempCelsius: p.MinTempCelsius, MaxTempCelsius: p.MaxTempCelsius, IsActive: p.IsActive,
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Product{}, ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "products_temperature_range_check":
		return Product{}, ErrTemperatureRange
	case err != nil:
		return Product{}, fmt.Errorf("update product: %w", err)
	}
	return fromRow(queries.GetProductRow(row)), nil
}

func fromRow(row queries.GetProductRow) Product {
	return Product{
		ID: row.ID, GTIN: row.Gtin, Name: row.Name, Description: row.Description,
		MinTempCelsius: row.MinTempCelsius, MaxTempCelsius: row.MaxTempCelsius, IsActive: row.IsActive,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
