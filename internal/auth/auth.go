package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/ratelimit"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// Config holds the authentication settings, read from the environment.
type Config struct {
	// SigningKeys lists the Ed25519 signing keys; see ParseKeySet. The first one signs.
	SigningKeys string `env:"JWT_SIGNING_KEYS"`
	// RefreshTokenTTL is how long a refresh token stays valid; each refresh starts a new period.
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"168h"`
	// TrustedProxies lists the CIDR ranges whose X-Forwarded-For header names the client address.
	TrustedProxies string `env:"TRUSTED_PROXIES" envDefault:"127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"`
}

// Validate reports the settings that are missing or invalid.
func (c Config) Validate() error {
	var errs []error
	if c.SigningKeys == "" {
		errs = append(errs, errors.New("JWT_SIGNING_KEYS is required"))
	} else if _, err := ParseKeySet(c.SigningKeys); err != nil {
		errs = append(errs, fmt.Errorf("JWT_SIGNING_KEYS: %w", err))
	}
	if c.RefreshTokenTTL < time.Hour {
		errs = append(errs, fmt.Errorf("REFRESH_TOKEN_TTL must be at least 1h, got %s", c.RefreshTokenTTL))
	}
	if _, err := ratelimit.NewClientIP(c.TrustedProxies); err != nil {
		errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
	}
	return errors.Join(errs...)
}

// Authentication failures.
var (
	// ErrInvalidCredentials hides whether the email, the password, or the account status was wrong.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrRefreshTokenInvalid covers unknown, expired, revoked, and reused refresh tokens.
	ErrRefreshTokenInvalid = errors.New("refresh token invalid")
	// ErrRefreshTokenReused reports a rotated token presented again. Its whole family has been revoked.
	ErrRefreshTokenReused = fmt.Errorf("%w: reused", ErrRefreshTokenInvalid)
	// ErrIncorrectPassword reports a wrong current password on a password change.
	ErrIncorrectPassword = errors.New("current password is incorrect")
	// ErrNotFound reports a missing record in the store.
	ErrNotFound = errors.New("not found")
)

// Session is what a login or refresh returns: a new access token, the refresh token that replaces the
// presented one, and the signed-in user.
type Session struct {
	AccessToken     string
	AccessTokenTTL  time.Duration
	RefreshToken    string
	RefreshTokenTTL time.Duration
	User            user.Me
}

// LoginUser is an account found by email before a tenant context exists.
type LoginUser struct {
	UserID       uuid.UUID
	TenantID     uuid.UUID
	PasswordHash string
	Role         identity.Role
	// Active is false when the user or its tenant may not sign in.
	Active bool
}

// SessionRef locates a refresh session found by its token hash.
type SessionRef struct {
	SessionID uuid.UUID
	FamilyID  uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
}

// StoredSession is a refresh session read, and locked, inside its tenant's transaction.
type StoredSession struct {
	ID        uuid.UUID
	FamilyID  uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
	RotatedAt *time.Time
	RevokedAt *time.Time
}

// NewSession is a refresh session to store.
type NewSession struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	TokenHash []byte
	ExpiresAt time.Time
	UserAgent *string
}
