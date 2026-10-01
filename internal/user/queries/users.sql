-- name: ListUsers :many
SELECT
    id,
    email,
    full_name,
    phone,
    role,
    is_active,
    last_login_at,
    created_at,
    updated_at
FROM core.users
WHERE (sqlc.narg(role)::text IS NULL OR role = sqlc.narg(role))
  AND (sqlc.narg(is_active)::boolean IS NULL OR is_active = sqlc.narg(is_active))
  AND (sqlc.narg(after)::uuid IS NULL OR id < sqlc.narg(after))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetUser :one
SELECT
    id,
    email,
    full_name,
    phone,
    role,
    is_active,
    last_login_at,
    created_at,
    updated_at
FROM core.users
WHERE id = sqlc.arg(id);

-- name: CreateUser :one
INSERT INTO core.users (
    tenant_id,
    email,
    password_hash,
    full_name,
    phone,
    role
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(email),
    sqlc.arg(password_hash),
    sqlc.arg(full_name),
    sqlc.narg(phone),
    sqlc.arg(role)
)
RETURNING
    id,
    email,
    full_name,
    phone,
    role,
    is_active,
    last_login_at,
    created_at,
    updated_at;

-- name: UpdateUser :one
UPDATE core.users
SET full_name = coalesce(sqlc.narg(full_name), full_name),
    phone = CASE WHEN sqlc.arg(set_phone)::boolean THEN sqlc.narg(phone) ELSE phone END,
    role = coalesce(sqlc.narg(role), role),
    is_active = coalesce(sqlc.narg(is_active), is_active)
WHERE id = sqlc.arg(id)
RETURNING
    id,
    email,
    full_name,
    phone,
    role,
    is_active,
    last_login_at,
    created_at,
    updated_at;

-- name: LockTenant :one
SELECT id
FROM core.tenants
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: RevokeUserSessions :exec
UPDATE core.auth_sessions
SET revoked_at = sqlc.arg(revoked_at)
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;
