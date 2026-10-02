-- Pickup codes (data-model.md §3.3): the one-time code that the owner hands to the assigned driver at the dock,
-- stored only as an HMAC (shipment-lifecycle.md §6.1).

-- +goose Up
CREATE TABLE core.pickup_codes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    shipment_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    failed_attempts smallint NOT NULL DEFAULT 0,
    consumed_at timestamptz,
    invalidated_at timestamptz,
    issued_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pickup_codes_shipment_id_fkey FOREIGN KEY (shipment_id) REFERENCES core.shipments (id),
    CONSTRAINT pickup_codes_issued_by_fkey FOREIGN KEY (issued_by) REFERENCES core.users (id),
    CONSTRAINT pickup_codes_code_hash_check CHECK (octet_length(code_hash) = 32),
    CONSTRAINT pickup_codes_failed_attempts_check CHECK (failed_attempts BETWEEN 0 AND 5),
    CONSTRAINT pickup_codes_expires_at_check CHECK (expires_at > created_at)
);

-- At most one active code per shipment: issuing a code invalidates the one before.
CREATE UNIQUE INDEX pickup_codes_active_key ON core.pickup_codes (shipment_id)
    WHERE consumed_at IS NULL AND invalidated_at IS NULL;

ALTER TABLE core.pickup_codes ENABLE ROW LEVEL SECURITY;
CREATE POLICY participant_access ON core.pickup_codes
    TO veritrace_core_app
    USING (core.is_shipment_participant(shipment_id))
    WITH CHECK (core.is_shipment_participant(shipment_id));

-- Codes are consumed or invalidated, never deleted.
REVOKE DELETE ON core.pickup_codes FROM veritrace_core_app;

-- +goose Down
DROP TABLE core.pickup_codes;
