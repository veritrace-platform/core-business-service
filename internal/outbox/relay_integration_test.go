//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/outbox"
	"github.com/veritrace-platform/core-business-service/internal/outbox/kafkatest"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

const topic = "shipment.events"

// enqueue stores messages in one transaction of the runtime role, as a state change does.
func enqueue(t *testing.T, db *tenancytest.Database, messages ...outbox.Message) {
	t.Helper()
	err := pgx.BeginFunc(t.Context(), db.App, func(tx pgx.Tx) error {
		for _, m := range messages {
			if err := outbox.Enqueue(t.Context(), tx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func message(key string, n int) outbox.Message {
	return outbox.Message{
		Topic: topic, Key: key, Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)),
		Headers: map[string]string{"event-type": "test.event", "traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
	}
}

func pending(t *testing.T, db *tenancytest.Database) int {
	t.Helper()
	var n int
	if err := db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRelayPublishesInOrder(t *testing.T) {
	db := tenancytest.Start(t)
	brokers := kafkatest.Start(t, 6, topic)
	publisher, err := outbox.NewKafkaPublisher(brokers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(publisher.Close)
	relay := outbox.NewRelay(db.App, publisher, slog.New(slog.DiscardHandler), time.Now)

	keys := []string{"A", "B", "A", "C", "A"}
	for i, key := range keys {
		enqueue(t, db, message(key, i))
	}
	published, err := relay.RelayOnce(t.Context())
	if err != nil || published != len(keys) {
		t.Fatalf("RelayOnce() = %d, %v; want %d", published, err, len(keys))
	}
	if n := pending(t, db); n != 0 {
		t.Errorf("%d messages still pending", n)
	}
	if again, err := relay.RelayOnce(t.Context()); err != nil || again != 0 {
		t.Errorf("second RelayOnce() = %d, %v; want nothing to publish", again, err)
	}

	byKey := map[string][]int{}
	for _, r := range kafkatest.Consume(t, brokers, topic, len(keys)) {
		var payload struct{ N int }
		if err := json.Unmarshal(r.Value, &payload); err != nil {
			t.Fatal(err)
		}
		byKey[string(r.Key)] = append(byKey[string(r.Key)], payload.N)
		headers := map[string]string{}
		for _, h := range r.Headers {
			headers[h.Key] = string(h.Value)
		}
		if headers["event-type"] != "test.event" || headers["traceparent"] == "" {
			t.Errorf("record headers = %v", headers)
		}
	}
	if got := byKey["A"]; len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 4 {
		t.Errorf("messages of key A arrived as %v, want [0 2 4]", got)
	}
}

// failingPublisher rejects every batch.
type failingPublisher struct{ calls int }

func (p *failingPublisher) Publish(context.Context, []outbox.Message) error {
	p.calls++
	return errors.New("broker unreachable")
}

// recordingPublisher keeps every message it is given.
type recordingPublisher struct{ messages []outbox.Message }

func (p *recordingPublisher) Publish(_ context.Context, messages []outbox.Message) error {
	p.messages = append(p.messages, messages...)
	return nil
}

func TestRelayKeepsMessagesUntilPublished(t *testing.T) {
	db := tenancytest.Start(t)
	enqueue(t, db, message("A", 1), message("A", 2))

	failing := &failingPublisher{}
	if _, err := outbox.NewRelay(db.App, failing, slog.New(slog.DiscardHandler), time.Now).RelayOnce(t.Context()); err == nil {
		t.Error("RelayOnce() succeeded with a failing publisher")
	}
	if n := pending(t, db); n != 2 {
		t.Errorf("%d messages pending after a failed publish, want 2", n)
	}

	// While another relay holds the batch lock, nothing is published.
	recording := &recordingPublisher{}
	relay := outbox.NewRelay(db.App, recording, slog.New(slog.DiscardHandler), time.Now)
	err := pgx.BeginFunc(t.Context(), db.App, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(4715400003)`).Scan(&locked); err != nil || !locked {
			return fmt.Errorf("take the relay lock: %v, %w", locked, err)
		}
		if published, err := relay.RelayOnce(t.Context()); err != nil || published != 0 {
			t.Errorf("RelayOnce() beside another relay = %d, %v; want 0", published, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run publishes what is pending, then stops with its context.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Run(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for pending(t, db) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if len(recording.messages) != 2 || string(recording.messages[0].Payload) != `{"n": 1}` {
		t.Errorf("published %+v", recording.messages)
	}
}

func TestPurge(t *testing.T) {
	db := tenancytest.Start(t)
	enqueue(t, db, message("A", 1), message("B", 2), message("C", 3))
	if _, err := outbox.NewRelay(db.App, &recordingPublisher{}, slog.New(slog.DiscardHandler), time.Now).RelayOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Owner.Exec(t.Context(), `UPDATE core.outbox SET published_at = now() - interval '8 days' WHERE message_key = 'A'`); err != nil {
		t.Fatal(err)
	}
	enqueue(t, db, message("D", 4)) // pending messages are never purged

	deleted, err := outbox.NewRelay(db.App, nil, slog.New(slog.DiscardHandler), time.Now).Purge(t.Context())
	var left int
	if qerr := db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.outbox`).Scan(&left); qerr != nil {
		t.Fatal(qerr)
	}
	if err != nil || deleted != 1 || left != 3 {
		t.Errorf("Purge() = %d, %v with %d messages left; want 1 deleted and 3 left", deleted, err, left)
	}
}
