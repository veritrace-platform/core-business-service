//go:build integration

package event_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/platform/tracecontext"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

type fixture struct {
	db                                  *tenancytest.Database
	owner, carrier, consignee, stranger tenancytest.Tenant
	shipment                            tenancytest.Shipment
}

func setUp(t *testing.T) fixture {
	t.Helper()
	db := tenancytest.Start(t)
	f := fixture{db: db, owner: db.CreateTenant(t), carrier: db.CreateTenant(t), consignee: db.CreateTenant(t), stranger: db.CreateTenant(t)}
	lot := db.CommissionLot(t, f.owner, db.CreateProduct(t, f.owner), f.owner.Headquarters, 100)
	f.shipment = db.CreateShipment(t, tenancytest.ShipmentSpec{
		Owner: f.owner, Carrier: f.carrier, Consignee: f.consignee, Lot: lot,
		Origin: f.owner.Headquarters, Destination: db.CreateLocation(t, f.consignee), Quantity: 40,
	})
	return f
}

// appendAs appends an event in a transaction of tenant that holds the shipment's row lock, as commands do.
func (f fixture) appendAs(ctx context.Context, t *testing.T, tenant uuid.UUID, n event.New) event.Event {
	t.Helper()
	var appended event.Event
	err := f.db.Tenancy.WithTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM core.shipments WHERE id = $1 FOR UPDATE`, n.ShipmentID); err != nil {
			return err
		}
		var err error
		appended, err = event.Append(ctx, tx, n)
		return err
	})
	if err != nil {
		t.Fatalf("append %s: %v", n.Type, err)
	}
	return appended
}

func (f fixture) list(t *testing.T, tenant uuid.UUID) []event.Event {
	t.Helper()
	var events []event.Event
	if err := f.db.Tenancy.WithTenantTx(t.Context(), tenant, func(tx pgx.Tx) error {
		var err error
		events, err = event.All(t.Context(), tx, f.shipment.ID)
		return err
	}); err != nil {
		t.Fatalf("list events: %v", err)
	}
	return events
}

func TestAppendBuildsAChain(t *testing.T) {
	f := setUp(t)
	ctx := tracecontext.NewContext(t.Context(), tracecontext.New())
	ownerActor := &event.Actor{TenantID: f.owner.ID, UserID: f.owner.Admin.ID}
	at := time.Date(2026, 10, 2, 8, 0, 0, 123456789, time.UTC)
	created := f.appendAs(ctx, t, f.owner.ID, event.New{
		ShipmentID: f.shipment.ID, SSCC: f.shipment.SSCC, Status: policy.ShipmentCreated, Type: event.TypeCreated,
		Actor: ownerActor, OccurredAt: at, Data: map[string]any{"quantity": 40, "min_temp_celsius": 2.0},
	})
	if created.Sequence != 1 || created.PrevEventHash != event.ZeroHash || created.OccurredAt != "2026-10-02T08:00:00.123456Z" {
		t.Errorf("first event = %+v", created)
	}
	driver := uuid.New()
	assigned := f.appendAs(ctx, t, f.carrier.ID, event.New{
		ShipmentID: f.shipment.ID, SSCC: f.shipment.SSCC, Status: policy.ShipmentCreated, Type: event.TypeDriverAssigned,
		Actor: &event.Actor{TenantID: f.carrier.ID, UserID: f.carrier.Admin.ID}, OccurredAt: at.Add(time.Minute),
		Data: map[string]any{"driver_user_id": driver},
	})
	system := f.appendAs(ctx, t, f.consignee.ID, event.New{
		ShipmentID: f.shipment.ID, SSCC: f.shipment.SSCC, Status: policy.ShipmentInTransit, Type: event.TypePickupConfirmed,
		OccurredAt: at.Add(2 * time.Minute), Data: map[string]any{"distance_meters": 18.4},
	})
	if assigned.Sequence != 2 || assigned.PrevEventHash != created.EventHash || system.Sequence != 3 ||
		system.PrevEventHash != assigned.EventHash || system.Actor != nil {
		t.Errorf("chain = %+v, %+v", assigned, system)
	}

	// Every participant reads the same verifiable log; others read nothing.
	for _, participant := range []tenancytest.Tenant{f.owner, f.carrier, f.consignee} {
		events := f.list(t, participant.ID)
		if got := event.Verify(events); !got.Valid || got.EventCount != 3 || *got.HeadHash != system.EventHash {
			t.Errorf("tenant %s: Verify() = %+v", participant.Code, got)
		}
	}
	if events := f.list(t, f.stranger.ID); len(events) != 0 {
		t.Errorf("a stranger read %d events", len(events))
	}

	// Each event is queued for shipment.events, keyed by SSCC, with the published envelope.
	rows, err := f.db.Owner.Query(t.Context(), `SELECT topic, message_key, payload, headers FROM core.outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type queued struct {
		topic, key       string
		payload, headers []byte
	}
	messages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queued, error) {
		var q queued
		return q, row.Scan(&q.topic, &q.key, &q.payload, &q.headers)
	})
	if err != nil || len(messages) != 3 {
		t.Fatalf("outbox = %d messages, %v", len(messages), err)
	}
	var envelope struct {
		event.Event
		Producer string `json:"producer"`
	}
	var headers map[string]string
	if err := json.Unmarshal(messages[1].payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(messages[1].headers, &headers); err != nil {
		t.Fatal(err)
	}
	if messages[1].topic != event.Topic || messages[1].key != f.shipment.SSCC || envelope.Producer != event.Producer ||
		envelope.EventHash != assigned.EventHash || headers["event-type"] != string(event.TypeDriverAssigned) ||
		headers["content-type"] != "application/json" || len(headers["traceparent"]) != 55 {
		t.Errorf("queued message = %s %s %s %v", messages[1].topic, messages[1].key, messages[1].payload, headers)
	}
}

