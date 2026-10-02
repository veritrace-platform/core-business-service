-- Shipments, their participants and hash-chained event log, and the outbox (data-model.md §3.3). A shipment is
-- visible to its participants only (ADR-0002); every state change appends an event and an outbox message in the
-- same transaction (ADR-0005, ADR-0006).

-- +goose Up
CREATE TABLE core.shipments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_tenant_id uuid NOT NULL,
    sscc char(18) NOT NULL,
    lot_id uuid NOT NULL,
    quantity integer NOT NULL,
    -- Snapshots of the product and lot, copied from the lot.
    gtin char(14) NOT NULL,
    product_name text NOT NULL,
    lot_number text NOT NULL,
    expiration_date date NOT NULL,
    min_temp_celsius numeric(5, 2) NOT NULL,
    max_temp_celsius numeric(5, 2) NOT NULL,
    origin_location_id uuid NOT NULL,
    origin_gln char(13) NOT NULL,
    origin_name text NOT NULL,
    origin_latitude numeric(9, 6) NOT NULL,
    origin_longitude numeric(9, 6) NOT NULL,
    origin_geo_fence_radius_meters integer NOT NULL,
    destination_location_id uuid NOT NULL,
    destination_gln char(13) NOT NULL,
    destination_name text NOT NULL,
    destination_latitude numeric(9, 6) NOT NULL,
    destination_longitude numeric(9, 6) NOT NULL,
    destination_geo_fence_radius_meters integer NOT NULL,
    consignee_tenant_id uuid NOT NULL,
    carrier_tenant_id uuid NOT NULL,
    assigned_driver_id uuid,
    status text NOT NULL DEFAULT 'CREATED',
    picked_up_at timestamptz,
    delivered_at timestamptz,
    cancelled_at timestamptz,
    recalled_at timestamptz,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shipments_sscc_key UNIQUE (sscc),
    CONSTRAINT shipments_owner_tenant_id_fkey FOREIGN KEY (owner_tenant_id) REFERENCES core.tenants (id),
    CONSTRAINT shipments_lot_id_fkey FOREIGN KEY (lot_id) REFERENCES core.lots (id),
    CONSTRAINT shipments_origin_location_id_fkey
        FOREIGN KEY (owner_tenant_id, origin_location_id) REFERENCES core.locations (tenant_id, id),
    CONSTRAINT shipments_destination_location_id_fkey
        FOREIGN KEY (consignee_tenant_id, destination_location_id) REFERENCES core.locations (tenant_id, id),
    CONSTRAINT shipments_carrier_tenant_id_fkey FOREIGN KEY (carrier_tenant_id) REFERENCES core.tenants (id),
    -- The driver is a user of the carrier.
    CONSTRAINT shipments_assigned_driver_id_fkey
        FOREIGN KEY (carrier_tenant_id, assigned_driver_id) REFERENCES core.users (tenant_id, id),
    CONSTRAINT shipments_created_by_fkey FOREIGN KEY (owner_tenant_id, created_by) REFERENCES core.users (tenant_id, id),
    CONSTRAINT shipments_sscc_check CHECK (sscc ~ '^[0-9]{18}$'),
    CONSTRAINT shipments_quantity_check CHECK (quantity > 0),
    CONSTRAINT shipments_temperature_range_check CHECK (min_temp_celsius < max_temp_celsius),
    CONSTRAINT shipments_route_check CHECK (destination_location_id <> origin_location_id),
    CONSTRAINT shipments_status_check CHECK (status IN ('CREATED', 'IN_TRANSIT', 'DELIVERED', 'CANCELLED', 'RECALLED')),
    CONSTRAINT shipments_cancelled_at_check CHECK ((status = 'CANCELLED') = (cancelled_at IS NOT NULL)),
    CONSTRAINT shipments_recalled_at_check CHECK ((status = 'RECALLED') = (recalled_at IS NOT NULL))
);

