-- name: InvalidatePickupCodes :exec
UPDATE core.pickup_codes
SET invalidated_at = sqlc.arg(invalidated_at)
WHERE shipment_id = sqlc.arg(shipment_id)
  AND consumed_at IS NULL
  AND invalidated_at IS NULL;

-- name: InsertPickupCode :exec
INSERT INTO core.pickup_codes (shipment_id, code_hash, expires_at, issued_by, created_at)
VALUES (
    sqlc.arg(shipment_id),
    sqlc.arg(code_hash),
    sqlc.arg(expires_at),
    sqlc.arg(issued_by),
    sqlc.arg(created_at)
);

-- name: LockActivePickupCode :one
SELECT
    id,
    code_hash,
    expires_at,
    failed_attempts
FROM core.pickup_codes
WHERE shipment_id = sqlc.arg(shipment_id)
  AND consumed_at IS NULL
  AND invalidated_at IS NULL
FOR UPDATE;

-- name: RecordFailedAttempt :one
UPDATE core.pickup_codes
SET failed_attempts = failed_attempts + 1
WHERE id = sqlc.arg(id)
RETURNING failed_attempts;

-- name: ConsumePickupCode :exec
UPDATE core.pickup_codes
SET consumed_at = sqlc.arg(consumed_at)
WHERE id = sqlc.arg(id);

-- name: SetPickedUp :exec
UPDATE core.shipments
SET status = 'IN_TRANSIT',
    picked_up_at = sqlc.arg(picked_up_at)
WHERE id = sqlc.arg(id);

-- name: SetDelivered :exec
UPDATE core.shipments
SET status = 'DELIVERED',
    delivered_at = sqlc.arg(delivered_at)
WHERE id = sqlc.arg(id);
