//go:build integration

package tenancy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/veritrace-platform/core-business-service/internal/tenancy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// probeDDL creates a tenant-scoped table protected by the standard tenant policy.
const probeDDL = `
CREATE TABLE core.isolation_probes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    label text NOT NULL
);
ALTER TABLE core.isolation_probes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.isolation_probes
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));
`

const insertProbeSQL = `INSERT INTO core.isolation_probes (tenant_id, label) VALUES ($1, $2) RETURNING id`

func TestTenantContext(t *testing.T) {
	db := tenancytest.Start(t)
	if _, err := db.Owner.Exec(t.Context(), probeDDL); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	tenantA, tenantB := uuid.New(), uuid.New()
	rowA := insertFixture(t, db, tenantA)
	rowB := insertFixture(t, db, tenantB)

	t.Run("current_tenant_id reads the transaction setting", func(t *testing.T) {
		testCurrentTenantID(t, db, tenantA)
	})

	t.Run("scopes reads and writes to the tenant", func(t *testing.T) {
		db.AssertVisible(t, tenantA, probe(rowA))
		db.AssertHidden(t, tenantA, probe(rowB))
		db.AssertVisible(t, tenantB, probe(rowB))
		db.AssertHidden(t, tenantB, probe(rowA))
	})

	t.Run("fails closed without a tenant context", func(t *testing.T) {
		db.AssertHidden(t, tenancytest.NoTenant, probe(rowA))
		db.AssertDenied(t, tenancytest.NoTenant, insertProbeSQL, tenantA, "no context")
	})

	t.Run("rejects rows written for another tenant", func(t *testing.T) {
		db.AssertDenied(t, tenantA, insertProbeSQL, tenantB, "foreign")
		db.AssertDenied(t, tenantA, `UPDATE core.isolation_probes SET tenant_id = $1 WHERE id = $2`, tenantB, rowA)
	})

	t.Run("commits the work of fn", func(t *testing.T) {
		var id uuid.UUID
		err := db.Tenancy.WithTenantTx(t.Context(), tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), insertProbeSQL, tenantA, "committed").Scan(&id)
		})
		if err != nil {
			t.Fatalf("WithTenantTx() error = %v", err)
		}
		db.AssertVisible(t, tenantA, probe(id))
		db.AssertHidden(t, tenantB, probe(id))
	})

	t.Run("rolls back when fn fails", func(t *testing.T) {
		errAbort := errors.New("abort")
		var id uuid.UUID
		err := db.Tenancy.WithTenantTx(t.Context(), tenantA, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), insertProbeSQL, tenantA, "rolled back").Scan(&id); err != nil {
				return err
			}
			return errAbort
		})
		if !errors.Is(err, errAbort) {
			t.Fatalf("WithTenantTx() error = %v, want %v", err, errAbort)
		}
		assertAbsent(t, db, id)
	})

	t.Run("rolls back when fn panics", func(t *testing.T) {
		var id uuid.UUID
		func() {
			defer func() {
				if r := recover(); r != "boom" {
					t.Errorf("recovered %v, want the panic from fn", r)
				}
			}()
			_ = db.Tenancy.WithTenantTx(t.Context(), tenantA, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), insertProbeSQL, tenantA, "panicked").Scan(&id); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		assertAbsent(t, db, id)
	})

	t.Run("does not leak the tenant context to the next transaction", func(t *testing.T) {
		// With one connection, the next query must reuse the connection that ran the tenant transaction.
		cfg := db.App.Config()
		cfg.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatalf("create pool: %v", err)
		}
		defer pool.Close()

		for _, fnErr := range []error{nil, errors.New("abort")} {
			_ = tenancy.NewDB(pool).WithTenantTx(t.Context(), tenantA, func(pgx.Tx) error { return fnErr })
			var tenantIsNull bool
			if err := pool.QueryRow(t.Context(), `SELECT core.current_tenant_id() IS NULL`).Scan(&tenantIsNull); err != nil {
				t.Fatalf("read tenant context: %v", err)
			}
			if !tenantIsNull {
				t.Errorf("tenant context outlived a transaction that ended with error %v", fnErr)
			}
		}
	})

	t.Run("keeps tenant policies parallel safe", func(t *testing.T) {
		err := db.Tenancy.WithTenantTx(t.Context(), tenantA, func(tx pgx.Tx) error {
			// Forces a parallel plan wherever one is allowed, so workers evaluate the policy too.
			if _, err := tx.Exec(t.Context(), `SET LOCAL debug_parallel_query = on`); err != nil {
				return err
			}
			rows, err := tx.Query(t.Context(), `EXPLAIN (COSTS OFF) SELECT tenant_id FROM core.isolation_probes`)
			if err != nil {
				return err
			}
			plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if !strings.Contains(strings.Join(plan, "\n"), "Gather") {
				t.Errorf("plan is not parallel, so the policy blocks parallel query:\n%s", strings.Join(plan, "\n"))
			}

			rows, err = tx.Query(t.Context(), `SELECT tenant_id FROM core.isolation_probes`)
			if err != nil {
				return err
			}
			tenants, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return err
			}
			for _, got := range tenants {
				if got != tenantA {
					t.Errorf("parallel query returned a row of tenant %s, want only %s", got, tenantA)
				}
			}
			if len(tenants) == 0 {
				t.Error("parallel query returned no rows")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithTenantTx() error = %v", err)
		}
	})
}