CREATE INDEX idx_shipments_owner_tenant_id_created_at ON core.shipments (owner_tenant_id, created_at);
CREATE INDEX idx_shipments_lot_id ON core.shipments (lot_id);
CREATE INDEX idx_shipments_status ON core.shipments (status);
CREATE INDEX idx_shipments_assigned_driver_id ON core.shipments (assigned_driver_id);

CREATE TRIGGER set_updated_at
    BEFORE UPDATE ON core.shipments
    FOR EACH ROW EXECUTE FUNCTION core.set_updated_at();

CREATE TABLE core.shipment_participants (
    shipment_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    role text NOT NULL,
    -- Directory fields of the tenant when it joined, so participants need not read each other's tenant rows.
    tenant_code text NOT NULL,
    tenant_legal_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shipment_participants_pkey PRIMARY KEY (shipment_id, tenant_id, role),
    CONSTRAINT shipment_participants_shipment_id_fkey FOREIGN KEY (shipment_id) REFERENCES core.shipments (id),
    CONSTRAINT shipment_participants_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES core.tenants (id),
    CONSTRAINT shipment_participants_role_check CHECK (role IN ('OWNER', 'CARRIER', 'CONSIGNEE', 'INSPECTOR'))
);

-- A shipment has one owner, one carrier, and one consignee; inspectors may be several (M2).
CREATE UNIQUE INDEX shipment_participants_role_key ON core.shipment_participants (shipment_id, role)
    WHERE role <> 'INSPECTOR';
CREATE INDEX idx_shipment_participants_tenant_id_shipment_id ON core.shipment_participants (tenant_id, shipment_id);

CREATE TABLE core.shipment_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    shipment_id uuid NOT NULL,
    sequence integer NOT NULL,
    event_type text NOT NULL,
    event_version smallint NOT NULL,
    status text NOT NULL,
    actor_tenant_id uuid,
    actor_user_id uuid,
    occurred_at timestamptz NOT NULL,
    data jsonb NOT NULL,
    prev_event_hash char(64) NOT NULL,
    event_hash char(64) NOT NULL,
    CONSTRAINT shipment_events_shipment_id_sequence_key UNIQUE (shipment_id, sequence),
    CONSTRAINT shipment_events_event_hash_key UNIQUE (event_hash),
    CONSTRAINT shipment_events_shipment_id_fkey FOREIGN KEY (shipment_id) REFERENCES core.shipments (id),
    CONSTRAINT shipment_events_actor_user_id_fkey
        FOREIGN KEY (actor_tenant_id, actor_user_id) REFERENCES core.users (tenant_id, id),
    CONSTRAINT shipment_events_actor_check CHECK ((actor_tenant_id IS NULL) = (actor_user_id IS NULL)),
    CONSTRAINT shipment_events_sequence_check CHECK (sequence >= 1),
    CONSTRAINT shipment_events_event_type_check CHECK (event_type ~ '^shipment\.[a-z_]+$'),
    CONSTRAINT shipment_events_event_version_check CHECK (event_version >= 1),
    CONSTRAINT shipment_events_status_check
        CHECK (status IN ('CREATED', 'IN_TRANSIT', 'DELIVERED', 'CANCELLED', 'RECALLED')),
    CONSTRAINT shipment_events_data_check CHECK (jsonb_typeof(data) = 'object'),
    CONSTRAINT shipment_events_prev_event_hash_check CHECK (prev_event_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT shipment_events_event_hash_check CHECK (event_hash ~ '^[0-9a-f]{64}$')
);

-- Messages waiting for, or recently given to, Kafka. The relay publishes them in id order.
CREATE TABLE core.outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic text NOT NULL,
    message_key text NOT NULL,
    payload jsonb NOT NULL,
    headers jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    CONSTRAINT outbox_headers_check CHECK (jsonb_typeof(headers) = 'object')
);

