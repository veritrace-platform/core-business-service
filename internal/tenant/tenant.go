// Package tenant registers tenants and manages the tenant profile (data-model.md §3.1). A tenant is one
// company, identified by its code, tax code, and GS1 company prefix.
package tenant

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// Status of a tenant.
type Status string

// Statuses. Users of a suspended tenant cannot sign in.
const (
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
)

// Tenant is a company registered on VeriTrace.
type Tenant struct {
	ID                 uuid.UUID `json:"id"`
	Code               string    `json:"code"`
	LegalName          string    `json:"legal_name"`
	TaxCode            string    `json:"tax_code"`
	GS1CompanyPrefix   string    `json:"gs1_company_prefix"`
	SSCCExtensionDigit int       `json:"sscc_extension_digit"`
	Status             Status    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Registration is a validated request to register a tenant with its headquarters and first admin.
type Registration struct {
	Code             string
	LegalName        string
	TaxCode          string
	GS1CompanyPrefix string
	Headquarters     Headquarters
	Admin            Admin
}

// Headquarters is the tenant's first location.
type Headquarters struct {
	GLN                  string
	Name                 string
	Address              string
	City                 string
	CountryCode          string
	Latitude             float64
	Longitude            float64
	GeoFenceRadiusMeters int
}

// Admin is the tenant's first user, who gets the ADMIN role.
type Admin struct {
	Email    string
	Password string
	FullName string
	Phone    *string
}

// Registered is a registered tenant with the records created for it.
type Registered struct {
	Tenant       Tenant            `json:"tenant"`
	Headquarters location.Location `json:"headquarters"`
	Admin        user.User         `json:"admin"`
}

// Key names a unique key of a registration.
type Key string

// Unique keys checked at registration.
const (
	KeyCode            Key = "code"
	KeyTaxCode         Key = "tax_code"
	KeyCompanyPrefix   Key = "gs1_company_prefix"
	KeyHeadquartersGLN Key = "headquarters_gln"
	KeyAdminEmail      Key = "admin_email"
)

// ConflictError reports a unique key that another tenant or user already holds. For the company prefix it also
// covers a prefix that extends, or is extended by, a registered one.
type ConflictError struct {
	Key Key
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s is already registered", e.Key)
}

// ErrNotFound reports a tenant that does not exist or is not visible.
var ErrNotFound = errors.New("tenant not found")

// ProfilePatch lists the changes to a tenant profile; nil fields stay as they are. The code, tax code, and
// company prefix identify the tenant and never change.
type ProfilePatch struct {
	LegalName *string
	// SSCCExtensionDigit starts a new SSCC serial space when the current one is exhausted.
	SSCCExtensionDigit *int
}
