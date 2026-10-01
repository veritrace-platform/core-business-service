package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
	"github.com/veritrace-platform/core-business-service/internal/user/queries"
)

// PostgresStore keeps users in PostgreSQL.
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

func (r repository) List(ctx context.Context, f Filter) ([]User, error) {
	params := queries.ListUsersParams{IsActive: f.IsActive, RowLimit: int32(f.Limit)} //nolint:gosec // limit is at most 101
	if f.Role != nil {
		role := string(*f.Role)
		params.Role = &role
	}
	if f.After != uuid.Nil {
		params.After = &f.After
	}
	rows, err := r.q.ListUsers(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users := make([]User, len(rows))
	for i, row := range rows {
		users[i] = fromRow(queries.GetUserRow(row))
	}
	return users, nil
}

func (r repository) Get(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := r.q.GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	return fromRow(row), nil
}

func (r repository) Create(ctx context.Context, tenantID uuid.UUID, u NewUser, passwordHash string) (User, error) {
	row, err := r.q.CreateUser(ctx, queries.CreateUserParams{
		TenantID: tenantID, Email: u.Email, PasswordHash: passwordHash, FullName: u.FullName, Phone: u.Phone,
		Role: string(u.Role),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return fromRow(queries.GetUserRow(row)), nil
}

func (r repository) Update(ctx context.Context, id uuid.UUID, p Patch) (User, error) {
	params := queries.UpdateUserParams{ID: id, FullName: p.FullName, IsActive: p.IsActive}
	if p.ClearPhone || p.Phone != nil {
		params.SetPhone, params.Phone = true, p.Phone
	}
	if p.Role != nil {
		role := string(*p.Role)
		params.Role = &role
	}
	row, err := r.q.UpdateUser(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("update user: %w", err)
	}
	return fromRow(queries.GetUserRow(row)), nil
}

func (r repository) LockTenant(ctx context.Context, tenantID uuid.UUID) error {
	if _, err := r.q.LockTenant(ctx, tenantID); err != nil {
		return fmt.Errorf("lock tenant: %w", err)
	}
	return nil
}

func (r repository) RevokeSessions(ctx context.Context, userID uuid.UUID, at time.Time) error {
	if err := r.q.RevokeUserSessions(ctx, queries.RevokeUserSessionsParams{UserID: userID, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

func fromRow(row queries.GetUserRow) User {
	return User{
		ID: row.ID, Email: row.Email, FullName: row.FullName, Phone: row.Phone, Role: identity.Role(row.Role),
		IsActive: row.IsActive, LastLoginAt: row.LastLoginAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
