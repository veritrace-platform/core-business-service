package tenant

import (
	"context"
	"fmt"
)

// Registrar stores a registration before any tenant context exists.
type Registrar interface {
	RegisterTenant(ctx context.Context, r Registration, passwordHash string) (Registered, error)
}

// PasswordHasher hashes a new password.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

// Service carries out the tenant use cases.
type Service struct {
	registrar Registrar
	hasher    PasswordHasher
}

// NewService returns a Service.
func NewService(registrar Registrar, hasher PasswordHasher) *Service {
	return &Service{registrar: registrar, hasher: hasher}
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