// testCurrentTenantID checks core.current_tenant_id() on a fresh connection, where the setting starts
// undefined, through each state the setting can be in.
func testCurrentTenantID(t *testing.T, db *tenancytest.Database, tenantID uuid.UUID) {
	conn, err := pgx.ConnectConfig(t.Context(), db.App.Config().ConnConfig)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(t.Context()) }()

	steps := []struct {
		name     string
		setting  *string // nil leaves the setting untouched
		wantNull bool
		want     uuid.UUID
		wantCode string
	}{
		{name: "undefined in the session", wantNull: true},
		{name: "set to the tenant", setting: new(tenantID.String()), want: tenantID},
		{name: "reset when the setting transaction ended", wantNull: true},
		{name: "empty", setting: new(""), wantNull: true},
		{name: "malformed", setting: new("not-a-uuid"), wantCode: "22P02"},
	}
	for _, step := range steps {
		var got pgtype.UUID
		err := pgx.BeginFunc(t.Context(), conn, func(tx pgx.Tx) error {
			if step.setting != nil {
				if _, err := tx.Exec(t.Context(), `SELECT set_config('app.current_tenant_id', $1, true)`, *step.setting); err != nil {
					return err
				}
			}
			return tx.QueryRow(t.Context(), `SELECT core.current_tenant_id()`).Scan(&got)
		})

		switch {
		case step.wantCode != "":
			if !hasCode(err, step.wantCode) {
				t.Errorf("%s: error = %v, want SQLSTATE %s", step.name, err, step.wantCode)
			}
		case err != nil:
			t.Errorf("%s: error = %v", step.name, err)
		case step.wantNull && got.Valid:
			t.Errorf("%s: current_tenant_id() = %v, want NULL", step.name, uuid.UUID(got.Bytes))
		case !step.wantNull && (!got.Valid || uuid.UUID(got.Bytes) != step.want):
			t.Errorf("%s: current_tenant_id() = %v, want %s", step.name, got, step.want)
		}
	}
}

func probe(id uuid.UUID) tenancytest.Rows {
	return tenancytest.Rows{Table: "core.isolation_probes", Where: map[string]any{"id": id}}
}

func insertFixture(t *testing.T, db *tenancytest.Database, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Owner.QueryRow(t.Context(), insertProbeSQL, tenantID, "fixture").Scan(&id); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	return id
}

func assertAbsent(t *testing.T, db *tenancytest.Database, id uuid.UUID) {
	t.Helper()
	var exists bool
	err := db.Owner.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM core.isolation_probes WHERE id = $1)`, id).
		Scan(&exists)
	if err != nil {
		t.Fatalf("look up row: %v", err)
	}
	if exists {
		t.Errorf("row %s was committed, want it rolled back", id)
	}
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
