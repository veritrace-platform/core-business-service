-- name: ListLocations :many
SELECT
    id,
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters,
    is_headquarters,
    is_active,
    created_at,
    updated_at
FROM core.locations
WHERE (sqlc.narg(is_active)::boolean IS NULL OR is_active = sqlc.narg(is_active))
  AND (sqlc.narg(after)::uuid IS NULL OR id < sqlc.narg(after))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetLocation :one
SELECT
    id,
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters,
    is_headquarters,
    is_active,
    created_at,
    updated_at
FROM core.locations
WHERE id = sqlc.arg(id);

-- name: CreateLocation :one
INSERT INTO core.locations (
    tenant_id,
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(gln),
    sqlc.arg(name),
    sqlc.arg(address),
    sqlc.arg(city),
    sqlc.arg(country_code),
    sqlc.arg(latitude),
    sqlc.arg(longitude),
    sqlc.arg(geo_fence_radius_meters)
)
RETURNING
    id,
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters,
    is_headquarters,
    is_active,
    created_at,
    updated_at;

-- name: UpdateLocation :one
UPDATE core.locations
SET name = coalesce(sqlc.narg(name), name),
    address = coalesce(sqlc.narg(address), address),
    city = coalesce(sqlc.narg(city), city),
    country_code = coalesce(sqlc.narg(country_code), country_code),
    latitude = coalesce(sqlc.narg(latitude), latitude),
    longitude = coalesce(sqlc.narg(longitude), longitude),
    geo_fence_radius_meters = coalesce(sqlc.narg(geo_fence_radius_meters), geo_fence_radius_meters),
    is_active = coalesce(sqlc.narg(is_active), is_active)
WHERE id = sqlc.arg(id)
RETURNING
    id,
    gln,
    name,
    address,
    city,
    country_code,
    latitude,
    longitude,
    geo_fence_radius_meters,
    is_headquarters,
    is_active,
    created_at,
    updated_at;

-- name: GetCompanyPrefix :one
SELECT gs1_company_prefix
FROM core.tenants
WHERE id = sqlc.arg(id);
