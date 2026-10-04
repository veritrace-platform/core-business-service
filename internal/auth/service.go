package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// Store reads what authentication needs before a tenant context exists, and opens tenant transactions.
type Store interface {
	// FindLoginUser returns ErrNotFound for an unknown email.
	FindLoginUser(ctx context.Context, email string) (LoginUser, error)
	// FindSession returns ErrNotFound for an unknown token hash.
	FindSession(ctx context.Context, tokenHash []byte) (SessionRef, error)
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads and changes sessions and accounts inside one tenant transaction.
type Repository interface {
	CreateSession(ctx context.Context, s NewSession) (uuid.UUID, error)
	// LockSession returns ErrNotFound for a missing session.
	LockSession(ctx context.Context, id uuid.UUID) (StoredSession, error)
	// SessionFamily returns ErrNotFound for a missing session.
	SessionFamily(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error)
	MarkRotated(ctx context.Context, id uuid.UUID, at time.Time) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error
	RevokeOtherSessions(ctx context.Context, userID, keepFamilyID uuid.UUID, at time.Time) error
	DeleteExpiredFamily(ctx context.Context, familyID uuid.UUID, before time.Time) error
	DeleteExpiredForUser(ctx context.Context, userID uuid.UUID, before time.Time) error
	RecordLogin(ctx context.Context, userID uuid.UUID, at time.Time) error
	// PasswordHash returns ErrNotFound for a missing user.
	PasswordHash(ctx context.Context, userID uuid.UUID) (string, error)
	SetPasswordHash(ctx context.Context, userID uuid.UUID, hash string) error
	// Me returns ErrNotFound for a missing user.
	Me(ctx context.Context, userID uuid.UUID) (user.Me, error)
}

// Hasher hashes and verifies passwords.
type Hasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (match, needsRehash bool, err error)
	VerifyDummy(ctx context.Context, password string) error
}

// Service signs users in and out and keeps their sessions.
type Service struct {
	store      Store
	hasher     Hasher
	tokens     *Tokens
	refreshTTL time.Duration
	now        func() time.Time
}

// NewService returns a Service. refreshTTL is the lifetime of each refresh token.
func NewService(store Store, hasher Hasher, tokens *Tokens, refreshTTL time.Duration, now func() time.Time) *Service {
	return &Service{store: store, hasher: hasher, tokens: tokens, refreshTTL: refreshTTL, now: now}
}

