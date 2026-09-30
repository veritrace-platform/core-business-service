-- Tenant context for row-level security (ADR-0002).
-- Tenant-scoped transactions start with SELECT set_config('app.current_tenant_id', $1, true). Policies compare
-- rows against (SELECT core.current_tenant_id()), so the function runs once per query rather than once per row.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION core.current_tenant_id()
    RETURNS uuid
    LANGUAGE sql
    STABLE
    PARALLEL SAFE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    -- A missing or empty setting yields NULL, which matches no row, so policies fail closed.
    RETURN NULLIF(current_setting('app.current_tenant_id', TRUE), '')::uuid;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.current_tenant_id() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.current_tenant_id() TO veritrace_core_app;

-- +goose Down
DROP FUNCTION core.current_tenant_id();
