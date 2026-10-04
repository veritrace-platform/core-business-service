package lot

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/lot/queries"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// PostgresStore keeps lots in PostgreSQL.
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
		return fn(repository{tx: tx, q: queries.New(tx)})
	})
}

type repository struct {
	tx pgx.Tx
	q  *queries.Queries
}

func (r repository) List(ctx context.Context, f Filter) ([]Lot, error) {
	params := queries.ListLotsParams{ProductID: f.ProductID, RowLimit: int32(f.Limit)} //nolint:gosec // limit is at most 101
	if f.Status != nil {
		status := string(*f.Status)
		params.Status = &status
	}
	if f.After != uuid.Nil {
		params.After = &f.After
	}
	rows, err := r.q.ListLots(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list lots: %w", err)
	}
	lots := make([]Lot, len(rows))
	for i, row := range rows {
		lots[i] = fromRow(queries.GetLotRow(row))
	}
	return lots, nil
}

func (r repository) Get(ctx context.Context, id uuid.UUID) (Lot, error) {
	row, err := r.q.GetLot(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, ErrNotFound
	}
	if err != nil {
		return Lot{}, fmt.Errorf("read lot: %w", err)
	}
	return fromRow(row), nil
}

func (r repository) LockProduct(ctx context.Context, id uuid.UUID) (Product, error) {
	row, err := r.q.LockProduct(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotOwned
	}
	if err != nil {
		return Product{}, fmt.Errorf("lock product: %w", err)
	}
	return Product{
		ID: row.ID, GTIN: row.Gtin, Name: row.Name, MinTempCelsius: row.MinTempCelsius,
		MaxTempCelsius: row.MaxTempCelsius, IsActive: row.IsActive,
	}, nil
}

func (r repository) LockLocation(ctx context.Context, id uuid.UUID) (bool, error) {
	row, err := r.q.LockLocation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotOwned
	}
	if err != nil {
		return false, fmt.Errorf("lock location: %w", err)
	}
	return row.IsActive, nil
}

func (r repository) Create(ctx context.Context, tenantID, createdBy uuid.UUID, nl NewLot, product Product) (Lot, error) {
	row, err := r.q.CreateLot(ctx, queries.CreateLotParams{
		TenantID: tenantID, ProductID: product.ID, Gtin: product.GTIN, ProductName: product.Name,
		MinTempCelsius: product.MinTempCelsius, MaxTempCelsius: product.MaxTempCelsius, LotNumber: nl.LotNumber,
		ProductionDate: nl.ProductionDate, ExpirationDate: nl.ExpirationDate,
		QuantityCommissioned:   int32(nl.Quantity), //nolint:gosec // validated to fit an integer column
		CommissionedLocationID: nl.LocationID, CreatedBy: createdBy,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "lots_product_id_lot_number_key" {
		return Lot{}, ErrLotNumberTaken
	}
	if err != nil {
		return Lot{}, fmt.Errorf("create lot: %w", err)
	}
	return fromRow(queries.GetLotRow(row)), nil
}

func (r repository) ApplyMovement(ctx context.Context, m inventory.Movement) error {
	_, err := inventory.ApplyMovement(ctx, r.tx, m)
	return err
}

func fromRow(row queries.GetLotRow) Lot {
	return Lot{
		ID: row.ID, OwnerTenantID: row.TenantID, ProductID: row.ProductID, GTIN: row.Gtin, ProductName: row.ProductName,
		MinTempCelsius: row.MinTempCelsius, MaxTempCelsius: row.MaxTempCelsius, LotNumber: row.LotNumber,
		ProductionDate: row.ProductionDate, ExpirationDate: row.ExpirationDate,
		QuantityCommissioned: int(row.QuantityCommissioned), CommissionedLocationID: row.CommissionedLocationID,
		Status: policy.LotStatus(row.Status), RecalledAt: row.RecalledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