CREATE INDEX idx_outbox_pending ON core.outbox (id) WHERE published_at IS NULL;
CREATE INDEX idx_outbox_published_at ON core.outbox (published_at) WHERE published_at IS NOT NULL;

ALTER TABLE core.inventory_movements
    ADD CONSTRAINT inventory_movements_shipment_id_fkey FOREIGN KEY (shipment_id) REFERENCES core.shipments (id);

-- +goose StatementBegin
CREATE FUNCTION core.is_shipment_participant(p_shipment_id uuid)
    RETURNS boolean
    LANGUAGE sql
    STABLE
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN EXISTS (
        SELECT 1
        FROM core.shipment_participants p
        WHERE p.shipment_id = p_shipment_id
          AND p.tenant_id = core.current_tenant_id()
    );
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.is_shipment_participant(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.is_shipment_participant(uuid) TO veritrace_core_app;

ALTER TABLE core.shipments ENABLE ROW LEVEL SECURITY;
-- The owner sees a shipment from the moment it inserts it, before the participant rows exist.
CREATE POLICY participant_read ON core.shipments
    FOR SELECT
    TO veritrace_core_app
    USING (owner_tenant_id = (SELECT core.current_tenant_id()) OR core.is_shipment_participant(id));
CREATE POLICY owner_insert ON core.shipments
    FOR INSERT
    TO veritrace_core_app
    WITH CHECK (owner_tenant_id = (SELECT core.current_tenant_id()));
-- Participants change the shipment through its commands; the service decides which party may run each.
CREATE POLICY participant_update ON core.shipments
    FOR UPDATE
    TO veritrace_core_app
    USING (core.is_shipment_participant(id))
    WITH CHECK (core.is_shipment_participant(id));

-- Shipments end as cancelled or recalled, never deleted.
REVOKE DELETE ON core.shipments FROM veritrace_core_app;

ALTER TABLE core.shipment_participants ENABLE ROW LEVEL SECURITY;
CREATE POLICY participant_read ON core.shipment_participants
    FOR SELECT
    TO veritrace_core_app
    USING (core.is_shipment_participant(shipment_id));
-- Only the owner adds participants, and replaces itself as the carrier.
CREATE POLICY owner_insert ON core.shipment_participants
    FOR INSERT
    TO veritrace_core_app
    WITH CHECK (EXISTS (
        SELECT 1
        FROM core.shipments s
        WHERE s.id = shipment_id
          AND s.owner_tenant_id = (SELECT core.current_tenant_id())
    ));
CREATE POLICY owner_delete ON core.shipment_participants
    FOR DELETE
    TO veritrace_core_app
    USING (EXISTS (
        SELECT 1
        FROM core.shipments s
        WHERE s.id = shipment_id
          AND s.owner_tenant_id = (SELECT core.current_tenant_id())
    ));

REVOKE UPDATE ON core.shipment_participants FROM veritrace_core_app;

ALTER TABLE core.shipment_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY participant_read ON core.shipment_events
    FOR SELECT
    TO veritrace_core_app
    USING (core.is_shipment_participant(shipment_id));
CREATE POLICY participant_insert ON core.shipment_events
    FOR INSERT
    TO veritrace_core_app
    WITH CHECK (core.is_shipment_participant(shipment_id));

-- The event log is append-only (ADR-0006).
REVOKE UPDATE, DELETE ON core.shipment_events FROM veritrace_core_app;

-- +goose Down
ALTER TABLE core.inventory_movements DROP CONSTRAINT inventory_movements_shipment_id_fkey;
DROP TABLE core.outbox;
DROP TABLE core.shipment_events;
-- The function reads shipment_participants, and these policies call it.
DROP POLICY participant_read ON core.shipments;
DROP POLICY participant_update ON core.shipments;
DROP POLICY participant_read ON core.shipment_participants;
DROP FUNCTION core.is_shipment_participant(uuid);
DROP TABLE core.shipment_participants;
DROP TABLE core.shipments;
