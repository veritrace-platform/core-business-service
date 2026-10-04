-- Directory lookups (data-model.md §3.5): any tenant resolves a GLN or a tenant code to public fields only.
-- Inactive locations and suspended tenants are not listed.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION core.lookup_location_by_gln(
    p_gln text,
    OUT location_id uuid,
    OUT gln text,
    OUT name text,
    OUT address text,
    OUT city text,
    OUT country_code text,
    OUT latitude numeric,
    OUT longitude numeric,
    OUT geo_fence_radius_meters integer,
    OUT tenant_id uuid,
    OUT tenant_code text,
    OUT tenant_legal_name text
)
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
BEGIN ATOMIC
    SELECT
        l.id,
        l.gln::text,
        l.name,
        l.address,
        l.city,
        l.country_code::text,
        l.latitude,
        l.longitude,
        l.geo_fence_radius_meters,
        t.id,
        t.code,
        t.legal_name
    FROM core.locations l
    JOIN core.tenants t ON t.id = l.tenant_id
    WHERE l.gln = p_gln
      AND l.is_active
      AND t.status = 'ACTIVE';
END;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.lookup_location_by_gln(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.lookup_location_by_gln(text) TO veritrace_core_app;

-- +goose StatementBegin
CREATE FUNCTION core.lookup_tenant_by_code(
    p_code text,
    OUT tenant_id uuid,
    OUT code text,
    OUT legal_name text
)
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
BEGIN ATOMIC
    SELECT t.id, t.code, t.legal_name
    FROM core.tenants t
    WHERE t.code = p_code
      AND t.status = 'ACTIVE';
END;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.lookup_tenant_by_code(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.lookup_tenant_by_code(text) TO veritrace_core_app;

-- +goose Down
DROP FUNCTION core.lookup_tenant_by_code(text);
DROP FUNCTION core.lookup_location_by_gln(text);
