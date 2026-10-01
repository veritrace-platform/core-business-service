// Package user manages the users of a tenant (data-model.md §3.1). A user belongs to exactly one tenant and
// has exactly one role.
package user

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
)

// User is a person who signs in to VeriTrace. The password hash never leaves the store.
type User struct {
	ID          uuid.UUID     `json:"id"`
	Email       string        `json:"email"`
	FullName    string        `json:"full_name"`
	Phone       *string       `json:"phone"`
	Role        identity.Role `json:"role"`
	IsActive    bool          `json:"is_active"`
	LastLoginAt *time.Time    `json:"last_login_at"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// TenantSummary identifies the tenant of the signed-in user.
type TenantSummary struct {
	ID               uuid.UUID `json:"id"`
	Code             string    `json:"code"`
	LegalName        string    `json:"legal_name"`
	GS1CompanyPrefix string    `json:"gs1_company_prefix"`
	Status           string    `json:"status"`
}

// Me is the signed-in user with a summary of its tenant.
type Me struct {
	User
	Tenant TenantSummary `json:"tenant"`
}

// NewUser is a validated request to add a user to the caller's tenant.
type NewUser struct {
	Email    string
	Password string
	FullName string
	Phone    *string
	Role     identity.Role
}

// Filter selects a page of users, newest first.
type Filter struct {
	Role     *identity.Role
	IsActive *bool
	// After is the last user of the previous page; uuid.Nil starts at the newest user.
	After uuid.UUID
	Limit int
}

// Patch lists the changes to a user; nil fields stay as they are. ClearPhone removes the phone number.
type Patch struct {
	FullName   *string
	Phone      *string
	ClearPhone bool
	Role       *identity.Role
	IsActive   *bool
}

// User errors.
var (
	// ErrNotFound reports a user that does not exist or belongs to another tenant.
	ErrNotFound = errors.New("user not found")
	// ErrEmailTaken reports an email address that another user, of any tenant, already has.
	ErrEmailTaken = errors.New("email address already registered")
	// ErrSelfChange reports an admin changing its own role or deactivating itself. Forbidding it keeps at least
	// one active admin in every tenant.
	ErrSelfChange = errors.New("admins cannot change their own role or deactivate themselves")
	// ErrActorNotAdmin reports a caller whose token still says ADMIN although the account is no longer an active
	// admin.
	ErrActorNotAdmin = errors.New("the caller is no longer an active admin")
)
