-- Products (data-model.md §3.2): a tenant's trade items, identified by GTIN-14, with the temperature range
-- that the cold chain must hold.

-- +goose Up
CREATE TABLE core.products (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    gtin char(14) NOT NULL,
    name text NOT NULL,
    description text,
    min_temp_celsius numeric(5, 2) NOT NULL,
    max_temp_celsius numeric(5, 2) NOT NULL,
    is_active boolean NOT NULL DEFAULT TRUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT products_tenant_id_id_key UNIQUE (tenant_id, id),
    CONSTRAINT products_gtin_key UNIQUE (gtin),
    CONSTRAINT products_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES core.tenants (id),
    CONSTRAINT products_gtin_check CHECK (gtin ~ '^[0-9]{14}$'),
    CONSTRAINT products_name_check CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT products_description_check CHECK (char_length(description) BETWEEN 1 AND 1000),
    CONSTRAINT products_min_temp_celsius_check CHECK (min_temp_celsius BETWEEN -50 AND 80),
    CONSTRAINT products_max_temp_celsius_check CHECK (max_temp_celsius BETWEEN -50 AND 80),
    CONSTRAINT products_temperature_range_check CHECK (min_temp_celsius < max_temp_celsius)
);

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.products
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

ALTER TABLE core.products ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.products
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- Products are deactivated, never deleted: lots keep referring to them.
REVOKE DELETE ON core.products FROM veritrace_core_app;

-- +goose Down
DROP TABLE core.products;
