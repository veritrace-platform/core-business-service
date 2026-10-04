-- name: RegisterTenant :one
SELECT
    tenant_id,
    headquarters_location_id,
    admin_user_id,
    created_at
FROM core.register_tenant(
    sqlc.arg(code),
    sqlc.arg(legal_name),
    sqlc.arg(tax_code),
    sqlc.arg(gs1_company_prefix),
    sqlc.arg(headquarters_gln),
    sqlc.arg(headquarters_name),
    sqlc.arg(headquarters_address),
    sqlc.arg(headquarters_city),
    sqlc.arg(headquarters_country_code),
    sqlc.arg(headquarters_latitude),
    sqlc.arg(headquarters_longitude),
    sqlc.arg(headquarters_geo_fence_radius_meters),
    sqlc.arg(admin_email),
    sqlc.arg(admin_password_hash),
    sqlc.arg(admin_full_name),
    sqlc.narg(admin_phone)
);

-- name: GetTenant :one
SELECT
    id,
    code,
    legal_name,
    tax_code,
    gs1_company_prefix,
    sscc_extension_digit,
    status,
    created_at,
    updated_at
FROM core.tenants
WHERE id = sqlc.arg(id);

-- name: UpdateTenant :one
-- A new extension digit starts a new SSCC serial space (gs1-identifiers.md §3).
UPDATE core.tenants
SET legal_name = coalesce(sqlc.narg(legal_name), legal_name),
    sscc_next_serial = CASE
        WHEN sqlc.narg(sscc_extension_digit)::smallint <> sscc_extension_digit THEN 1
        ELSE sscc_next_serial
    END,
    sscc_extension_digit = coalesce(sqlc.narg(sscc_extension_digit), sscc_extension_digit)
WHERE id = sqlc.arg(id)
RETURNING
    id,
    code,
    legal_name,
    tax_code,
    gs1_company_prefix,
    sscc_extension_digit,
    status,
    created_at,
    updated_at;

-- name: ExtensionDigitUsed :one
SELECT EXISTS (
    SELECT 1
    FROM core.shipments
    WHERE owner_tenant_id = sqlc.arg(tenant_id)
      AND left(sscc, 1) = sqlc.arg(digit)::text
);
