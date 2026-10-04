-- name: Enqueue :exec
INSERT INTO core.outbox (topic, message_key, payload, headers)
VALUES (sqlc.arg(topic), sqlc.arg(message_key), sqlc.arg(payload), sqlc.arg(headers));

-- name: TryLockRelay :one
-- One relay publishes at a time, so messages reach Kafka in outbox order.
SELECT pg_try_advisory_xact_lock(4715400003);

-- name: ListPending :many
SELECT
    id,
    topic,
    message_key,
    payload,
    headers
FROM core.outbox
WHERE published_at IS NULL
ORDER BY id
LIMIT sqlc.arg(row_limit)
FOR UPDATE;

-- name: MarkPublished :exec
UPDATE core.outbox
SET published_at = sqlc.arg(published_at)
WHERE id = ANY (sqlc.arg(ids)::bigint[]);

-- name: DeletePublished :execrows
DELETE FROM core.outbox
WHERE published_at < sqlc.arg(published_before);
