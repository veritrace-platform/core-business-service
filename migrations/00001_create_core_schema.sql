-- Application schema, runtime grants, and default privileges for the core database.
-- Roles veritrace_core_owner (runs this) and veritrace_core_app are created by infrastructure bootstrap.

-- +goose Up
CREATE SCHEMA core;
REVOKE ALL ON SCHEMA core FROM PUBLIC;
GRANT USAGE ON SCHEMA core TO veritrace_core_app;

-- The runtime role gets DML only; TRUNCATE would bypass row-level security.
ALTER DEFAULT PRIVILEGES IN SCHEMA core
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO veritrace_core_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA core
    GRANT USAGE, SELECT ON SEQUENCES TO veritrace_core_app;

-- Functions are not executable by default; each one is granted explicitly.
ALTER DEFAULT PRIVILEGES
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- +goose Down
ALTER DEFAULT PRIVILEGES
    GRANT EXECUTE ON FUNCTIONS TO PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA core
    REVOKE USAGE, SELECT ON SEQUENCES FROM veritrace_core_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA core
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM veritrace_core_app;
DROP SCHEMA core;
