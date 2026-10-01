package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads and changes users inside one tenant transaction.
type Repository interface {
	List(ctx context.Context, f Filter) ([]User, error)
	// Get returns ErrNotFound for a user that is missing or belongs to another tenant.
	Get(ctx context.Context, id uuid.UUID) (User, error)
	// Create returns ErrEmailTaken for an email address in use.
	Create(ctx context.Context, tenantID uuid.UUID, u NewUser, passwordHash string) (User, error)
	Update(ctx context.Context, id uuid.UUID, p Patch) (User, error)
	// LockTenant serializes changes to the tenant's users until the transaction ends.
	LockTenant(ctx context.Context, tenantID uuid.UUID) error
	RevokeSessions(ctx context.Context, userID uuid.UUID, at time.Time) error
}

// PasswordHasher hashes a new password.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

// Service manages the users of a tenant.
type Service struct {
	store  Store
	hasher PasswordHasher
	now    func() time.Time
}

// NewService returns a Service.
func NewService(store Store, hasher PasswordHasher, now func() time.Time) *Service {
	return &Service{store: store, hasher: hasher, now: now}
}

// ownTenant authorizes an action on the caller's own tenant.
func ownTenant(p identity.Principal, action policy.Action) error {
	return policy.Evaluate(policy.Request{Action: action, Role: p.Role, Parties: []policy.Party{policy.OwnTenant}})
}

// List returns a page of users. Admins see every user; warehouse managers may list drivers only, to assign
// them to shipments.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]User, error) {
	action := policy.ManageUsers
	if p.Role != identity.RoleAdmin && f.Role != nil && *f.Role == identity.RoleDriver {
		action = policy.ListDrivers
	}
	if err := ownTenant(p, action); err != nil {
		return nil, err
	}
	var users []User
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		users, err = repo.List(ctx, f)
		return err
	})
	return users, err
}

// Get returns one user of the caller's tenant.
func (s *Service) Get(ctx context.Context, p identity.Principal, id uuid.UUID) (User, error) {
	if err := ownTenant(p, policy.ManageUsers); err != nil {
		return User{}, err
	}
	var u User
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		u, err = repo.Get(ctx, id)
		return err
	})
	return u, err
}

// Create adds a user to the caller's tenant.
func (s *Service) Create(ctx context.Context, p identity.Principal, nu NewUser) (User, error) {
	if err := ownTenant(p, policy.ManageUsers); err != nil {
		return User{}, err
	}
	hash, err := s.hasher.Hash(ctx, nu.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	var created User
	err = s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		if err := lockAsActiveAdmin(ctx, repo, p); err != nil {
			return err
		}
		created, err = repo.Create(ctx, p.TenantID, nu, hash)
		return err
	})
	return created, err
}

// Update changes a user. Admins cannot change their own role or deactivate themselves, and deactivating a user
// revokes its sessions, so it is signed out at its next refresh.
func (s *Service) Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (User, error) {
	if err := ownTenant(p, policy.ManageUsers); err != nil {
		return User{}, err
	}
	var updated User
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		if err := lockAsActiveAdmin(ctx, repo, p); err != nil {
			return err
		}
		current, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if id == p.UserID &&
			((patch.Role != nil && *patch.Role != current.Role) || (patch.IsActive != nil && !*patch.IsActive)) {
			return ErrSelfChange
		}
		if updated, err = repo.Update(ctx, id, patch); err != nil {
			return err
		}
		if current.IsActive && !updated.IsActive {
			return repo.RevokeSessions(ctx, id, s.now().UTC())
		}
		return nil
	})
	return updated, err
}

// lockAsActiveAdmin serializes changes to the tenant's users, then checks that the caller is still an active
// admin. An access token keeps its role for up to 15 minutes after the role changes, and without the lock two
// admins could demote each other, or a demoted admin create another admin, at the same moment.
func lockAsActiveAdmin(ctx context.Context, repo Repository, p identity.Principal) error {
	if err := repo.LockTenant(ctx, p.TenantID); err != nil {
		return err
	}
	actor, err := repo.Get(ctx, p.UserID)
	if errors.Is(err, ErrNotFound) {
		return ErrActorNotAdmin
	}
	if err != nil {
		return err
	}
	if !actor.IsActive || actor.Role != identity.RoleAdmin {
		return ErrActorNotAdmin
	}
	return nil
}
