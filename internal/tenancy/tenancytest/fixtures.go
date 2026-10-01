package tenancytest

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	ID           uuid.UUID
	Code         string
	GCP          string
	Headquarters Location
	Admin        User
}

// Location is a fixture location.
type Location struct {
	ID  uuid.UUID
	GLN string
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
	tenant := Tenant{Code: fmt.Sprintf("FIXTURE_%d", n), GCP: gcp, Headquarters: Location{GLN: glnFor(gcp, 0)}}
	tenant.Admin = User{Email: fmt.Sprintf("admin-%d@fixture.example", n), Role: identity.RoleAdmin}

	err := d.Owner.QueryRow(t.Context(), `
		SELECT tenant_id, headquarters_location_id, admin_user_id
		FROM core.register_tenant($1, $2, $3, $4, $5, $6, $7, $8, 'VN', 10.77, 106.7, 200, $9, $10, $11, NULL)`,
		tenant.Code, "Fixture Company "+strconv.FormatInt(n, 10), fmt.Sprintf("%010d", n), gcp,
		tenant.Headquarters.GLN, "Headquarters", "1 Fixture Street", "Ho Chi Minh City",
		tenant.Admin.Email, passwordHash(t), "Fixture Admin",
	).Scan(&tenant.ID, &tenant.Headquarters.ID, &tenant.Admin.ID)
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

// glnFor returns the GLN with location reference ref under the company prefix gcp.
func glnFor(gcp string, ref int) string {
	payload := gcp + fmt.Sprintf("%0*d", 12-len(gcp), ref)
	return payload + strconv.Itoa(gs1.CheckDigit(payload))
}

// CreateLocation adds an active location to a tenant. Its GLN takes the next location reference under the
// tenant's company prefix; the headquarters has reference 0.
func (d *Database) CreateLocation(t testing.TB, tenant Tenant) Location {
	t.Helper()
	var count int
	if err := d.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.locations WHERE tenant_id = $1`,
		tenant.ID).Scan(&count); err != nil {
		t.Fatalf("count fixture locations: %v", err)
	}
	l := Location{GLN: glnFor(tenant.GCP, count)}
	err := d.Owner.QueryRow(t.Context(), `
		INSERT INTO core.locations (tenant_id, gln, name, address, city, latitude, longitude)
		VALUES ($1, $2, 'Fixture Warehouse', '2 Fixture Street', 'Ho Chi Minh City', 10.8, 106.65)
		RETURNING id`,
		tenant.ID, l.GLN,
	).Scan(&l.ID)
	if err != nil {
		t.Fatalf("create fixture location: %v", err)
	}
	return l
}

// Product is a fixture product.
type Product struct {
	ID   uuid.UUID
	GTIN string
}

// CreateProduct adds an active product kept at 2 to 8 °C to a tenant. Its GTIN is a base unit with the next item
// reference under the tenant's company prefix.
func (d *Database) CreateProduct(t testing.TB, tenant Tenant) Product {
	t.Helper()
	var count int
	if err := d.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.products WHERE tenant_id = $1`,
		tenant.ID).Scan(&count); err != nil {
		t.Fatalf("count fixture products: %v", err)
	}
	payload := "0" + tenant.GCP + fmt.Sprintf("%0*d", 12-len(tenant.GCP), count+1)
	p := Product{GTIN: payload + strconv.Itoa(gs1.CheckDigit(payload))}
	err := d.Owner.QueryRow(t.Context(), `
		INSERT INTO core.products (tenant_id, gtin, name, min_temp_celsius, max_temp_celsius)
		VALUES ($1, $2, 'Fixture Product', 2, 8)
		RETURNING id`,
		tenant.ID, p.GTIN,
	).Scan(&p.ID)
	if err != nil {
		t.Fatalf("create fixture product: %v", err)
	}
	return p
}

// Lot is a fixture lot.
type Lot struct {
	ID        uuid.UUID
	LotNumber string
}

// CommissionLot commissions quantity units of a tenant's product at one of its locations, as its admin: the lot,
// the location's balance, and the COMMISSIONED movement. The lot expires 30 days after today.
func (d *Database) CommissionLot(t testing.TB, tenant Tenant, product Product, location Location, quantity int) Lot {
	t.Helper()
	l := Lot{LotNumber: fmt.Sprintf("FIX-%d", fixtureSeq.Add(1))}
	err := pgx.BeginFunc(t.Context(), d.Owner, func(tx pgx.Tx) error {
		err := tx.QueryRow(t.Context(), `
			INSERT INTO core.lots (tenant_id, product_id, gtin, product_name, min_temp_celsius, max_temp_celsius,
			                       lot_number, production_date, expiration_date, quantity_commissioned,
			                       commissioned_location_id, created_by)
			SELECT p.tenant_id, p.id, p.gtin, p.name, p.min_temp_celsius, p.max_temp_celsius,
			       $2, current_date, current_date + 30, $3, $4, $5
			FROM core.products p
			WHERE p.id = $1
			RETURNING id`,
			product.ID, l.LotNumber, quantity, location.ID, tenant.Admin.ID,
		).Scan(&l.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO core.inventory_balances (tenant_id, location_id, lot_id, quantity_on_hand)
			VALUES ($1, $2, $3, $4)`,
			tenant.ID, location.ID, l.ID, quantity); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `
			INSERT INTO core.inventory_movements (tenant_id, location_id, lot_id, quantity_delta, reason, created_by)
			VALUES ($1, $2, $3, $4, 'COMMISSIONED', $5)`,
			tenant.ID, location.ID, l.ID, quantity, tenant.Admin.ID)
		return err
	})
	if err != nil {
		t.Fatalf("commission fixture lot: %v", err)
	}
	return l
}

// AddBalance gives a tenant a balance of a lot at one of its locations, as receiving a shipment of a lot owned by
// another tenant does. The tenant becomes a holder of the lot.
func (d *Database) AddBalance(t testing.TB, holder Tenant, location Location, lotID uuid.UUID, quantity int) {
	t.Helper()
	if _, err := d.Owner.Exec(t.Context(), `
		INSERT INTO core.inventory_balances (tenant_id, location_id, lot_id, quantity_on_hand)
		VALUES ($1, $2, $3, $4)`,
		holder.ID, location.ID, lotID, quantity); err != nil {
		t.Fatalf("add fixture balance: %v", err)
	}
}
