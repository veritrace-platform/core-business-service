//go:build integration

package auth_test

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

func TestPostgresStore(t *testing.T) {
	db := tenancytest.Start(t)
	store := auth.NewPostgresStore(db.App, db.Tenancy)
	a, b := db.CreateTenant(t), db.CreateTenant(t)

	t.Run("finds login users by email, ignoring case", func(t *testing.T) {
		got, err := store.FindLoginUser(t.Context(), a.Admin.Email)
		if err != nil {
			t.Fatalf("FindLoginUser() error = %v", err)
		}
		upper, err := store.FindLoginUser(t.Context(), strings.ToUpper(a.Admin.Email))
		if err != nil || upper.UserID != got.UserID {
			t.Errorf("upper-case email: %+v, %v", upper, err)
		}
		if got.UserID != a.Admin.ID || got.TenantID != a.ID || got.Role != identity.RoleAdmin || !got.Active ||
			got.PasswordHash == "" {
			t.Errorf("FindLoginUser() = %+v", got)
		}
		if _, err := store.FindLoginUser(t.Context(), "nobody@fixture.example"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("unknown email: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("reports accounts that may not sign in", func(t *testing.T) {
		driver := db.CreateUser(t, b.ID, identity.RoleDriver)
		exec(t, db, `UPDATE core.users SET is_active = FALSE WHERE id = $1`, driver.ID)
		if got, err := store.FindLoginUser(t.Context(), driver.Email); err != nil || got.Active {
			t.Errorf("inactive user: %+v, %v; want inactive", got, err)
		}
		exec(t, db, `UPDATE core.tenants SET status = 'SUSPENDED' WHERE id = $1`, b.ID)
		defer exec(t, db, `UPDATE core.tenants SET status = 'ACTIVE' WHERE id = $1`, b.ID)
		if got, err := store.FindLoginUser(t.Context(), b.Admin.Email); err != nil || got.Active {
			t.Errorf("suspended tenant: %+v, %v; want inactive", got, err)
		}
	})

	t.Run("finds sessions by token hash and works inside the tenant", func(t *testing.T) {
		hash := sha256.Sum256([]byte("token-a"))
		family := uuid.Must(uuid.NewV7())
		now := time.Now().UTC().Truncate(time.Microsecond)
		var sessionID uuid.UUID
		err := store.WithTenantTx(t.Context(), a.ID, func(repo auth.Repository) error {
			var err error
			sessionID, err = repo.CreateSession(t.Context(), auth.NewSession{
				TenantID: a.ID, UserID: a.Admin.ID, FamilyID: family, TokenHash: hash[:], ExpiresAt: now.Add(time.Hour),
			})
			return err
		})
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}

		ref, err := store.FindSession(t.Context(), hash[:])
		if err != nil || ref.SessionID != sessionID || ref.FamilyID != family || ref.TenantID != a.ID || ref.UserID != a.Admin.ID {
			t.Fatalf("FindSession() = %+v, %v", ref, err)
		}
		missing := sha256.Sum256([]byte("unknown"))
		if _, err := store.FindSession(t.Context(), missing[:]); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("unknown hash: error = %v, want ErrNotFound", err)
		}

		err = store.WithTenantTx(t.Context(), a.ID, func(repo auth.Repository) error {
			locked, err := repo.LockSession(t.Context(), sessionID)
			if err != nil || !locked.ExpiresAt.Equal(now.Add(time.Hour)) || locked.RotatedAt != nil {
				t.Errorf("LockSession() = %+v, %v", locked, err)
			}
			if err := repo.MarkRotated(t.Context(), sessionID, now); err != nil {
				return err
			}
			if f, err := repo.SessionFamily(t.Context(), sessionID); err != nil || f != family {
				t.Errorf("SessionFamily() = %s, %v", f, err)
			}
			if err := repo.RevokeFamily(t.Context(), family, now); err != nil {
				return err
			}
			me, err := repo.Me(t.Context(), a.Admin.ID)
			if err != nil || me.Tenant.ID != a.ID || me.Tenant.Code != a.Code || me.Tenant.GS1CompanyPrefix != a.GCP {
				t.Errorf("Me() = %+v, %v", me, err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("tenant transaction: %v", err)
		}

		// Another tenant sees nothing of the session.
		err = store.WithTenantTx(t.Context(), b.ID, func(repo auth.Repository) error {
			if _, err := repo.LockSession(t.Context(), sessionID); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("other tenant: LockSession() error = %v, want ErrNotFound", err)
			}
			if _, err := repo.Me(t.Context(), a.Admin.ID); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("other tenant: Me() error = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		db.AssertVisible(t, a.ID, tenancytest.Rows{Table: "core.auth_sessions", Where: map[string]any{"id": sessionID}})
		db.AssertHidden(t, b.ID, tenancytest.Rows{Table: "core.auth_sessions", Where: map[string]any{"id": sessionID}})
		db.AssertHidden(t, tenancytest.NoTenant, tenancytest.Rows{Table: "core.auth_sessions", Where: map[string]any{"id": sessionID}})
	})

	t.Run("sessions cannot point at another tenant's user", func(t *testing.T) {
		hash := sha256.Sum256([]byte("token-cross"))
		db.AssertDenied(t, b.ID, `INSERT INTO core.auth_sessions (tenant_id, user_id, family_id, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, now())`, a.ID, a.Admin.ID, uuid.New(), hash[:])
		// Even the owner, which bypasses row-level security, cannot attach a session to a user of
		// another tenant: the composite foreign key includes the tenant.
		_, err := db.Owner.Exec(t.Context(), `INSERT INTO core.auth_sessions (tenant_id, user_id, family_id, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, now())`, b.ID, a.Admin.ID, uuid.New(), hash[:])
		if err == nil {
			t.Error("a session of tenant B referenced a user of tenant A")
		}
	})
}

func exec(t *testing.T, db *tenancytest.Database, sql string, args ...any) {
	t.Helper()
	if _, err := db.Owner.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
