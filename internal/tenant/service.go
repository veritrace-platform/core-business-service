package tenant

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Registrar stores a registration before any tenant context exists.
type Registrar interface {
	RegisterTenant(ctx context.Context, r Registration, passwordHash string) (Registered, error)
}

// Profiles reads and changes the profile of the tenant of a transaction.
type Profiles interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(ProfileRepository) error) error
}

// ProfileRepository works on the tenant's own row inside one tenant transaction.
type ProfileRepository interface {
	// Get returns ErrNotFound when the tenant is not visible.
	Get(ctx context.Context, id uuid.UUID) (Tenant, error)
	Update(ctx context.Context, id uuid.UUID, p ProfilePatch) (Tenant, error)
}

// PasswordHasher hashes a new password.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

// Service carries out the tenant use cases.
type Service struct {
	registrar Registrar
	profiles  Profiles
	hasher    PasswordHasher
}

// NewService returns a Service.
func NewService(registrar Registrar, profiles Profiles, hasher PasswordHasher) *Service {
	return &Service{registrar: registrar, profiles: profiles, hasher: hasher}
}

// Register creates a tenant with its headquarters and first admin. It returns a *ConflictError when a unique
// key is taken.
func (s *Service) Register(ctx context.Context, r Registration) (Registered, error) {
	hash, err := s.hasher.Hash(ctx, r.Admin.Password)
	if err != nil {
		return Registered{}, fmt.Errorf("hash admin password: %w", err)
	}
	registered, err := s.registrar.RegisterTenant(ctx, r, hash)
	if err != nil {
		return Registered{}, fmt.Errorf("register tenant: %w", err)
	}
	return registered, nil
}

// Profile returns the caller's tenant.
func (s *Service) Profile(ctx context.Context, p identity.Principal) (Tenant, error) {
	if err := authorizeProfile(p); err != nil {
		return Tenant{}, err
	}
	var t Tenant
	err := s.profiles.WithTenantTx(ctx, p.TenantID, func(repo ProfileRepository) error {
		var err error
		t, err = repo.Get(ctx, p.TenantID)
		return err
	})
	return t, err
}

// UpdateProfile changes the caller's tenant.
func (s *Service) UpdateProfile(ctx context.Context, p identity.Principal, patch ProfilePatch) (Tenant, error) {
	if err := authorizeProfile(p); err != nil {
		return Tenant{}, err
	}
	var t Tenant
	err := s.profiles.WithTenantTx(ctx, p.TenantID, func(repo ProfileRepository) error {
		var err error
		t, err = repo.Update(ctx, p.TenantID, patch)
		return err
	})
	return t, err
}

func authorizeProfile(p identity.Principal) error {
	return policy.Evaluate(policy.Request{
		Action: policy.ManageTenantProfile, Role: p.Role, Parties: []policy.Party{policy.OwnTenant},
	})
}
