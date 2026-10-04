-- Lots and the inventory ledger (data-model.md §3.2). A lot belongs to the tenant that commissioned it and is
-- visible to every tenant that holds or held stock of it (ADR-0002). Balances and movements belong to the tenant
-- whose location holds the stock.

-- +goose Up
CREATE TABLE core.lots (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    product_id uuid NOT NULL,
    -- Copied from the product, so holders read them without the owner's catalog (ADR-0002).
    gtin char(14) NOT NULL,
    product_name text NOT NULL,
    min_temp_celsius numeric(5, 2) NOT NULL,
    max_temp_celsius numeric(5, 2) NOT NULL,
    lot_number text NOT NULL,
    production_date date NOT NULL,
    expiration_date date NOT NULL,
    quantity_commissioned integer NOT NULL,
    commissioned_location_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    recalled_at timestamptz,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT lots_tenant_id_id_key UNIQUE (tenant_id, id),
    CONSTRAINT lots_product_id_lot_number_key UNIQUE (product_id, lot_number),
    CONSTRAINT lots_product_id_fkey FOREIGN KEY (tenant_id, product_id) REFERENCES core.products (tenant_id, id),
    CONSTRAINT lots_commissioned_location_id_fkey
        FOREIGN KEY (tenant_id, commissioned_location_id) REFERENCES core.locations (tenant_id, id),
    CONSTRAINT lots_created_by_fkey FOREIGN KEY (tenant_id, created_by) REFERENCES core.users (tenant_id, id),
    CONSTRAINT lots_gtin_check CHECK (gtin ~ '^[0-9]{14}$'),
    CONSTRAINT lots_product_name_check CHECK (char_length(product_name) BETWEEN 1 AND 255),
    CONSTRAINT lots_temperature_range_check CHECK (min_temp_celsius < max_temp_celsius),
    CONSTRAINT lots_lot_number_check CHECK (lot_number ~ '^[0-9A-Za-z._-]{1,20}$'),
    CONSTRAINT lots_expiration_date_check CHECK (expiration_date >= production_date),
    CONSTRAINT lots_quantity_commissioned_check CHECK (quantity_commissioned > 0),
    CONSTRAINT lots_status_check CHECK (status IN ('ACTIVE', 'RECALLED')),
    CONSTRAINT lots_recalled_at_check CHECK ((status = 'RECALLED') = (recalled_at IS NOT NULL))
);

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.lots
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

-- Lots are recalled, never deleted.
REVOKE DELETE ON core.lots FROM veritrace_core_app;

CREATE TABLE core.inventory_balances (
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    lot_id uuid NOT NULL,
    quantity_on_hand integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_balances_pkey PRIMARY KEY (location_id, lot_id),
    CONSTRAINT inventory_balances_location_id_fkey
        FOREIGN KEY (tenant_id, location_id) REFERENCES core.locations (tenant_id, id),
    -- The lot may belong to another tenant: a holder keeps stock of the owner's lot.
    CONSTRAINT inventory_balances_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES core.lots (id),
    -- A balance never goes negative, so two shipments cannot promise the same stock.
    CONSTRAINT inventory_balances_quantity_on_hand_check CHECK (quantity_on_hand >= 0)
);

-- Serves the holder check of lot visibility and the inventory filter by lot.
CREATE INDEX idx_inventory_balances_tenant_id_lot_id ON core.inventory_balances (tenant_id, lot_id);

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.inventory_balances
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

ALTER TABLE core.inventory_balances ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.inventory_balances
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- A balance that reaches zero stays: it is the record that the tenant held the lot.
REVOKE DELETE ON core.inventory_balances FROM veritrace_core_app;

CREATE TABLE core.inventory_movements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    lot_id uuid NOT NULL,
    quantity_delta integer NOT NULL,
    reason text NOT NULL,
    -- References core.shipments, whose migration adds the foreign key.
    shipment_id uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_movements_location_id_fkey
        FOREIGN KEY (tenant_id, location_id) REFERENCES core.locations (tenant_id, id),
    CONSTRAINT inventory_movements_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES core.lots (id),
    CONSTRAINT inventory_movements_created_by_fkey FOREIGN KEY (tenant_id, created_by) REFERENCES core.users (tenant_id, id),
    CONSTRAINT inventory_movements_quantity_delta_check CHECK (quantity_delta <> 0),
    CONSTRAINT inventory_movements_reason_check
        CHECK (reason IN ('COMMISSIONED', 'SHIPMENT_CREATED', 'SHIPMENT_CANCELLED', 'SHIPMENT_DELIVERED')),
    -- Every movement but commissioning belongs to a shipment.
    CONSTRAINT inventory_movements_shipment_id_check CHECK ((reason = 'COMMISSIONED') = (shipment_id IS NULL))
);

ALTER TABLE core.inventory_movements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.inventory_movements
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- The ledger is append-only (ADR-0002).
REVOKE UPDATE, DELETE ON core.inventory_movements FROM veritrace_core_app;

-- A tenant holds or held stock of a lot when it has a balance of it, even an empty one (ADR-0002). The function
-- reads balances of the current tenant only.
-- +goose StatementBegin
CREATE FUNCTION core.is_lot_visible(p_lot_id uuid)
    RETURNS boolean
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN EXISTS (
        SELECT 1
        FROM core.inventory_balances b
        WHERE b.lot_id = p_lot_id
          AND b.tenant_id = core.current_tenant_id()
    );
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.is_lot_visible(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.is_lot_visible(uuid) TO veritrace_core_app;

ALTER TABLE core.lots ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.lots
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));
-- Holders read the lot to ship received stock onward; only the owner changes it.
CREATE POLICY holder_read ON core.lots
    FOR SELECT
    TO veritrace_core_app
    USING (core.is_lot_visible(id));

-- +goose Down
DROP TABLE core.inventory_movements;
DROP POLICY holder_read ON core.lots;
DROP FUNCTION core.is_lot_visible(uuid);
DROP TABLE core.inventory_balances;
DROP TABLE core.lots;
