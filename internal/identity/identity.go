// Package identity defines who acts in the core service: user roles and the authenticated principal carried
// through a request.
package identity

import (
	"context"

	"github.com/google/uuid"
)

// Role is a user's role within its tenant (access-control.md §1). A user has exactly one role.
type Role string

// Roles.
const (
	RoleAdmin            Role = "ADMIN"
	RoleWarehouseManager Role = "WAREHOUSE_MANAGER"
	RoleDriver           Role = "DRIVER"
	RoleInspector        Role = "INSPECTOR"
)

// Roles lists every role.
var Roles = []Role{RoleAdmin, RoleWarehouseManager, RoleDriver, RoleInspector}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleWarehouseManager, RoleDriver, RoleInspector:
		return true
	}
	return false
}

// Principal is the authenticated caller of a request, as stated by its access token.
type Principal struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	Role     Role
	// SessionID identifies the refresh session that issued the access token (the token's jti).
	SessionID uuid.UUID
}

type contextKey struct{}

// NewContext returns a copy of ctx carrying p.
func NewContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// FromContext returns the principal stored in ctx, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}
