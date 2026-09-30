package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/auth/queries"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// PostgresStore keeps sessions in PostgreSQL.
type PostgresStore struct {
	pool queries.DBTX
	db   *tenancy.DB
}

// NewPostgresStore returns a store that looks up accounts and sessions through pool, a pool of the runtime
// role, and runs tenant-scoped work through db.
func NewPostgresStore(pool queries.DBTX, db *tenancy.DB) *PostgresStore {
	return &PostgresStore{pool: pool, db: db}
}

// FindLoginUser calls core.find_login_user.
func (s *PostgresStore) FindLoginUser(ctx context.Context, email string) (LoginUser, error) {
	row, err := queries.New(s.pool).FindLoginUser(ctx, email)
	if err != nil {
		return LoginUser{}, fmt.Errorf("find login user: %w", err)
	}
	if row.UserID == nil || row.TenantID == nil || row.PasswordHash == nil || row.Role == nil || row.IsActive == nil {
		return LoginUser{}, ErrNotFound
	}
	return LoginUser{
		UserID:       *row.UserID,
		TenantID:     *row.TenantID,
		PasswordHash: *row.PasswordHash,
		Role:         identity.Role(*row.Role),
		Active:       *row.IsActive,
	}, nil
}

// FindSession calls core.find_auth_session.
func (s *PostgresStore) FindSession(ctx context.Context, tokenHash []byte) (SessionRef, error) {
	row, err := queries.New(s.pool).FindAuthSession(ctx, tokenHash)
	if err != nil {
		return SessionRef{}, fmt.Errorf("find session: %w", err)
	}
	if row.SessionID == nil || row.FamilyID == nil || row.TenantID == nil || row.UserID == nil {
		return SessionRef{}, ErrNotFound
	}
	return SessionRef{SessionID: *row.SessionID, FamilyID: *row.FamilyID, TenantID: *row.TenantID, UserID: *row.UserID}, nil
}

// WithTenantTx runs fn with a repository bound to one transaction of tenantID.
func (s *PostgresStore) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error {
	return s.db.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return fn(repository{q: queries.New(tx)})
	})
}

// repository runs the session and account queries of one tenant transaction.
type repository struct {
	q *queries.Queries
}

func (r repository) CreateSession(ctx context.Context, s NewSession) (uuid.UUID, error) {
	id, err := r.q.CreateSession(ctx, queries.CreateSessionParams{
		TenantID:  s.TenantID,
		UserID:    s.UserID,
		FamilyID:  s.FamilyID,
		TokenHash: s.TokenHash,
		ExpiresAt: s.ExpiresAt,
		UserAgent: s.UserAgent,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create session: %w", err)
	}
	return id, nil
}

func (r repository) LockSession(ctx context.Context, id uuid.UUID) (StoredSession, error) {
	row, err := r.q.LockSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredSession{}, ErrNotFound
	}
	if err != nil {
		return StoredSession{}, fmt.Errorf("lock session: %w", err)
	}
	return StoredSession{
		ID: row.ID, FamilyID: row.FamilyID, UserID: row.UserID,
		ExpiresAt: row.ExpiresAt, RotatedAt: row.RotatedAt, RevokedAt: row.RevokedAt,
	}, nil
}

func (r repository) SessionFamily(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	family, err := r.q.GetSessionFamily(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read session family: %w", err)
	}
	return family, nil
}

func (r repository) MarkRotated(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := r.q.MarkSessionRotated(ctx, queries.MarkSessionRotatedParams{ID: id, RotatedAt: &at}); err != nil {
		return fmt.Errorf("mark session rotated: %w", err)
	}
	return nil
}

func (r repository) RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	if err := r.q.RevokeSessionFamily(ctx, queries.RevokeSessionFamilyParams{FamilyID: familyID, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke session family: %w", err)
	}
	return nil
}

func (r repository) RevokeOtherSessions(ctx context.Context, userID, keepFamilyID uuid.UUID, at time.Time) error {
	err := r.q.RevokeOtherUserSessions(ctx, queries.RevokeOtherUserSessionsParams{
		UserID: userID, KeepFamilyID: keepFamilyID, RevokedAt: &at,
	})
	if err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	return nil
}

func (r repository) DeleteExpiredFamily(ctx context.Context, familyID uuid.UUID, before time.Time) error {
	if err := r.q.DeleteExpiredFamilySessions(ctx, queries.DeleteExpiredFamilySessionsParams{FamilyID: familyID, Before: before}); err != nil {
		return fmt.Errorf("delete expired sessions of a family: %w", err)
	}
	return nil
}

func (r repository) DeleteExpiredForUser(ctx context.Context, userID uuid.UUID, before time.Time) error {
	if err := r.q.DeleteExpiredUserSessions(ctx, queries.DeleteExpiredUserSessionsParams{UserID: userID, Before: before}); err != nil {
		return fmt.Errorf("delete expired sessions of a user: %w", err)
	}
	return nil
}

func (r repository) RecordLogin(ctx context.Context, userID uuid.UUID, at time.Time) error {
	if err := r.q.RecordLogin(ctx, queries.RecordLoginParams{ID: userID, LoggedInAt: &at}); err != nil {
		return fmt.Errorf("record login: %w", err)
	}
	return nil
}

func (r repository) PasswordHash(ctx context.Context, userID uuid.UUID) (string, error) {
	hash, err := r.q.GetPasswordHash(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read password hash: %w", err)
	}
	return hash, nil
}

func (r repository) SetPasswordHash(ctx context.Context, userID uuid.UUID, hash string) error {
	if err := r.q.SetPasswordHash(ctx, queries.SetPasswordHashParams{ID: userID, PasswordHash: hash}); err != nil {
		return fmt.Errorf("store password hash: %w", err)
	}
	return nil
}

func (r repository) Me(ctx context.Context, userID uuid.UUID) (user.Me, error) {
	row, err := r.q.GetMe(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Me{}, ErrNotFound
	}
	if err != nil {
		return user.Me{}, fmt.Errorf("read user: %w", err)
	}
	return user.Me{
		User: user.User{
			ID: row.ID, Email: row.Email, FullName: row.FullName, Phone: row.Phone, Role: identity.Role(row.Role),
			IsActive: row.IsActive, LastLoginAt: row.LastLoginAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
		Tenant: user.TenantSummary{
			ID: row.TenantID, Code: row.TenantCode, LegalName: row.TenantLegalName,
			GS1CompanyPrefix: row.TenantGs1CompanyPrefix, Status: row.TenantStatus,
		},
	}, nil
}
