package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/outbox/queries"
)

// Relay settings. Published messages are kept for a week (data-model.md §3.3) to help diagnose consumers.
const (
	batchSize    = 100
	pollInterval = 500 * time.Millisecond
	// publishTimeout bounds one batch, so that an unreachable broker does not hold the batch's row locks.
	publishTimeout = 30 * time.Second
	retention      = 7 * 24 * time.Hour
	purgeInterval  = time.Hour
	maxBackoff     = 30 * time.Second
)

// Publisher sends a batch of messages and returns once every one is acknowledged. Messages with the same key
// must be written in the order given.
type Publisher interface {
	Publish(ctx context.Context, messages []Message) error
}

// TxStarter begins transactions; *pgxpool.Pool implements it.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Relay publishes pending outbox messages in id order.
type Relay struct {
	db        TxStarter
	publisher Publisher
	logger    *slog.Logger
	now       func() time.Time
}

// NewRelay returns a Relay that reads the outbox through db, a pool of the runtime role.
func NewRelay(db TxStarter, publisher Publisher, logger *slog.Logger, now func() time.Time) *Relay {
	return &Relay{db: db, publisher: publisher, logger: logger, now: now}
}

// Run publishes messages until ctx is cancelled. A failed batch is retried with exponential backoff; published
// messages older than the retention are purged every hour.
func (r *Relay) Run(ctx context.Context) {
	backoff := pollInterval
	lastPurge := time.Time{}
	for ctx.Err() == nil {
		published, err := r.RelayOnce(ctx)
		wait := pollInterval
		switch {
		case err != nil && ctx.Err() == nil:
			r.logger.WarnContext(ctx, "outbox relay failed", slog.Any("error", err), slog.Duration("retry_in", backoff))
			wait, backoff = backoff, min(2*backoff, maxBackoff)
		case err == nil:
			backoff = pollInterval
			if published == batchSize {
				wait = 0 // more messages are waiting
			}
		}
		if now := r.now(); now.Sub(lastPurge) >= purgeInterval {
			lastPurge = now
			if _, err := r.Purge(ctx); err != nil && ctx.Err() == nil {
				r.logger.WarnContext(ctx, "outbox purge failed", slog.Any("error", err))
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// RelayOnce publishes the next batch of pending messages and returns how many it published. While another
// relay holds the batch lock it publishes nothing.
func (r *Relay) RelayOnce(ctx context.Context) (int, error) {
	published := 0
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		q := queries.New(tx)
		locked, err := q.TryLockRelay(ctx)
		if err != nil {
			return fmt.Errorf("take the relay lock: %w", err)
		}
		if !locked {
			return nil
		}
		rows, err := q.ListPending(ctx, batchSize)
		if err != nil {
			return fmt.Errorf("read pending messages: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		messages := make([]Message, len(rows))
		ids := make([]int64, len(rows))
		for i, row := range rows {
			messages[i] = Message{Topic: row.Topic, Key: row.MessageKey, Payload: row.Payload}
			if err := json.Unmarshal(row.Headers, &messages[i].Headers); err != nil {
				return fmt.Errorf("read headers of message %d: %w", row.ID, err)
			}
			ids[i] = row.ID
		}
		publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		if err := r.publisher.Publish(publishCtx, messages); err != nil {
			return fmt.Errorf("publish %d messages: %w", len(messages), err)
		}
		now := r.now()
		if err := q.MarkPublished(ctx, queries.MarkPublishedParams{PublishedAt: &now, Ids: ids}); err != nil {
			return fmt.Errorf("mark messages published: %w", err)
		}
		published = len(rows)
		return nil
	})
	return published, err
}

// Purge deletes messages published longer ago than the retention and returns how many it deleted.
func (r *Relay) Purge(ctx context.Context) (int64, error) {
	before := r.now().Add(-retention)
	var deleted int64
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		var err error
		deleted, err = queries.New(tx).DeletePublished(ctx, &before)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("purge published messages: %w", err)
	}
	return deleted, nil
}
