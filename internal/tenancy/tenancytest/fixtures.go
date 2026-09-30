package tenancytest

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/password"
)

// FixturePassword is the password of every fixture user.
const FixturePassword = "fixture password 1"

var (
	fixtureSeq      atomic.Int64
	fixtureHashOnce sync.Once
	fixtureHash     string
	errFixtureHash  error
)

// passwordHash returns the hash of FixturePassword, computed once per test binary.
func passwordHash(t testing.TB) string {
	t.Helper()
	fixtureHashOnce.Do(func() {
		var h *password.Hasher
		if h, errFixtureHash = password.NewHasher(1); errFixtureHash == nil {
			fixtureHash, errFixtureHash = h.Hash(context.Background(), FixturePassword)
		}
	})
	if errFixtureHash != nil {
		t.Fatalf("hash fixture password: %v", errFixtureHash)
	}
	return fixtureHash
}

// Tenant is a registered fixture tenant.
type Tenant struct {
	ID             uuid.UUID
	Code           string
	GCP            string
	HeadquartersID uuid.UUID
	Admin          User
}

// User is a fixture user; its password is FixturePassword.
type User struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
	Role     identity.Role
}

// CreateTenant registers a tenant through core.register_tenant, with an ADMIN whose password is
// FixturePassword. Fixture company prefixes are ten digits starting with 99, so they never overlap the
// shorter prefixes that tests register themselves.
func (d *Database) CreateTenant(t testing.TB) Tenant {
	t.Helper()
	n := fixtureSeq.Add(1)
	gcp := fmt.Sprintf("99%08d", n)
	gln := gcp + "00" + strconv.Itoa(gs1.CheckDigit(gcp+"00"))
	tenant := Tenant{Code: fmt.Sprintf("FIXTURE_%d", n), GCP: gcp}
	tenant.Admin = User{Email: fmt.Sprintf("admin-%d@fixture.example", n), Role: identity.RoleAdmin}

	err := d.Owner.QueryRow(t.Context(), `
		SELECT tenant_id, headquarters_location_id, admin_user_id
		FROM core.register_tenant($1, $2, $3, $4, $5, $6, $7, $8, 'VN', 10.77, 106.7, 200, $9, $10, $11, NULL)`,
		tenant.Code, "Fixture Company "+strconv.FormatInt(n, 10), fmt.Sprintf("%010d", n), gcp,
		gln, "Headquarters", "1 Fixture Street", "Ho Chi Minh City",
		tenant.Admin.Email, passwordHash(t), "Fixture Admin",
	).Scan(&tenant.ID, &tenant.HeadquartersID, &tenant.Admin.ID)
	if err != nil {
		t.Fatalf("register fixture tenant: %v", err)
	}
	tenant.Admin.TenantID = tenant.ID
	return tenant
}

// CreateUser adds an active user with the given role to a tenant.
func (d *Database) CreateUser(t testing.TB, tenantID uuid.UUID, role identity.Role) User {
	t.Helper()
	u := User{TenantID: tenantID, Role: role, Email: fmt.Sprintf("user-%d@fixture.example", fixtureSeq.Add(1))}
	err := d.Owner.QueryRow(t.Context(), `
		INSERT INTO core.users (tenant_id, email, password_hash, full_name, role)
		VALUES ($1, $2, $3, 'Fixture User', $4)
		RETURNING id`,
		tenantID, u.Email, passwordHash(t), string(role),
	).Scan(&u.ID)
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	return u
}
