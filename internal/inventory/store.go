package inventory

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/inventory/queries"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// PostgresStore reads balances from PostgreSQL.
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

func (r repository) List(ctx context.Context, f Filter) ([]Balance, error) {
	params := queries.ListBalancesParams{
		LocationID: f.LocationID, LotID: f.LotID,
		RowLimit: int32(f.Limit), //nolint:gosec // limit is at most 101
	}
	if f.After != nil {
		params.AfterLocationID, params.AfterLotID = &f.After.LocationID, &f.After.LotID
	}
	rows, err := r.q.ListBalances(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	balances := make([]Balance, len(rows))
	for i, row := range rows {
		balances[i] = Balance{
			Location: LocationRef{ID: row.LocationID, GLN: row.LocationGln, Name: row.LocationName},
			Lot: LotRef{
				ID: row.LotID, LotNumber: row.LotNumber, GTIN: row.Gtin, ProductName: row.ProductName,
				ExpirationDate: row.ExpirationDate, Status: policy.LotStatus(row.LotStatus),
			},
			QuantityOnHand: int(row.QuantityOnHand),
			UpdatedAt:      row.UpdatedAt,
		}
	}
	return balances, nil
}
