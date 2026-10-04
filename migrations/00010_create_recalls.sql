-- Emergency recall (data-model.md §3.3, §3.5; shipment-lifecycle.md §8). The lot owner's admin recalls a lot, and
-- every shipment of the lot that is not cancelled becomes RECALLED across all tenants, in one transaction.

-- +goose Up
CREATE TABLE core.recalls (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    lot_id uuid NOT NULL,
    reason text NOT NULL,
    affected_shipment_count integer NOT NULL,
    initiated_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT recalls_tenant_id_id_key UNIQUE (tenant_id, id),
    -- A lot is recalled at most once.
    CONSTRAINT recalls_lot_id_key UNIQUE (lot_id),
    CONSTRAINT recalls_lot_id_fkey FOREIGN KEY (tenant_id, lot_id) REFERENCES core.lots (tenant_id, id),
    CONSTRAINT recalls_initiated_by_fkey FOREIGN KEY (tenant_id, initiated_by) REFERENCES core.users (tenant_id, id),
    CONSTRAINT recalls_reason_check CHECK (char_length(reason) BETWEEN 1 AND 1000),
    CONSTRAINT recalls_affected_shipment_count_check CHECK (affected_shipment_count >= 0)
);

ALTER TABLE core.recalls ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON core.recalls
    TO veritrace_core_app
    USING (tenant_id = (SELECT core.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT core.current_tenant_id()));

-- Only core.recall_lot writes recalls, and they never change.
REVOKE INSERT, UPDATE, DELETE ON core.recalls FROM veritrace_core_app;

-- Recalls the current tenant's lot on behalf of one of its users. Each affected shipment gets a shipment.recalled
-- event in its hash chain and an outbox message. The event hash covers the RFC 8785 canonical JSON of the event
-- (messaging.md §2.1), written out below: its members in code-unit order, no whitespace, and strings escaped by
-- to_json, which escapes exactly as ECMAScript's JSON.stringify does. The service's verifier recomputes the hash
-- from the stored event, so the two must agree.
-- +goose StatementBegin
CREATE FUNCTION core.recall_lot(
    p_lot_id uuid,
    p_reason text,
    p_user_id uuid,
    p_traceparent text,
    OUT recall_id uuid,
    OUT affected_shipment_count integer,
    OUT recalled_at timestamptz
)
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_tenant_id uuid := core.current_tenant_id();
    v_recall_id uuid := uuidv7();
    v_now timestamptz := now();
    v_occurred_at text := to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
    v_count integer := 0;
    v_lot record;
    v_shipment record;
    v_sequence integer;
    v_prev text;
    v_event_id uuid;
    v_hash_input text;
    v_hash text;
    v_data jsonb;
