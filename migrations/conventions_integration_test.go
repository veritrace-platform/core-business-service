//go:build integration

package migrations_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// conventions holds the deliberate exceptions to the schema defaults.
type conventions struct {
	withoutRLS []string // tables exempt from row-level security
	appendOnly []string // tables the runtime role may read and insert into, but never update or delete
}

// schemaConventions are the exceptions that the design documents decide.
var schemaConventions = conventions{
	// data-model.md §3.3: the outbox is internal and never exposed through the API.
	withoutRLS: []string{"outbox"},
	// ADR-0002: the shipment event log and the inventory ledger are append-only.
	appendOnly: []string{"shipment_events", "inventory_movements"},
}

// conventionChecks return one message per violation. They take the named arguments owner_role, app_role,
// without_rls, and append_only.
var conventionChecks = []string{
	// Tenant data is protected by row-level security (ADR-0002).
	`SELECT format('table core.%I: row-level security is not enabled', c.relname)
	FROM pg_class c
	WHERE c.relnamespace = 'core'::regnamespace
	  AND c.relkind IN ('r', 'p')
	  AND NOT c.relrowsecurity
	  AND c.relname <> ALL (@without_rls::text[])`,

	// A view reads its tables with the privileges of its owner, which bypasses their policies, unless it is
	// declared security_invoker. Materialized views cannot have policies at all.
	`SELECT format('view core.%I: security_invoker is not enabled', c.relname)
	FROM pg_class c
	WHERE c.relnamespace = 'core'::regnamespace
	  AND c.relkind = 'v'
	  AND NOT EXISTS (
	      SELECT 1
	      FROM pg_options_to_table(c.reloptions) o
	      WHERE o.option_name = 'security_invoker' AND o.option_value::boolean)
	UNION ALL
	SELECT format('materialized view core.%I: materialized views bypass row-level security', c.relname)
	FROM pg_class c
	WHERE c.relnamespace = 'core'::regnamespace
	  AND c.relkind = 'm'`,

	// Policies apply to the runtime role only, so any other role sees nothing.
	`SELECT format('policy %I on core.%I: applies to %s instead of %s only',
	               p.policyname, p.tablename, p.roles, @app_role::text)
	FROM pg_policies p
	WHERE p.schemaname = 'core'
	  AND p.roles <> ARRAY[@app_role::text::name]`,

	// (SELECT core.current_tenant_id()) runs once per query; a bare call runs once per row.
	`SELECT format('policy %I on core.%I: %s calls current_tenant_id() outside (SELECT ...)',
	               p.policyname, p.tablename, e.clause)
	FROM pg_policies p
	CROSS JOIN LATERAL (VALUES ('USING', p.qual), ('WITH CHECK', p.with_check)) e (clause, expression)
	WHERE p.schemaname = 'core'
	  AND regexp_count(e.expression, 'current_tenant_id\(\)')
	      <> regexp_count(e.expression, 'SELECT (core\.)?current_tenant_id\(\)')`,

	// The runtime role has DML only; TRUNCATE in particular bypasses row-level security (ADR-0002).
	`SELECT format('table core.%I: the runtime role has %s', c.relname, p.privilege)
	FROM pg_class c
	CROSS JOIN unnest(ARRAY['TRUNCATE', 'REFERENCES', 'TRIGGER', 'MAINTAIN']) p (privilege)
	WHERE c.relnamespace = 'core'::regnamespace
	  AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
	  AND has_table_privilege(@app_role::text::name, c.oid, p.privilege)
	UNION ALL
	SELECT format('table core.%I: append-only, but the runtime role has %s', c.relname, p.privilege)
	FROM pg_class c
	CROSS JOIN unnest(ARRAY['UPDATE', 'DELETE']) p (privilege)
	WHERE c.relnamespace = 'core'::regnamespace
	  AND c.relname = ANY (@append_only::text[])
	  AND has_table_privilege(@app_role::text::name, c.oid, p.privilege)`,

	// The runtime role owns nothing, so it can neither alter objects nor grant privileges on them.
	`SELECT format('%s %s: owned by the runtime role', o.kind, o.name)
	FROM (
	    SELECT 'relation', c.oid::regclass::text FROM pg_class c WHERE c.relowner = @app_role::text::regrole
	    UNION ALL
	    SELECT 'function', p.oid::regprocedure::text FROM pg_proc p WHERE p.proowner = @app_role::text::regrole
	    UNION ALL
	    SELECT 'schema', n.nspname::text FROM pg_namespace n WHERE n.nspowner = @app_role::text::regrole
	    UNION ALL
	    SELECT 'type', t.oid::regtype::text FROM pg_type t WHERE t.typowner = @app_role::text::regrole
	) o (kind, name)`,

	// Functions (data-model.md §3.5) belong to the owner role and only the runtime role may execute them.
	// SECURITY DEFINER functions pin search_path, so callers cannot substitute objects they resolve.
	`SELECT format('function core.%I(%s): owned by %s instead of %s',
	               p.proname, pg_get_function_identity_arguments(p.oid), p.proowner::regrole, @owner_role::text)
	FROM pg_proc p
	WHERE p.pronamespace = 'core'::regnamespace
	  AND p.proowner <> @owner_role::text::regrole
	UNION ALL
	SELECT format('function core.%I(%s): executable by %s',
	              p.proname, pg_get_function_identity_arguments(p.oid),
	              CASE a.grantee WHEN 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END)
	FROM pg_proc p
	CROSS JOIN LATERAL aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
	WHERE p.pronamespace = 'core'::regnamespace
	  AND a.privilege_type = 'EXECUTE'
	  AND a.grantee NOT IN (p.proowner, @app_role::text::regrole)
	UNION ALL
	SELECT format('function core.%I(%s): SECURITY DEFINER without SET search_path = pg_catalog, pg_temp',
	              p.proname, pg_get_function_identity_arguments(p.oid))
	FROM pg_proc p
	WHERE p.pronamespace = 'core'::regnamespace
	  AND p.prosecdef
	  AND NOT coalesce('search_path=pg_catalog, pg_temp' = ANY (p.proconfig), FALSE)`,
}

