-- name: ListProducts :many
SELECT
    id,
    gtin,
    name,
    description,
    min_temp_celsius,
    max_temp_celsius,
    is_active,
    created_at,
    updated_at
FROM core.products
WHERE (sqlc.narg(is_active)::boolean IS NULL OR is_active = sqlc.narg(is_active))
  AND (sqlc.narg(pattern)::text IS NULL OR name ILIKE sqlc.narg(pattern) OR gtin LIKE sqlc.narg(pattern))
  AND (sqlc.narg(after)::uuid IS NULL OR id < sqlc.narg(after))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetProduct :one
SELECT
    id,
    gtin,
    name,
    description,
    min_temp_celsius,
    max_temp_celsius,
    is_active,
    created_at,
    updated_at
FROM core.products
WHERE id = sqlc.arg(id);

-- name: CreateProduct :one
INSERT INTO core.products (
    tenant_id,
    gtin,
    name,
    description,
    min_temp_celsius,
    max_temp_celsius
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(gtin),
    sqlc.arg(name),
    sqlc.narg(description),
    sqlc.arg(min_temp_celsius),
    sqlc.arg(max_temp_celsius)
)
RETURNING
    id,
    gtin,
    name,
    description,
    min_temp_celsius,
    max_temp_celsius,
    is_active,
    created_at,
    updated_at;

-- name: UpdateProduct :one
UPDATE core.products
SET name = coalesce(sqlc.narg(name), name),
    description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.narg(description) ELSE description END,
    min_temp_celsius = coalesce(sqlc.narg(min_temp_celsius), min_temp_celsius),
    max_temp_celsius = coalesce(sqlc.narg(max_temp_celsius), max_temp_celsius),
    is_active = coalesce(sqlc.narg(is_active), is_active)
WHERE id = sqlc.arg(id)
RETURNING
    id,
    gtin,
    name,
    description,
    min_temp_celsius,
    max_temp_celsius,
    is_active,
    created_at,
    updated_at;

-- name: GetCompanyPrefix :one
SELECT gs1_company_prefix
FROM core.tenants
WHERE id = sqlc.arg(id);
