// Package directory looks up the entries that every tenant may see: active locations by GLN, to pick a
// shipment destination or confirm a checkpoint facility, and active tenants by code, to pick a carrier or an
// inspector (data-model.md §3.5). Entries carry public fields only.
package directory

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Tenant is the public entry of an active tenant.
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	LegalName string    `json:"legal_name"`
}

// Location is the public entry of an active location of an active tenant.
type Location struct {
	GLN                  string  `json:"gln"`
	Name                 string  `json:"name"`
	Address              string  `json:"address"`
	City                 string  `json:"city"`
	CountryCode          string  `json:"country_code"`
	Latitude             float64 `json:"latitude"`
	Longitude            float64 `json:"longitude"`
	GeoFenceRadiusMeters int     `json:"geo_fence_radius_meters"`
	// Tenant owns the location.
	Tenant Tenant `json:"tenant"`
}

// ErrNotFound reports a GLN or tenant code without an active entry.
var ErrNotFound = errors.New("directory entry not found")

// Store looks up entries across tenants.
type Store interface {
	// LocationByGLN returns ErrNotFound for a GLN without an active location.
	LocationByGLN(ctx context.Context, gln string) (Location, error)
	// TenantByCode returns ErrNotFound for a code without an active tenant.
	TenantByCode(ctx context.Context, code string) (Tenant, error)
}

// Service looks up directory entries.
type Service struct {
	store Store
}

// NewService returns a Service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

func authorize(p identity.Principal) error {
	return policy.Evaluate(policy.Request{
		Action: policy.LookUpDirectory, Role: p.Role, Parties: []policy.Party{policy.AnyTenant},
	})
}

// Location returns the entry of the active location with the GLN.
func (s *Service) Location(ctx context.Context, p identity.Principal, gln string) (Location, error) {
	if err := authorize(p); err != nil {
		return Location{}, err
	}
	return s.store.LocationByGLN(ctx, gln)
}

// Tenant returns the entry of the active tenant with the code.
func (s *Service) Tenant(ctx context.Context, p identity.Principal, code string) (Tenant, error) {
	if err := authorize(p); err != nil {
		return Tenant{}, err
	}
	return s.store.TenantByCode(ctx, code)
}
