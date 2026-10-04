package location

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

// Repository reads and changes locations inside one tenant transaction.
type Repository interface {
	List(ctx context.Context, f Filter) ([]Location, error)
	// Get returns ErrNotFound for a location that is missing or belongs to another tenant.
	Get(ctx context.Context, id uuid.UUID) (Location, error)
	// CompanyPrefix returns the GS1 company prefix of the tenant.
	CompanyPrefix(ctx context.Context, tenantID uuid.UUID) (string, error)
	// Create returns ErrGLNTaken for a GLN in use.
	Create(ctx context.Context, tenantID uuid.UUID, nl NewLocation) (Location, error)
	// Update returns ErrNotFound for a location that is missing or belongs to another tenant.
	Update(ctx context.Context, id uuid.UUID, p Patch) (Location, error)
}

// Service manages the locations of a tenant.
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

// List returns a page of the caller's locations.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]Location, error) {
	if err := authorize(p, policy.ViewCatalog, nil); err != nil {
		return nil, err
	}
	var locations []Location
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		locations, err = repo.List(ctx, f)
		return err
	})
	return locations, err
}

// Get returns one location of the caller's tenant.
func (s *Service) Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Location, error) {
	if err := authorize(p, policy.ViewCatalog, nil); err != nil {
		return Location{}, err
	}
	var l Location
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		l, err = repo.Get(ctx, id)
		return err
	})
	return l, err
}

// Create adds a location to the caller's tenant. A GLN that does not start with the tenant's company prefix
// fails the policy's prefix check and is returned as a *gs1.InvalidKeyError.
func (s *Service) Create(ctx context.Context, p identity.Principal, nl NewLocation) (Location, error) {
	var created Location
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var invalidGLN error
		err := authorize(p, policy.ManageLocations, func(policy.Check) (bool, error) {
			// The only check of this action is the GLN prefix.
			gcp, err := repo.CompanyPrefix(ctx, p.TenantID)
			if err != nil {
				return false, err
			}
			invalidGLN = gs1.ValidateGLN(nl.GLN, gcp)
			return invalidGLN == nil, nil
		})
		if invalidGLN != nil {
			return invalidGLN
		}
		if err != nil {
			return err
		}
		created, err = repo.Create(ctx, p.TenantID, nl)
		return err
	})
	return created, err
}

// Update changes a location of the caller's tenant.
func (s *Service) Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (Location, error) {
	// A patch cannot change the GLN, which passed the prefix check when the location was created.
	if err := authorize(p, policy.ManageLocations, policy.Known(map[policy.Check]bool{policy.GLNPrefix: true})); err != nil {
		return Location{}, err
	}
	var updated Location
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		updated, err = repo.Update(ctx, id, patch)
		return err
	})
	return updated, err
}
