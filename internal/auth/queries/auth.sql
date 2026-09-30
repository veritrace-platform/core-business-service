-- name: FindLoginUser :one
SELECT
    user_id,
    tenant_id,
    password_hash,
    role,
    is_active
FROM core.find_login_user(sqlc.arg(email));

-- name: FindAuthSession :one
SELECT
    session_id,
    family_id,
    tenant_id,
    user_id
FROM core.find_auth_session(sqlc.arg(token_hash));

-- name: CreateSession :one
INSERT INTO core.auth_sessions (
    tenant_id,
    user_id,
    family_id,
    token_hash,
    expires_at,
    user_agent
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(user_id),
    sqlc.arg(family_id),
    sqlc.arg(token_hash),
    sqlc.arg(expires_at),
    sqlc.narg(user_agent)
)
RETURNING id;

-- name: LockSession :one
SELECT
    id,
    family_id,
    user_id,
    expires_at,
    rotated_at,
    revoked_at
FROM core.auth_sessions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: GetSessionFamily :one
SELECT family_id
FROM core.auth_sessions
WHERE id = sqlc.arg(id);

-- name: MarkSessionRotated :exec
UPDATE core.auth_sessions
SET rotated_at = sqlc.arg(rotated_at)
WHERE id = sqlc.arg(id);

-- name: RevokeSessionFamily :exec
UPDATE core.auth_sessions
SET revoked_at = sqlc.arg(revoked_at)
WHERE family_id = sqlc.arg(family_id)
  AND revoked_at IS NULL;

-- name: RevokeOtherUserSessions :exec
UPDATE core.auth_sessions
SET revoked_at = sqlc.arg(revoked_at)
WHERE user_id = sqlc.arg(user_id)
  AND family_id <> sqlc.arg(keep_family_id)
  AND revoked_at IS NULL;

-- name: DeleteExpiredFamilySessions :exec
DELETE FROM core.auth_sessions
WHERE family_id = sqlc.arg(family_id)
  AND expires_at < sqlc.arg(before);

-- name: DeleteExpiredUserSessions :exec
DELETE FROM core.auth_sessions
WHERE user_id = sqlc.arg(user_id)
  AND expires_at < sqlc.arg(before);

-- name: RecordLogin :exec
UPDATE core.users
SET last_login_at = sqlc.arg(logged_in_at)
WHERE id = sqlc.arg(id);

-- name: SetPasswordHash :exec
UPDATE core.users
SET password_hash = sqlc.arg(password_hash)
WHERE id = sqlc.arg(id);

-- name: GetPasswordHash :one
SELECT password_hash
FROM core.users
WHERE id = sqlc.arg(id);

-- name: GetMe :one
SELECT
    u.id,
    u.email,
    u.full_name,
    u.phone,
    u.role,
    u.is_active,
    u.last_login_at,
    u.created_at,
    u.updated_at,
    t.id AS tenant_id,
    t.code AS tenant_code,
    t.legal_name AS tenant_legal_name,
    t.gs1_company_prefix AS tenant_gs1_company_prefix,
    t.status AS tenant_status
FROM core.users u
JOIN core.tenants t ON t.id = u.tenant_id
WHERE u.id = sqlc.arg(id);
