package identity_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
)

func TestRoleValid(t *testing.T) {
	for _, role := range identity.Roles {
		if !role.Valid() {
			t.Errorf("%s.Valid() = false", role)
		}
	}
	for _, role := range []identity.Role{"", "admin", "OWNER", "SUPERUSER"} {
		if role.Valid() {
			t.Errorf("%q.Valid() = true", role)
		}
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := identity.FromContext(context.Background()); ok {
		t.Fatal("FromContext() found a principal in an empty context")
	}
	want := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleDriver, SessionID: uuid.New()}
	got, ok := identity.FromContext(identity.NewContext(context.Background(), want))
	if !ok || got != want {
		t.Errorf("FromContext() = %+v, %t; want %+v", got, ok, want)
	}
}