BEGIN
    -- Wait for shipments being created from the lot, which hold this lock in shared mode, and keep new ones out
    -- until the recall commits.
    PERFORM pg_advisory_xact_lock(47154002, hashtext(p_lot_id::text));

    SELECT l.gtin::text AS gtin, l.lot_number, l.status
    INTO v_lot
    FROM core.lots l
    WHERE l.id = p_lot_id
      AND l.tenant_id = v_tenant_id
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'lot % is not a lot of the current tenant', p_lot_id USING ERRCODE = 'no_data_found';
    END IF;
    IF v_lot.status <> 'ACTIVE' THEN
        RAISE EXCEPTION 'lot % is already recalled', p_lot_id USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    UPDATE core.lots SET status = 'RECALLED', recalled_at = v_now WHERE id = p_lot_id;

    FOR v_shipment IN
        SELECT s.id, s.sscc::text AS sscc, s.status
        FROM core.shipments s
        WHERE s.lot_id = p_lot_id
          AND s.status IN ('CREATED', 'IN_TRANSIT', 'DELIVERED')
        ORDER BY s.id
        FOR UPDATE
    LOOP
        UPDATE core.shipments SET status = 'RECALLED', recalled_at = v_now WHERE id = v_shipment.id;
        UPDATE core.pickup_codes
        SET invalidated_at = v_now
        WHERE shipment_id = v_shipment.id
          AND consumed_at IS NULL
          AND invalidated_at IS NULL;

        SELECT e.sequence, e.event_hash::text
        INTO v_sequence, v_prev
        FROM core.shipment_events e
        WHERE e.shipment_id = v_shipment.id
        ORDER BY e.sequence DESC
        LIMIT 1;
        IF NOT FOUND THEN
            v_sequence := 0;
            v_prev := repeat('0', 64);
        END IF;
        v_sequence := v_sequence + 1;
        v_event_id := uuidv7();

        v_hash_input := '{"actor":{"tenant_id":' || to_json(v_tenant_id::text)::text
            || ',"user_id":' || to_json(p_user_id::text)::text
            || '},"data":{"gtin":' || to_json(v_lot.gtin)::text
            || ',"lot_id":' || to_json(p_lot_id::text)::text
            || ',"lot_number":' || to_json(v_lot.lot_number)::text
            || ',"previous_status":' || to_json(v_shipment.status)::text
            || ',"reason":' || to_json(p_reason)::text
            || ',"recall_id":' || to_json(v_recall_id::text)::text
            || '},"event_id":' || to_json(v_event_id::text)::text
            || ',"event_type":"shipment.recalled","event_version":1'
            || ',"occurred_at":' || to_json(v_occurred_at)::text
            || ',"prev_event_hash":' || to_json(v_prev)::text
            || ',"sequence":' || v_sequence::text
            || ',"subject":{"shipment_id":' || to_json(v_shipment.id::text)::text
            || ',"sscc":' || to_json(v_shipment.sscc)::text
            || ',"status":"RECALLED"}}';
        v_hash := encode(sha256(convert_to(v_hash_input, 'UTF8')), 'hex');
        v_data := jsonb_build_object(
            'recall_id', v_recall_id, 'lot_id', p_lot_id, 'gtin', v_lot.gtin, 'lot_number', v_lot.lot_number,
            'reason', p_reason, 'previous_status', v_shipment.status);

        INSERT INTO core.shipment_events (
            id, shipment_id, sequence, event_type, event_version, status, actor_tenant_id, actor_user_id,
            occurred_at, data, prev_event_hash, event_hash)
        VALUES (
            v_event_id, v_shipment.id, v_sequence, 'shipment.recalled', 1, 'RECALLED', v_tenant_id, p_user_id,
            v_now, v_data, v_prev, v_hash);

        INSERT INTO core.outbox (topic, message_key, payload, headers)
        VALUES (
            'shipment.events',
            v_shipment.sscc,
            jsonb_build_object(
                'event_id', v_event_id, 'event_type', 'shipment.recalled', 'event_version', 1,
                'occurred_at', v_occurred_at, 'producer', 'core-business-service',
                'subject', jsonb_build_object('shipment_id', v_shipment.id, 'sscc', v_shipment.sscc, 'status', 'RECALLED'),
                'actor', jsonb_build_object('tenant_id', v_tenant_id, 'user_id', p_user_id),
                'sequence', v_sequence, 'prev_event_hash', v_prev, 'event_hash', v_hash, 'data', v_data),
            jsonb_strip_nulls(jsonb_build_object(
                'content-type', 'application/json', 'event-type', 'shipment.recalled', 'traceparent', p_traceparent)));

        v_count := v_count + 1;
    END LOOP;

    INSERT INTO core.recalls (id, tenant_id, lot_id, reason, affected_shipment_count, initiated_by, created_at)
    VALUES (v_recall_id, v_tenant_id, p_lot_id, p_reason, v_count, p_user_id, v_now);

    recall_id := v_recall_id;
    affected_shipment_count := v_count;
    recalled_at := v_now;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION core.recall_lot(uuid, text, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION core.recall_lot(uuid, text, uuid, text) TO veritrace_core_app;

-- +goose Down
DROP FUNCTION core.recall_lot(uuid, text, uuid, text);
DROP TABLE core.recalls;
