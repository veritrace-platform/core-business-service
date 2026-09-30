// Package user manages the users of a tenant (data-model.md §3.1). A user belongs to exactly one tenant and
// has exactly one role.
package user

import (
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
