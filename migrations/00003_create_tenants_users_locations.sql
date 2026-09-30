-- Tenants, users, and locations (data-model.md §3.1–3.2), and tenant registration (§3.5).

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION core.set_updated_at()
    RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Trigger functions run through their triggers; nobody executes them directly.
REVOKE ALL ON FUNCTION core.set_updated_at() FROM PUBLIC;

CREATE TABLE core.tenants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    code text NOT NULL,
    legal_name text NOT NULL,
    tax_code text NOT NULL,
    gs1_company_prefix text NOT NULL,
    sscc_extension_digit smallint NOT NULL DEFAULT 0,
    sscc_next_serial bigint NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenants_code_key UNIQUE (code),
    CONSTRAINT tenants_tax_code_key UNIQUE (tax_code),
    CONSTRAINT tenants_gs1_company_prefix_key UNIQUE (gs1_company_prefix),
    CONSTRAINT tenants_code_check CHECK (code ~ '^[A-Z0-9_]{3,32}$'),
    CONSTRAINT tenants_legal_name_check CHECK (char_length(legal_name) BETWEEN 1 AND 255),
    CONSTRAINT tenants_tax_code_check CHECK (tax_code ~ '^[0-9]{10}(-[0-9]{3})?$'),
    CONSTRAINT tenants_gs1_company_prefix_check CHECK (gs1_company_prefix ~ '^[0-9]{6,10}$'),
    CONSTRAINT tenants_sscc_extension_digit_check CHECK (sscc_extension_digit BETWEEN 0 AND 9),
    CONSTRAINT tenants_sscc_next_serial_check CHECK (sscc_next_serial >= 1),
    CONSTRAINT tenants_status_check CHECK (status IN ('ACTIVE', 'SUSPENDED'))
);

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.tenants
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

ALTER TABLE core.tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_tenant ON core.tenants
    TO veritrace_core_app
    USING (id = (SELECT core.current_tenant_id()))
    WITH CHECK (id = (SELECT core.current_tenant_id()));

-- Tenants are created only by core.register_tenant and are never deleted.
REVOKE INSERT, DELETE ON core.tenants FROM veritrace_core_app;

CREATE TABLE core.users (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    full_name text NOT NULL,
    phone text,
    role text NOT NULL,
    is_active boolean NOT NULL DEFAULT TRUE,
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- The target of composite foreign keys, which keep referencing rows in the same tenant.
    CONSTRAINT users_tenant_id_id_key UNIQUE (tenant_id, id),
    CONSTRAINT users_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES core.tenants (id),
    CONSTRAINT users_email_check CHECK (char_length(email) BETWEEN 3 AND 254),
    CONSTRAINT users_password_hash_check CHECK (password_hash LIKE '$argon2id$%'),
    CONSTRAINT users_full_name_check CHECK (char_length(full_name) BETWEEN 1 AND 255),
    CONSTRAINT users_phone_check CHECK (char_length(phone) BETWEEN 6 AND 32),
    CONSTRAINT users_role_check CHECK (role IN ('ADMIN', 'WAREHOUSE_MANAGER', 'DRIVER', 'INSPECTOR'))
);

-- Email addresses are unique across tenants, ignoring case.
CREATE UNIQUE INDEX users_email_key ON core.users (lower(email));

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.users
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

ALTER TABLE core.users ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.users
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- Users are deactivated, never deleted: events and records keep referring to them.
REVOKE DELETE ON core.users FROM veritrace_core_app;

CREATE TABLE core.locations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    gln char(13) NOT NULL,
    name text NOT NULL,
    address text NOT NULL,
    city text NOT NULL,
    country_code char(2) NOT NULL DEFAULT 'VN',
    latitude numeric(9, 6) NOT NULL,
    longitude numeric(9, 6) NOT NULL,
    geo_fence_radius_meters integer NOT NULL DEFAULT 200,
    is_headquarters boolean NOT NULL DEFAULT FALSE,
    is_active boolean NOT NULL DEFAULT TRUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT locations_tenant_id_id_key UNIQUE (tenant_id, id),
    CONSTRAINT locations_gln_key UNIQUE (gln),
    CONSTRAINT locations_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES core.tenants (id),
    CONSTRAINT locations_gln_check CHECK (gln ~ '^[0-9]{13}$'),
    CONSTRAINT locations_name_check CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT locations_address_check CHECK (char_length(address) BETWEEN 1 AND 500),
    CONSTRAINT locations_city_check CHECK (char_length(city) BETWEEN 1 AND 100),
    CONSTRAINT locations_country_code_check CHECK (country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT locations_latitude_check CHECK (latitude BETWEEN -90 AND 90),
    CONSTRAINT locations_longitude_check CHECK (longitude BETWEEN -180 AND 180),
    CONSTRAINT locations_geo_fence_radius_meters_check CHECK (geo_fence_radius_meters BETWEEN 50 AND 5000)
);