// brokenDDL breaks every convention once. The checks must report exactly brokenDDLViolations.
const brokenDDL = `
CREATE TABLE core.probe_without_rls (id uuid PRIMARY KEY);
CREATE TABLE core.probe_append_only (id uuid PRIMARY KEY);
ALTER TABLE core.probe_append_only ENABLE ROW LEVEL SECURITY;
CREATE POLICY per_row ON core.probe_append_only TO veritrace_core_app USING (id = core.current_tenant_id());
CREATE POLICY for_everyone ON core.probe_append_only USING (TRUE);
GRANT TRUNCATE ON core.probe_append_only TO veritrace_core_app;
CREATE VIEW core.probe_view AS SELECT id FROM core.probe_append_only;
CREATE MATERIALIZED VIEW core.probe_matview AS SELECT id FROM core.probe_append_only;
CREATE FUNCTION core.probe_definer() RETURNS integer LANGUAGE sql SECURITY DEFINER RETURN 1;
GRANT EXECUTE ON FUNCTION core.probe_definer() TO PUBLIC;
`

var brokenDDLViolations = []string{
	"table core.probe_without_rls: row-level security is not enabled",
	"view core.probe_view: security_invoker is not enabled",
	"materialized view core.probe_matview: materialized views bypass row-level security",
	"policy for_everyone on core.probe_append_only: applies to {public} instead of veritrace_core_app only",
	"policy per_row on core.probe_append_only: USING calls current_tenant_id() outside (SELECT ...)",
	"table core.probe_append_only: the runtime role has TRUNCATE",
	"table core.probe_append_only: append-only, but the runtime role has UPDATE",
	"table core.probe_append_only: append-only, but the runtime role has DELETE",
	"function core.probe_definer(): executable by PUBLIC",
	"function core.probe_definer(): SECURITY DEFINER without SET search_path = pg_catalog, pg_temp",
}

func TestSchemaConventions(t *testing.T) {
	db := tenancytest.Start(t)

	t.Run("migrations follow the conventions", func(t *testing.T) {
		for _, violation := range violations(t, db.Owner, schemaConventions) {
			t.Error(violation)
		}
	})

	t.Run("checks report every violation", func(t *testing.T) {
		tx, err := db.Owner.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		if _, err := tx.Exec(t.Context(), brokenDDL); err != nil {
			t.Fatalf("create broken objects: %v", err)
		}

		got := violations(t, tx, conventions{appendOnly: []string{"probe_append_only"}})
		slices.Sort(got)
		want := slices.Sorted(slices.Values(brokenDDLViolations))
		if !slices.Equal(got, want) {
			t.Errorf("violations:\n got %q\nwant %q", got, want)
		}
	})
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func violations(t *testing.T, q querier, c conventions) []string {
	t.Helper()
	args := pgx.NamedArgs{
		"owner_role": tenancytest.OwnerRole,
		"app_role":   tenancytest.AppRole,
		// Empty rather than nil: x <> ALL (NULL) is never true.
		"without_rls": append([]string{}, c.withoutRLS...),
		"append_only": append([]string{}, c.appendOnly...),
	}
	var found []string
	for _, check := range conventionChecks {
		rows, err := q.Query(t.Context(), check, args)
		if err != nil {
			t.Fatalf("run check: %v\n%s", err, check)
		}
		messages, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read check results: %v\n%s", err, check)
		}
		found = append(found, messages...)
	}
	return found
}