// Login checks the email and password and starts a session family. It returns ErrInvalidCredentials, with
// the same timing, for an unknown email, a wrong password, and an account that may not sign in.
func (s *Service) Login(ctx context.Context, email, password string, userAgent *string) (Session, error) {
	account, err := s.store.FindLoginUser(ctx, email)
	if errors.Is(err, ErrNotFound) {
		if err := s.hasher.VerifyDummy(ctx, password); err != nil {
			return Session{}, fmt.Errorf("verify password: %w", err)
		}
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	match, stale, err := s.hasher.Verify(ctx, password, account.PasswordHash)
	if err != nil {
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if !match || !account.Active {
		return Session{}, ErrInvalidCredentials
	}
	var rehash string
	if stale {
		if rehash, err = s.hasher.Hash(ctx, password); err != nil {
			return Session{}, fmt.Errorf("rehash password: %w", err)
		}
	}

	now := s.now().UTC()
	familyID, err := uuid.NewV7()
	if err != nil {
		return Session{}, fmt.Errorf("new session family: %w", err)
	}
	refreshToken, tokenHash := newRefreshToken()
	var sessionID uuid.UUID
	var me user.Me
	err = s.store.WithTenantTx(ctx, account.TenantID, func(repo Repository) error {
		if rehash != "" {
			if err := repo.SetPasswordHash(ctx, account.UserID, rehash); err != nil {
				return err
			}
		}
		if err := repo.DeleteExpiredForUser(ctx, account.UserID, now); err != nil {
			return err
		}
		if sessionID, err = repo.CreateSession(ctx, NewSession{
			TenantID: account.TenantID, UserID: account.UserID, FamilyID: familyID,
			TokenHash: tokenHash, ExpiresAt: now.Add(s.refreshTTL), UserAgent: userAgent,
		}); err != nil {
			return err
		}
		if err := repo.RecordLogin(ctx, account.UserID, now); err != nil {
			return err
		}
		me, err = repo.Me(ctx, account.UserID)
		return err
	})
	if err != nil {
		return Session{}, fmt.Errorf("start session: %w", err)
	}
	return s.session(me, sessionID, refreshToken)
}

// Refresh rotates a refresh token: the presented token stops working and a new one replaces it. Presenting a
// rotated token again revokes its whole family and returns ErrRefreshTokenReused. Unknown, expired, and revoked
// tokens, and tokens of accounts that may no longer sign in, return ErrRefreshTokenInvalid.
func (s *Service) Refresh(ctx context.Context, presented string, userAgent *string) (Session, error) {
	tokenHash, ok := hashRefreshToken(presented)
	if !ok {
		return Session{}, ErrRefreshTokenInvalid
	}
	ref, err := s.store.FindSession(ctx, tokenHash)
	if errors.Is(err, ErrNotFound) {
		return Session{}, ErrRefreshTokenInvalid
	}
	if err != nil {
		return Session{}, err
	}

	now := s.now().UTC()
	refreshToken, newHash := newRefreshToken()
	var sessionID uuid.UUID
	var me user.Me
	// The outcome is decided inside the transaction, which must commit even when the refresh fails, so that a
	// revoked family stays revoked.
	var outcome error
	err = s.store.WithTenantTx(ctx, ref.TenantID, func(repo Repository) error {
		current, err := repo.LockSession(ctx, ref.SessionID)
		if errors.Is(err, ErrNotFound) {
			outcome = ErrRefreshTokenInvalid
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case current.RevokedAt != nil:
			outcome = ErrRefreshTokenInvalid
			return nil
		case current.RotatedAt != nil:
			outcome = ErrRefreshTokenReused
			return repo.RevokeFamily(ctx, current.FamilyID, now)
		case !now.Before(current.ExpiresAt):
			outcome = ErrRefreshTokenInvalid
			return nil
		}

		if me, err = repo.Me(ctx, current.UserID); err != nil {
			return err
		}
		if !me.IsActive || me.Tenant.Status != string(tenant.StatusActive) {
			outcome = ErrRefreshTokenInvalid
			return repo.RevokeFamily(ctx, current.FamilyID, now)
		}
		if err := repo.MarkRotated(ctx, current.ID, now); err != nil {
			return err
		}
		if err := repo.DeleteExpiredFamily(ctx, current.FamilyID, now); err != nil {
			return err
		}
		sessionID, err = repo.CreateSession(ctx, NewSession{
			TenantID: ref.TenantID, UserID: current.UserID, FamilyID: current.FamilyID,
			TokenHash: newHash, ExpiresAt: now.Add(s.refreshTTL), UserAgent: userAgent,
		})
		return err
	})
	if err != nil {
		return Session{}, fmt.Errorf("rotate session: %w", err)
	}
	if outcome != nil {
		return Session{}, outcome
	}
	return s.session(me, sessionID, refreshToken)
}

// Logout revokes the family of a refresh token. Unknown and malformed tokens are ignored, so logging out twice
// succeeds.
func (s *Service) Logout(ctx context.Context, presented string) error {
	tokenHash, ok := hashRefreshToken(presented)
	if !ok {
		return nil
	}
	ref, err := s.store.FindSession(ctx, tokenHash)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	err = s.store.WithTenantTx(ctx, ref.TenantID, func(repo Repository) error {
		return repo.RevokeFamily(ctx, ref.FamilyID, now)
	})
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// Me returns the signed-in user. ErrNotFound means the account no longer exists.
func (s *Service) Me(ctx context.Context, p identity.Principal) (user.Me, error) {
	var me user.Me
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		me, err = repo.Me(ctx, p.UserID)
		return err
	})
	return me, err
}

// ChangePassword replaces the signed-in user's password after checking the current one. Every other session
// of the user is revoked; the session that made the request stays signed in.
func (s *Service) ChangePassword(ctx context.Context, p identity.Principal, current, replacement string) error {
	var stored string
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		stored, err = repo.PasswordHash(ctx, p.UserID)
		return err
	})
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	err = policy.Evaluate(policy.Request{
		Action: policy.ChangeOwnPassword, Role: p.Role, Parties: []policy.Party{policy.Self},
		Facts: func(policy.Check) (bool, error) {
			// The only check of this action is the current password.
			match, _, err := s.hasher.Verify(ctx, current, stored)
			return match, err
		},
	})
	var denial *policy.DenialError
	switch {
	case errors.As(err, &denial) && denial.Check == policy.CurrentPassword:
		return ErrIncorrectPassword
	case err != nil:
		return fmt.Errorf("authorize password change: %w", err)
	}
	hash, err := s.hasher.Hash(ctx, replacement)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := s.now().UTC()
	err = s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		if err := repo.SetPasswordHash(ctx, p.UserID, hash); err != nil {
			return err
		}
		// The access token's jti is the session that issued it; its family is the caller's own session.
		keep, err := repo.SessionFamily(ctx, p.SessionID)
		if errors.Is(err, ErrNotFound) {
			keep = uuid.Nil
		} else if err != nil {
			return err
		}
		return repo.RevokeOtherSessions(ctx, p.UserID, keep, now)
	})
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return nil
}

func (s *Service) session(me user.Me, sessionID uuid.UUID, refreshToken string) (Session, error) {
	accessToken, _, err := s.tokens.Issue(identity.Principal{
		UserID: me.ID, TenantID: me.Tenant.ID, Role: me.Role, SessionID: sessionID,
	})
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken:     accessToken,
		AccessTokenTTL:  AccessTokenTTL,
		RefreshToken:    refreshToken,
		RefreshTokenTTL: s.refreshTTL,
		User:            me,
	}, nil
}
