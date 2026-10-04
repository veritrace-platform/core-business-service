-- name: ListLots :many
SELECT
    id,
    tenant_id,
    product_id,
    gtin,
    product_name,
    min_temp_celsius,
    max_temp_celsius,
    lot_number,
    production_date,
    expiration_date,
    quantity_commissioned,
    commissioned_location_id,
    status,
    recalled_at,
    created_at,
    updated_at
FROM core.lots
WHERE (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL OR id < sqlc.narg(after))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetLot :one
SELECT
    id,
    tenant_id,
    product_id,
    gtin,
    product_name,
    min_temp_celsius,
    max_temp_celsius,
    lot_number,
    production_date,
    expiration_date,
    quantity_commissioned,
    commissioned_location_id,
    status,
    recalled_at,
    created_at,
    updated_at
FROM core.lots
WHERE id = sqlc.arg(id);

-- name: LockProduct :one
-- Commissioning locks the product until it commits, so the product is neither changed nor deactivated between
-- the checks and the new lot.
SELECT
    id,
    gtin,
    name,
    min_temp_celsius,
    max_temp_celsius,
    is_active
FROM core.products
WHERE id = sqlc.arg(id)
FOR SHARE;

-- name: LockLocation :one
-- Commissioning locks the location until it commits, so the location is not deactivated meanwhile.
SELECT
    id,
    is_active
FROM core.locations
WHERE id = sqlc.arg(id)
FOR SHARE;

-- name: CreateLot :one
INSERT INTO core.lots (
    tenant_id,
    product_id,
    gtin,
    product_name,
    min_temp_celsius,
    max_temp_celsius,
    lot_number,
    production_date,
    expiration_date,
    quantity_commissioned,
    commissioned_location_id,
    created_by
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(product_id),
    sqlc.arg(gtin),
    sqlc.arg(product_name),
    sqlc.arg(min_temp_celsius),
    sqlc.arg(max_temp_celsius),
    sqlc.arg(lot_number),
    sqlc.arg(production_date),
    sqlc.arg(expiration_date),
    sqlc.arg(quantity_commissioned),
    sqlc.arg(commissioned_location_id),
    sqlc.arg(created_by)
)
RETURNING
    id,
    tenant_id,
    product_id,
    gtin,
    product_name,
    min_temp_celsius,
    max_temp_celsius,
    lot_number,
    production_date,
    expiration_date,
    quantity_commissioned,
    commissioned_location_id,
    status,
    recalled_at,
    created_at,
    updated_at;
