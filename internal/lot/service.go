package lot

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads and commissions lots inside one tenant transaction.
type Repository interface {
	// List returns lots that the tenant owns or holds.
	List(ctx context.Context, f Filter) ([]Lot, error)
	// Get returns ErrNotFound for a lot that is missing or that the tenant neither owns nor holds.
	Get(ctx context.Context, id uuid.UUID) (Lot, error)
	// LockProduct locks one of the tenant's products until the transaction ends. It returns ErrNotOwned for a
	// product that is missing or belongs to another tenant.
	LockProduct(ctx context.Context, id uuid.UUID) (Product, error)
	// LockLocation locks one of the tenant's locations until the transaction ends and reports whether it is
	// active. It returns ErrNotOwned for a location that is missing or belongs to another tenant.
	LockLocation(ctx context.Context, id uuid.UUID) (active bool, err error)
	// Create returns ErrLotNumberTaken when the product already has the lot number.
	Create(ctx context.Context, tenantID, createdBy uuid.UUID, nl NewLot, product Product) (Lot, error)
	ApplyMovement(ctx context.Context, m inventory.Movement) error
}

// Service commissions and reads lots.
type Service struct {
	store Store
}

// NewService returns a Service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// authorizeRead checks that the caller may view lots. Row-level security shows a tenant only the lots it owns
// or holds, so the caller is the owner or a holder of every lot it reads.
func authorizeRead(p identity.Principal) error {
	return policy.Evaluate(policy.Request{
		Action: policy.ViewLots, Role: p.Role, Parties: []policy.Party{policy.LotOwner, policy.LotHolder},
	})
}

// List returns a page of the lots that the caller's tenant owns or holds.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]Lot, error) {
	if err := authorizeRead(p); err != nil {
		return nil, err
	}
	var lots []Lot
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		lots, err = repo.List(ctx, f)
		return err
	})
	return lots, err
}

// Get returns a lot that the caller's tenant owns or holds.
func (s *Service) Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Lot, error) {
	if err := authorizeRead(p); err != nil {
		return Lot{}, err
	}
	var l Lot
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		l, err = repo.Get(ctx, id)
		return err
	})
	return l, err
}

// Commission creates an ACTIVE lot of one of the caller's active products at one of its active locations, and
// adds the quantity to the location's balance with a COMMISSIONED movement (shipment-lifecycle.md §2). A product
// or location of another tenant fails the policy's ownership checks.
func (s *Service) Commission(ctx context.Context, p identity.Principal, nl NewLot) (Lot, error) {
	var created Lot
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var product Product
		var locationActive bool
		establish := map[policy.Check]func() error{
			policy.ProductOwned: func() (err error) {
				product, err = repo.LockProduct(ctx, nl.ProductID)
				return err
			},
			policy.LocationOwned: func() (err error) {
				locationActive, err = repo.LockLocation(ctx, nl.LocationID)
				return err
			},
		}
		err := policy.Evaluate(policy.Request{
			Action: policy.CommissionLot, Role: p.Role, Parties: []policy.Party{policy.OwnTenant},
			Facts: func(c policy.Check) (bool, error) {
				check, known := establish[c]
				if !known {
					return false, nil
				}
				err := check()
				if errors.Is(err, ErrNotOwned) {
					return false, nil
				}
				return err == nil, err
			},
		})
		switch {
		case err != nil:
			return err
		case !product.IsActive:
			return ErrProductInactive
		case !locationActive:
			return ErrLocationInactive
		}
		if created, err = repo.Create(ctx, p.TenantID, p.UserID, nl, product); err != nil {
			return err
		}
		return repo.ApplyMovement(ctx, inventory.Movement{
			TenantID: p.TenantID, LocationID: nl.LocationID, LotID: created.ID, Delta: nl.Quantity,
			Reason: inventory.ReasonCommissioned, CreatedBy: p.UserID,
		})
	})
	return created, err
}
