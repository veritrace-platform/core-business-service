// Package tenancytest provides a migrated core database and row-level security assertions for integration
// tests. Fixtures are written through the owner role, which bypasses row-level security. Assertions run as
// the runtime role through the production tenant transaction helper, so they observe what the service
// observes, and they always roll back.
package tenancytest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/veritrace-platform/core-business-service/internal/platform/migrate"
	"github.com/veritrace-platform/core-business-service/internal/platform/postgres"
	"github.com/veritrace-platform/core-business-service/internal/platform/postgres/postgrestest"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
	"github.com/veritrace-platform/core-business-service/migrations"
)

// Database roles, named as the infrastructure bootstrap names them.
const (
	OwnerRole = migrations.Database + "_owner"
	AppRole   = migrations.Database + "_app"
)

// NoTenant runs an assertion without a tenant context, as pre-authentication code paths run.
var NoTenant = uuid.Nil

// Database is a migrated core database.
type Database struct {
	// Owner connects as the schema owner, which bypasses row-level security. Use it for fixtures.
	Owner *pgxpool.Pool
	// App connects as the runtime role, to which row-level security applies.
	App *pgxpool.Pool
	// Tenancy runs tenant-scoped transactions on App.
	Tenancy *tenancy.DB
}

// Start launches PostgreSQL, applies every migration, and connects as both roles. Everything is removed when
// the test finishes. Subtests share the database, so each test creates its own tenants and rows.
func Start(t *testing.T) *Database {
	t.Helper()
	database := postgrestest.Start(t).CreateDatabase(t, migrations.Database, OwnerRole, AppRole,
		migrations.RequiredExtensions...)

	runner, err := migrate.Open(database.OwnerURL, migrations.FS, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open migrations: %v", err)
	}
	defer func() { _ = runner.Close() }()
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	app := connect(t, database.AppURL)
	return &Database{Owner: connect(t, database.OwnerURL), App: app, Tenancy: tenancy.NewDB(app)}
}

func connect(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), url, "tenancytest")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Rows selects rows of one table by column values combined with AND, for example
// Rows{Table: "core.locations", Where: map[string]any{"id": id}}.
type Rows struct {
	Table string         // schema-qualified table name
	Where map[string]any // column name to value; at least one column
}

func (r Rows) String() string {
	return fmt.Sprintf("%s where %v", r.Table, r.Where)
}

// AssertVisible fails the test unless tenantID can read at least one of the rows.
func (d *Database) AssertVisible(t testing.TB, tenantID uuid.UUID, rows Rows) {
	t.Helper()
	if n := d.count(t, d.inTx(tenantID), rows); n == 0 {
		t.Errorf("%s: not visible to %s", rows, tenantLabel(tenantID))
	}
}

