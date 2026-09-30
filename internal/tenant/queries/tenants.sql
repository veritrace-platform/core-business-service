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
