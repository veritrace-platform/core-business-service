package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/inventory/queries"
)

// ApplyMovement changes a balance by m.Delta and records m in the ledger, through tx, a transaction of
// m.TenantID. It returns the new balance. A movement that would take the balance below zero fails with
// ErrInsufficientStock; as with any failed statement, the transaction must then end.
func ApplyMovement(ctx context.Context, tx queries.DBTX, m Movement) (int, error) {
	q := queries.New(tx)
	balance, err := q.AddToBalance(ctx, queries.AddToBalanceParams{
		TenantID: m.TenantID, LocationID: m.LocationID, LotID: m.LotID,
		QuantityDelta: int32(m.Delta), //nolint:gosec // quantities are validated to fit an integer column
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "inventory_balances_quantity_on_hand_check" {
		return 0, ErrInsufficientStock
	}
	if err != nil {
		return 0, fmt.Errorf("apply to balance: %w", err)
	}
	err = q.RecordMovement(ctx, queries.RecordMovementParams{
		TenantID: m.TenantID, LocationID: m.LocationID, LotID: m.LotID,
		QuantityDelta: int32(m.Delta), //nolint:gosec // as above
		Reason:        string(m.Reason), ShipmentID: m.ShipmentID, CreatedBy: m.CreatedBy,
	})
	if err != nil {
		return 0, fmt.Errorf("record movement: %w", err)
	}
	return int(balance), nil
}
