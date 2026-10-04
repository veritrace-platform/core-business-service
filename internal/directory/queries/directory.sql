-- name: LookupLocationByGLN :one
SELECT
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters,
    tenant_id,
    tenant_code,
    tenant_legal_name
FROM core.lookup_location_by_gln(sqlc.arg(gln));

-- name: LookupTenantByCode :one
SELECT
    tenant_id,
    code,
    legal_name
FROM core.lookup_tenant_by_code(sqlc.arg(code));
