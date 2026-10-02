-- name: GetLot :one
SELECT
    tenant_id,
    status
FROM core.lots
WHERE id = sqlc.arg(id);

-- name: RecallLot :one
SELECT
    recall_id,
    affected_shipment_count,
    recalled_at
FROM core.recall_lot(sqlc.arg(lot_id), sqlc.arg(reason), sqlc.arg(user_id), sqlc.narg(traceparent));
