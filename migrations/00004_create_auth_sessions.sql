-- Refresh sessions (data-model.md §3.1) and the lookups that run before a tenant context exists (§3.5).

-- +goose Up
CREATE TABLE core.auth_sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    family_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    rotated_at timestamptz,
    revoked_at timestamptz,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT auth_sessions_user_id_fkey FOREIGN KEY (tenant_id, user_id) REFERENCES core.users (tenant_id, id),
    CONSTRAINT auth_sessions_token_hash_check CHECK (octet_length(token_hash) = 32),
    CONSTRAINT auth_sessions_user_agent_check CHECK (char_length(user_agent) <= 512)
);

CREATE INDEX idx_auth_sessions_family_id ON core.auth_sessions (family_id);
CREATE INDEX idx_auth_sessions_tenant_id_user_id ON core.auth_sessions (tenant_id, user_id);

ALTER TABLE core.auth_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.auth_sessions
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- Login finds the account by email before any tenant context exists. is_active is false when the user or its
-- tenant cannot sign in. No row matches an unknown email, and then every column is NULL.
-- +goose StatementBegin
CREATE FUNCTION core.find_login_user(
    p_email text,
    OUT user_id uuid,
    OUT tenant_id uuid,
    OUT password_hash text,
    OUT role text,
    OUT is_active boolean
)
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
BEGIN ATOMIC
    SELECT u.id, u.tenant_id, u.password_hash, u.role, u.is_active AND t.status = 'ACTIVE'
    FROM core.users u
    JOIN core.tenants t ON t.id = u.tenant_id
    WHERE lower(u.email) = lower(p_email);
END;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.find_login_user(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.find_login_user(text) TO veritrace_core_app;

-- Refresh and logout find a session by the hash of its token before any tenant context exists. Everything
-- else about the session is read inside the tenant's transaction.
-- +goose StatementBegin
CREATE FUNCTION core.find_auth_session(
    p_token_hash bytea,
    OUT session_id uuid,
    OUT family_id uuid,
    OUT tenant_id uuid,
    OUT user_id uuid
)
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
BEGIN ATOMIC
    SELECT s.id, s.family_id, s.tenant_id, s.user_id
    FROM core.auth_sessions s
    WHERE s.token_hash = p_token_hash;
END;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.find_auth_session(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.find_auth_session(bytea) TO veritrace_core_app;

-- +goose Down
DROP FUNCTION core.find_auth_session(bytea);
DROP FUNCTION core.find_login_user(text);
DROP TABLE core.auth_sessions;