func TestTheLogIsAppendOnly(t *testing.T) {
	f := setUp(t)
	for i := range 3 {
		f.appendAs(t.Context(), t, f.owner.ID, event.New{
			ShipmentID: f.shipment.ID, SSCC: f.shipment.SSCC, Status: policy.ShipmentCreated, Type: event.TypeDriverAssigned,
			Actor: &event.Actor{TenantID: f.owner.ID, UserID: f.owner.Admin.ID}, OccurredAt: time.Now(), Data: map[string]int{"n": i},
		})
	}
	db := f.db
	db.AssertDenied(t, f.owner.ID, `UPDATE core.shipment_events SET data = '{}' WHERE shipment_id = $1`, f.shipment.ID)
	db.AssertDenied(t, f.owner.ID, `DELETE FROM core.shipment_events WHERE shipment_id = $1`, f.shipment.ID)
	db.AssertDenied(t, f.stranger.ID, `
		INSERT INTO core.shipment_events (shipment_id, sequence, event_type, event_version, status, occurred_at, data,
		                                  prev_event_hash, event_hash)
		VALUES ($1, 4, 'shipment.cancelled', 1, 'CANCELLED', now(), '{}', repeat('0', 64), repeat('1', 64))`, f.shipment.ID)
	db.AssertHidden(t, f.stranger.ID, tenancytest.Rows{Table: "core.shipment_events", Where: map[string]any{"shipment_id": f.shipment.ID}})

	// Someone with direct database access who rewrites history breaks the chain where the change starts.
	if _, err := db.Owner.Exec(t.Context(), `UPDATE core.shipment_events SET data = '{"n": 7}' WHERE shipment_id = $1 AND sequence = 2`, f.shipment.ID); err != nil {
		t.Fatal(err)
	}
	if got := event.Verify(f.list(t, f.owner.ID)); got.Valid || *got.FirstInvalidSequence != 2 {
		t.Errorf("after rewriting event 2: Verify() = %+v", got)
	}
	if _, err := db.Owner.Exec(t.Context(), `DELETE FROM core.shipment_events WHERE shipment_id = $1 AND sequence = 1`, f.shipment.ID); err != nil {
		t.Fatal(err)
	}
	if got := event.Verify(f.list(t, f.owner.ID)); got.Valid || *got.FirstInvalidSequence != 1 || got.EventCount != 2 {
		t.Errorf("after deleting event 1: Verify() = %+v", got)
	}
}
