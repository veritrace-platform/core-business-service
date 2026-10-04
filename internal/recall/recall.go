// Package recall recalls a lot across tenants (shipment-lifecycle.md §8). The lot owner's admin gives a reason;
// in one transaction the lot and every shipment of it that is not cancelled become RECALLED, each shipment's log
// records shipment.recalled, and every later command on those shipments is refused. Stock stays where it is,
// quarantined by the lot's status.
package recall

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/tracecontext"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/recall/queries"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// Recall is the record of a recalled lot.
type Recall struct {
	ID                    uuid.UUID `json:"id"`
	LotID                 uuid.UUID `json:"lot_id"`
	Reason                string    `json:"reason"`
	AffectedShipmentCount int       `json:"affected_shipment_count"`
	InitiatedBy           uuid.UUID `json:"initiated_by"`
	CreatedAt             time.Time `json:"created_at"`
}

// ErrNotFound reports a lot that does not exist or that the tenant neither owns nor holds.
var ErrNotFound = errors.New("lot not found")

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository recalls lots inside one tenant transaction.
type Repository interface {
	// Lot returns the owner and status of a lot that the tenant owns or holds.
	Lot(ctx context.Context, id uuid.UUID) (owner uuid.UUID, status policy.LotStatus, err error)
	// Recall runs core.recall_lot for one of the tenant's users. It returns a *policy.DenialError when another
	// recall of the lot committed first.
	Recall(ctx context.Context, lotID uuid.UUID, reason string, userID uuid.UUID, traceparent *string) (uuid.UUID, int, time.Time, error)
}

// Service recalls lots.
type Service struct {
	store Store
}

// NewService returns a Service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Recall recalls a lot of the caller's tenant.
func (s *Service) Recall(ctx context.Context, p identity.Principal, lotID uuid.UUID, reason string) (Recall, error) {
	var recall Recall
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		owner, status, err := repo.Lot(ctx, lotID)
		if err != nil {
			return err
		}
		party := policy.LotHolder
		if owner == p.TenantID {
			party = policy.LotOwner
		}
		err = policy.Evaluate(policy.Request{Action: policy.RecallLot, Role: p.Role, Parties: []policy.Party{party}, LotStatus: status})
		if err != nil {
			return err
		}
		var traceparent *string
		if tc, ok := tracecontext.FromContext(ctx); ok {
			child := tc.Child().Traceparent()
			traceparent = &child
		}
		id, affected, at, err := repo.Recall(ctx, lotID, reason, p.UserID, traceparent)
		if err != nil {
			return err
		}
		recall = Recall{ID: id, LotID: lotID, Reason: reason, AffectedShipmentCount: affected, InitiatedBy: p.UserID, CreatedAt: at}
		return nil
	})
	return recall, err
}

// PostgresStore recalls lots in PostgreSQL.
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

func (r repository) Lot(ctx context.Context, id uuid.UUID) (uuid.UUID, policy.LotStatus, error) {
	row, err := r.q.GetLot(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrNotFound
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("read lot: %w", err)
	}
	return row.TenantID, policy.LotStatus(row.Status), nil
}

// alreadyRecalled is the SQLSTATE that core.recall_lot raises for a lot that is no longer ACTIVE.
const alreadyRecalled = "55000"

func (r repository) Recall(ctx context.Context, lotID uuid.UUID, reason string, userID uuid.UUID, traceparent *string) (uuid.UUID, int, time.Time, error) {
	row, err := r.q.RecallLot(ctx, queries.RecallLotParams{LotID: lotID, Reason: reason, UserID: userID, Traceparent: traceparent})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == alreadyRecalled {
		return uuid.Nil, 0, time.Time{}, &policy.DenialError{Action: policy.RecallLot, Reason: policy.ReasonInvalidState}
	}
	if err != nil {
		return uuid.Nil, 0, time.Time{}, fmt.Errorf("recall lot: %w", err)
	}
	if row.RecallID == nil || row.AffectedShipmentCount == nil || row.RecalledAt == nil {
		return uuid.Nil, 0, time.Time{}, errors.New("core.recall_lot returned an incomplete result")
	}
	return *row.RecallID, int(*row.AffectedShipmentCount), *row.RecalledAt, nil
}
