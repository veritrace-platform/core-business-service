package inventory

import (
	"context"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads balances inside one tenant transaction.
type Repository interface {
	List(ctx context.Context, f Filter) ([]Balance, error)
}

// Service reads the inventory of a tenant.
type Service struct {
	store Store
}

// NewService returns a Service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List returns a page of the caller's balances above zero.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]Balance, error) {
	err := policy.Evaluate(policy.Request{
		Action: policy.ViewInventory, Role: p.Role, Parties: []policy.Party{policy.OwnTenant},
	})
	if err != nil {
		return nil, err
	}
	var balances []Balance
	err = s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		balances, err = repo.List(ctx, f)
		return err
	})
	return balances, err
}
