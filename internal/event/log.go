package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/event/queries"
	"github.com/veritrace-platform/core-business-service/internal/outbox"
	"github.com/veritrace-platform/core-business-service/internal/platform/tracecontext"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// New is an event to append to a shipment's log.
type New struct {
	ShipmentID uuid.UUID
	SSCC       string
	// Status is the shipment's status after the event.
	Status     policy.ShipmentStatus
	Type       Type
	Actor      *Actor
	OccurredAt time.Time
	// Data is the payload of the event type (messaging.md §3); it must marshal to a JSON object.
	Data any
}

// Append adds an event to the end of a shipment's log and queues it for Kafka, through tx. The transaction
// must hold the shipment's row lock, so that no other event takes the same place in the log.
func Append(ctx context.Context, tx queries.DBTX, n New) (Event, error) {
	q := queries.New(tx)
	sequence, prev := 1, ZeroHash
	head, err := q.GetHead(ctx, n.ShipmentID)
	switch {
	case err == nil:
		sequence, prev = int(head.Sequence)+1, head.EventHash
	case !errors.Is(err, pgx.ErrNoRows):
		return Event{}, fmt.Errorf("read the head of the event log: %w", err)
	}
	data, err := json.Marshal(n.Data)
	if err != nil {
		return Event{}, fmt.Errorf("marshal %s data: %w", n.Type, err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("generate event ID: %w", err)
	}
	// PostgreSQL keeps microseconds, so the stored time formats to the same text as the hashed one.
	occurred := n.OccurredAt.UTC().Truncate(time.Microsecond)
	e := Event{
		ID: id, Type: n.Type, Version: Version, OccurredAt: occurred.Format(TimeLayout),
		Subject: Subject{ShipmentID: n.ShipmentID, SSCC: n.SSCC, Status: n.Status}, Actor: n.Actor,
		Sequence: sequence, PrevEventHash: prev, Data: data,
	}
	if e.EventHash, err = e.Hash(); err != nil {
		return Event{}, err
	}

	params := queries.InsertEventParams{
		ID: e.ID, ShipmentID: n.ShipmentID, Sequence: int32(sequence), //nolint:gosec // logs stay far below 2^31 events
		EventType: string(e.Type), EventVersion: Version, Status: string(n.Status), OccurredAt: occurred,
		Data: data, PrevEventHash: prev, EventHash: e.EventHash,
	}
	if n.Actor != nil {
		params.ActorTenantID, params.ActorUserID = &n.Actor.TenantID, &n.Actor.UserID
	}
	if err := q.InsertEvent(ctx, params); err != nil {
		return Event{}, fmt.Errorf("insert %s event: %w", e.Type, err)
	}
	if err := enqueue(ctx, tx, e); err != nil {
		return Event{}, err
	}
	return e, nil
}

// enqueue queues the published form of e: the envelope with the producer (messaging.md §2.1).
func enqueue(ctx context.Context, tx queries.DBTX, e Event) error {
	payload, err := json.Marshal(struct {
		Event
		Producer string `json:"producer"`
	}{e, Producer})
	if err != nil {
		return fmt.Errorf("marshal %s envelope: %w", e.Type, err)
	}
	headers := map[string]string{"content-type": "application/json", "event-type": string(e.Type)}
	if tc, ok := tracecontext.FromContext(ctx); ok {
		headers["traceparent"] = tc.Child().Traceparent()
	}
	return outbox.Enqueue(ctx, tx, outbox.Message{Topic: Topic, Key: e.Subject.SSCC, Payload: payload, Headers: headers})
}

// Page is a run of a shipment's events in sequence order.
type Page struct {
	// After is the sequence of the last event of the previous page; 0 starts at the first event.
	After int
	Limit int
}

// List returns events of a shipment in sequence order, through db, a transaction in which the shipment is
// visible.
func List(ctx context.Context, db queries.DBTX, shipmentID uuid.UUID, page Page) ([]Event, error) {
	rows, err := queries.New(db).ListEvents(ctx, queries.ListEventsParams{
		ShipmentID: shipmentID, AfterSequence: int32(page.After), RowLimit: int32(page.Limit), //nolint:gosec // validated bounds
	})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	events := make([]Event, len(rows))
	for i, row := range rows {
		events[i] = Event{
			ID: row.ID, Type: Type(row.EventType), Version: int(row.EventVersion), OccurredAt: row.OccurredAt.UTC().Format(TimeLayout),
			Subject:  Subject{ShipmentID: row.ShipmentID, SSCC: row.Sscc, Status: policy.ShipmentStatus(row.Status)},
			Sequence: int(row.Sequence), PrevEventHash: row.PrevEventHash, EventHash: row.EventHash, Data: row.Data,
		}
		if row.ActorTenantID != nil && row.ActorUserID != nil {
			events[i].Actor = &Actor{TenantID: *row.ActorTenantID, UserID: *row.ActorUserID}
		}
	}
	return events, nil
}

// All returns every event of a shipment in sequence order, as Verify needs them.
func All(ctx context.Context, db queries.DBTX, shipmentID uuid.UUID) ([]Event, error) {
	return List(ctx, db, shipmentID, Page{Limit: math.MaxInt32})
}
