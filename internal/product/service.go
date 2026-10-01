package product

import (
	"context"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads and changes products inside one tenant transaction.
type Repository interface {
	List(ctx context.Context, f Filter) ([]Product, error)
	// Get returns ErrNotFound for a product that is missing or belongs to another tenant.
	Get(ctx context.Context, id uuid.UUID) (Product, error)
	// CompanyPrefix returns the GS1 company prefix of the tenant.
	CompanyPrefix(ctx context.Context, tenantID uuid.UUID) (string, error)
	// Create returns ErrGTINTaken for a GTIN in use.
	Create(ctx context.Context, tenantID uuid.UUID, np NewProduct) (Product, error)
	// Update returns ErrNotFound for a product that is missing or belongs to another tenant, and
	// ErrTemperatureRange when the result would not have its minimum temperature below its maximum.
	Update(ctx context.Context, id uuid.UUID, p Patch) (Product, error)
}

// Service manages the products of a tenant.
type Service struct {
	store Store
}

// NewService returns a Service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// authorize checks an action on the caller's own tenant.
func authorize(p identity.Principal, action policy.Action, facts policy.Facts) error {
	return policy.Evaluate(policy.Request{
		Action: action, Role: p.Role, Parties: []policy.Party{policy.OwnTenant}, Facts: facts,
	})
}

// List returns a page of the caller's products.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]Product, error) {
	if err := authorize(p, policy.ViewCatalog, nil); err != nil {
		return nil, err
	}
	var products []Product
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		products, err = repo.List(ctx, f)
		return err
	})
	return products, err
}

// Get returns one product of the caller's tenant.
func (s *Service) Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Product, error) {
	if err := authorize(p, policy.ViewCatalog, nil); err != nil {
		return Product{}, err
	}
	var product Product
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		product, err = repo.Get(ctx, id)
		return err
	})
	return product, err
}

// Create adds a product to the caller's tenant. A GTIN without the tenant's company prefix after its
// indicator digit fails the policy's prefix check and is returned as a *gs1.InvalidKeyError.
func (s *Service) Create(ctx context.Context, p identity.Principal, np NewProduct) (Product, error) {
	var created Product
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var invalidGTIN error
		err := authorize(p, policy.ManageProducts, func(policy.Check) (bool, error) {
			// The only check of this action is the GTIN prefix.
			gcp, err := repo.CompanyPrefix(ctx, p.TenantID)
			if err != nil {
				return false, err
			}
			invalidGTIN = gs1.ValidateGTIN14(np.GTIN, gcp)
			return invalidGTIN == nil, nil
		})
		if invalidGTIN != nil {
			return invalidGTIN
		}
		if err != nil {
			return err
		}
		created, err = repo.Create(ctx, p.TenantID, np)
		return err
	})
	return created, err
}

// Update changes a product of the caller's tenant. New temperature bounds apply to lots commissioned
// afterwards; existing lots and shipments keep the bounds they were created with.
func (s *Service) Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (Product, error) {
	// A patch cannot change the GTIN, which passed the prefix check when the product was created.
	if err := authorize(p, policy.ManageProducts, policy.Known(map[policy.Check]bool{policy.GTINPrefix: true})); err != nil {
		return Product{}, err
	}
	var updated Product
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		updated, err = repo.Update(ctx, id, patch)
		return err
	})
	return updated, err
}
