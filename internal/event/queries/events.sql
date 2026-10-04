-- name: GetHead :one
SELECT
    sequence,
    event_hash
FROM core.shipment_events
WHERE shipment_id = sqlc.arg(shipment_id)
ORDER BY sequence DESC
LIMIT 1;

-- name: InsertEvent :exec
INSERT INTO core.shipment_events (
    id,
    shipment_id,
    sequence,
    event_type,
    event_version,
    status,
    actor_tenant_id,
    actor_user_id,
    occurred_at,
    data,
    prev_event_hash,
    event_hash
)
VALUES (
    sqlc.arg(id),
    sqlc.arg(shipment_id),
    sqlc.arg(sequence),
    sqlc.arg(event_type),
    sqlc.arg(event_version),
    sqlc.arg(status),
    sqlc.narg(actor_tenant_id),
    sqlc.narg(actor_user_id),
    sqlc.arg(occurred_at),
    sqlc.arg(data),
    sqlc.arg(prev_event_hash),
    sqlc.arg(event_hash)
);

-- name: ListEvents :many
SELECT
    e.id,
    e.shipment_id,
    s.sscc,
    e.sequence,
    e.event_type,
    e.event_version,
    e.status,
    e.actor_tenant_id,
    e.actor_user_id,
    e.occurred_at,
    e.data,
    e.prev_event_hash,
    e.event_hash
FROM core.shipment_events e
JOIN core.shipments s ON s.id = e.shipment_id
WHERE e.shipment_id = sqlc.arg(shipment_id)
  AND e.sequence > sqlc.arg(after_sequence)
ORDER BY e.sequence
LIMIT sqlc.arg(row_limit);