// AssertHidden fails the test if tenantID can read, update, or delete any of the rows. The rows must exist, so
// a wrong key cannot make the assertion pass.
func (d *Database) AssertHidden(t testing.TB, tenantID uuid.UUID, rows Rows) {
	t.Helper()
	if d.count(t, d.asOwner, rows) == 0 {
		t.Fatalf("%s: no such rows, so hiding them proves nothing", rows)
	}
	if n := d.count(t, d.inTx(tenantID), rows); n != 0 {
		t.Errorf("%s: %s can read %d row(s)", rows, tenantLabel(tenantID), n)
	}

	q := rows.query(t)
	writes := []struct{ verb, sql string }{
		// SET <column> = <column> changes nothing, so only row visibility and grants decide the outcome.
		{"update", fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", q.table, q.columns[0], q.columns[0], q.where)},
		{"delete", fmt.Sprintf("DELETE FROM %s WHERE %s", q.table, q.where)},
	}
	for _, w := range writes {
		tag, err := d.exec(t.Context(), tenantID, w.sql, q.args...)
		switch {
		case IsDenied(err):
		case err != nil:
			t.Errorf("%s as %s: %v", w.sql, tenantLabel(tenantID), err)
		case tag.RowsAffected() != 0:
			t.Errorf("%s: %s can %s %d row(s)", rows, tenantLabel(tenantID), w.verb, tag.RowsAffected())
		}
	}
}

// AssertDenied fails the test unless PostgreSQL rejects the statement for tenantID, because the row would
// violate a row-level security policy or because the runtime role lacks the table privilege.
func (d *Database) AssertDenied(t testing.TB, tenantID uuid.UUID, sql string, args ...any) {
	t.Helper()
	if _, err := d.exec(t.Context(), tenantID, sql, args...); !IsDenied(err) {
		t.Errorf("%s as %s: error = %v, want a row-level security or table privilege denial", sql,
			tenantLabel(tenantID), err)
	}
}

// deniedMessages start the messages that PostgreSQL raises with SQLSTATE 42501 for a row that violates a
// row-level security policy and for a missing privilege on a table or view. The test container runs with
// the default locale, so messages are in English.
var deniedMessages = []string{
	"new row violates row-level security policy",
	"permission denied for table",
	"permission denied for view",
}

// IsDenied reports whether PostgreSQL rejected a statement because of a row-level security policy or a
// missing table privilege. Other permission errors share SQLSTATE 42501, such as a missing EXECUTE grant on
// a function that a policy calls; they do not count, so they fail assertions instead of passing as denials.
func IsDenied(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		return false
	}
	return slices.ContainsFunc(deniedMessages, func(prefix string) bool {
		return strings.HasPrefix(pgErr.Message, prefix)
	})
}

// errRollback ends a harness transaction without committing it, so assertions never change data.
var errRollback = errors.New("roll back")

type txRunner func(ctx context.Context, fn func(pgx.Tx) error) error

// inTx returns a runner for transactions of the runtime role, scoped to tenantID unless it is NoTenant.
func (d *Database) inTx(tenantID uuid.UUID) txRunner {
	return func(ctx context.Context, fn func(pgx.Tx) error) error {
		if tenantID == NoTenant {
			return pgx.BeginFunc(ctx, d.App, fn)
		}
		return d.Tenancy.WithTenantTx(ctx, tenantID, fn)
	}
}

func (d *Database) asOwner(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.Owner, fn)
}

// rolledBack runs fn through run and rolls the transaction back.
func rolledBack(ctx context.Context, run txRunner, fn func(pgx.Tx) error) error {
	err := run(ctx, func(tx pgx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errRollback
	})
	if errors.Is(err, errRollback) {
		return nil
	}
	return err
}

func (d *Database) count(t testing.TB, run txRunner, rows Rows) int64 {
	t.Helper()
	q := rows.query(t)
	var n int64
	err := rolledBack(t.Context(), run, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", q.table, q.where),
			q.args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s: %v", rows, err)
	}
	return n
}

func (d *Database) exec(ctx context.Context, tenantID uuid.UUID, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := rolledBack(ctx, d.inTx(tenantID), func(tx pgx.Tx) error {
		var err error
		tag, err = tx.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

// query holds the quoted parts of a statement over Rows.
type query struct {
	table   string
	columns []string
	where   string
	args    []any
}

func (r Rows) query(t testing.TB) query {
	t.Helper()
	if len(r.Where) == 0 {
		t.Fatalf("%s: Rows.Where needs at least one column", r.Table)
	}
	q := query{table: pgx.Identifier(strings.Split(r.Table, ".")).Sanitize()}
	conditions := make([]string, 0, len(r.Where))
	for i, column := range slices.Sorted(maps.Keys(r.Where)) {
		q.columns = append(q.columns, pgx.Identifier{column}.Sanitize())
		conditions = append(conditions, fmt.Sprintf("%s = $%d", q.columns[i], i+1))
		q.args = append(q.args, r.Where[column])
	}
	q.where = strings.Join(conditions, " AND ")
	return q
}

func tenantLabel(tenantID uuid.UUID) string {
	if tenantID == NoTenant {
		return "a session without tenant context"
	}
	return "tenant " + tenantID.String()
}
