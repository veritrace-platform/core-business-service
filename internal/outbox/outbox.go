// Package outbox delivers domain events to Kafka after the transactions that record them commit (ADR-0005).
// A state change enqueues its messages in the same transaction; the relay publishes them in order and marks
// them published. Delivery is at least once, so consumers deduplicate on the event ID.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veritrace-platform/core-business-service/internal/outbox/queries"
)

// Message is one Kafka record.
type Message struct {
	Topic   string
	Key     string
	Payload json.RawMessage
	Headers map[string]string
}

// Enqueue stores m through db, the transaction of the state change that m reports.
func Enqueue(ctx context.Context, db queries.DBTX, m Message) error {
	headers, err := json.Marshal(m.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	if m.Headers == nil {
		headers = []byte("{}")
	}
	err = queries.New(db).Enqueue(ctx, queries.EnqueueParams{
		Topic: m.Topic, MessageKey: m.Key, Payload: m.Payload, Headers: headers,
	})
	if err != nil {
		return fmt.Errorf("enqueue message: %w", err)
	}
	return nil
}