-- At most one headquarters per tenant.
CREATE UNIQUE INDEX locations_headquarters_key ON core.locations (tenant_id) WHERE is_headquarters;

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.locations
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

ALTER TABLE core.locations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.locations
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- Locations are deactivated, never deleted: lots and shipments keep referring to them.
REVOKE DELETE ON core.locations FROM veritrace_core_app;

-- Registration creates a tenant with its headquarters and first admin before any tenant context exists. The
-- caller validates every value; unique violations name the constraint that failed.
-- +goose StatementBegin
CREATE FUNCTION core.register_tenant(
    p_code text,
    p_legal_name text,
    p_tax_code text,
    p_gs1_company_prefix text,
    p_headquarters_gln text,
    p_headquarters_name text,
    p_headquarters_address text,
    p_headquarters_city text,
    p_headquarters_country_code text,
    p_headquarters_latitude numeric,
    p_headquarters_longitude numeric,
    p_headquarters_geo_fence_radius_meters integer,
    p_admin_email text,
    p_admin_password_hash text,
    p_admin_full_name text,
    p_admin_phone text,
    OUT tenant_id uuid,
    OUT headquarters_location_id uuid,
    OUT admin_user_id uuid,
    OUT created_at timestamptz
)
    LANGUAGE plpgsql
    VOLATILE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    -- Registrations run one at a time, so two overlapping company prefixes cannot both pass the check below.
    -- The lock key is reserved for this function.
    PERFORM pg_advisory_xact_lock(4715400001);

    -- GS1 assigns company prefixes so that none extends another; an overlap would share key space.
    IF EXISTS (
        SELECT 1
        FROM core.tenants t
        WHERE starts_with(t.gs1_company_prefix, p_gs1_company_prefix)
           OR starts_with(p_gs1_company_prefix, t.gs1_company_prefix)
    ) THEN
        RAISE unique_violation USING
            MESSAGE = 'company prefix overlaps a registered company prefix',
            CONSTRAINT = 'tenants_gs1_company_prefix_key';
    END IF;

    -- Aliases keep the table columns apart from the output parameters of the same names.
    INSERT INTO core.tenants AS t (code, legal_name, tax_code, gs1_company_prefix)
    VALUES (p_code, p_legal_name, p_tax_code, p_gs1_company_prefix)
    RETURNING t.id, t.created_at
    INTO tenant_id, created_at;

    INSERT INTO core.locations AS l (
        tenant_id,
        gln,
        name,
        address,
        city,
        country_code,
        latitude,
        longitude,
        geo_fence_radius_meters,
        is_headquarters
    )
    VALUES (
        register_tenant.tenant_id,
        p_headquarters_gln,
        p_headquarters_name,
        p_headquarters_address,
        p_headquarters_city,
        p_headquarters_country_code,
        p_headquarters_latitude,
        p_headquarters_longitude,
        p_headquarters_geo_fence_radius_meters,
        TRUE
    )
    RETURNING l.id
    INTO headquarters_location_id;

    INSERT INTO core.users AS u (tenant_id, email, password_hash, full_name, phone, role)
    VALUES (register_tenant.tenant_id, p_admin_email, p_admin_password_hash, p_admin_full_name, p_admin_phone, 'ADMIN')
    RETURNING u.id
    INTO admin_user_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.register_tenant(
    text, text, text, text, text, text, text, text, text, numeric, numeric, integer, text, text, text, text
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.register_tenant(
    text, text, text, text, text, text, text, text, text, numeric, numeric, integer, text, text, text, text
) TO veritrace_core_app;

-- +goose Down
DROP FUNCTION core.register_tenant(
    text, text, text, text, text, text, text, text, text, numeric, numeric, integer, text, text, text, text
);
DROP TABLE core.locations;
DROP TABLE core.users;
DROP TABLE core.tenants;
DROP FUNCTION core.set_updated_at();
