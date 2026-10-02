-- name: AddToBalance :one
-- PostgreSQL checks the proposed row before it looks for a conflict, so only positive quantities take this path.
INSERT INTO core.inventory_balances AS b (
    tenant_id,
    location_id,
    lot_id,
    quantity_on_hand
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(location_id),
    sqlc.arg(lot_id),
    sqlc.arg(quantity)
)
ON CONFLICT (location_id, lot_id) DO UPDATE
SET quantity_on_hand = b.quantity_on_hand + excluded.quantity_on_hand
RETURNING quantity_on_hand;

-- name: TakeFromBalance :one
UPDATE core.inventory_balances
SET quantity_on_hand = quantity_on_hand - sqlc.arg(quantity)
WHERE location_id = sqlc.arg(location_id)
  AND lot_id = sqlc.arg(lot_id)
RETURNING quantity_on_hand;

-- name: RecordMovement :exec
INSERT INTO core.inventory_movements (
    tenant_id,
    location_id,
    lot_id,
    quantity_delta,
    reason,
    shipment_id,
    created_by
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(location_id),
    sqlc.arg(lot_id),
    sqlc.arg(quantity_delta),
    sqlc.arg(reason),
    sqlc.narg(shipment_id),
    sqlc.arg(created_by)
);

-- name: ListBalances :many
SELECT
    b.location_id,
    l.gln AS location_gln,
    l.name AS location_name,
    b.lot_id,
    t.lot_number,
    t.gtin,
    t.product_name,
    t.expiration_date,
    t.status AS lot_status,
    b.quantity_on_hand,
    b.updated_at
FROM core.inventory_balances b
JOIN core.locations l ON l.id = b.location_id
JOIN core.lots t ON t.id = b.lot_id
WHERE b.quantity_on_hand > 0
  AND (sqlc.narg(location_id)::uuid IS NULL OR b.location_id = sqlc.narg(location_id))
  AND (sqlc.narg(lot_id)::uuid IS NULL OR b.lot_id = sqlc.narg(lot_id))
  AND (
      sqlc.narg(after_location_id)::uuid IS NULL
      OR b.location_id < sqlc.narg(after_location_id)
      OR (b.location_id = sqlc.narg(after_location_id) AND b.lot_id < sqlc.narg(after_lot_id)::uuid)
  )
ORDER BY b.location_id DESC, b.lot_id DESC
LIMIT sqlc.arg(row_limit);
