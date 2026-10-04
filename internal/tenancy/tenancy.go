// Package tenancy scopes database work to one tenant. DB.WithTenantTx is the only way to run tenant-scoped
// statements: it sets the tenant context that row-level security policies read, so every statement in the
// transaction sees and writes only that tenant's rows (ADR-0002, ADR-0004).
package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNoTenant is returned when a tenant-scoped transaction is requested without a tenant ID.
var ErrNoTenant = errors.New("tenant ID is required")

// setTenantSQL scopes the setting to the transaction (is_local = true), so it ends with the transaction and
// never carries over to the next user of a pooled connection.
const setTenantSQL = `SELECT set_config('app.current_tenant_id', $1, true)`

// TxBeginner starts database transactions. *pgxpool.Pool implements it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// DB runs tenant-scoped transactions on connections of the runtime role.
type DB struct {
	pool TxBeginner
}

// NewDB returns a DB that starts its transactions on pool.
func NewDB(pool TxBeginner) *DB {
	return &DB{pool: pool}
}

// WithTenantTx runs fn in a transaction scoped to tenantID. The transaction commits when fn returns nil, and
// rolls back when fn returns an error or panics. Errors from fn are returned unchanged, so callers can match
// their domain errors. Repositories receive tx from fn and never begin transactions themselves.
func (db *DB) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error {
	if tenantID == uuid.Nil {
		return ErrNoTenant
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant transaction: %w", err)
	}
	// Rollback is a no-op after a successful commit; it also runs when fn panics.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, setTenantSQL, tenantID.String()); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	return nil
}
