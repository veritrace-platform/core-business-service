//go:build integration

package recall_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/tracecontext"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/recall"
	"github.com/veritrace-platform/core-business-service/internal/shipment"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

func as(u tenancytest.User) identity.Principal {
	return identity.Principal{UserID: u.ID, TenantID: u.TenantID, Role: u.Role}
}

func denied(err error, reason policy.Reason) bool {
	var d *policy.DenialError
	return errors.As(err, &d) && d.Reason == reason
}

// Fixture locations stand at 10.8, 106.65.
var atTheDock = shipment.Position{Latitude: 10.8, Longitude: 106.65, AccuracyMeters: 5}

// reason exercises every kind of character that the recall's canonical JSON must escape, or must not.
const reason = "Contaminated \"batch\" at C:\\plant\nsecond line\ttab \u0001 Tân Thuận 🚚 \u2028 end"

type scene struct {
	db                                  *tenancytest.Database
	shipments                           *shipment.Service
	recalls                             *recall.Service
	owner, carrier, consignee, retailer tenancytest.Tenant
	lot                                 tenancytest.Lot
	admin, ownerManager, carrierManager identity.Principal
	driver, receiver                    identity.Principal
	created, inTransit, delivered       shipment.Shipment
	cancelled, onward, otherLot         shipment.Shipment
	store                               tenancytest.Location
}

// newScene ships one lot every way it can go: CREATED with a pickup code, IN_TRANSIT, DELIVERED, CANCELLED, and an
// onward hop by the consignee from its received stock, plus a shipment of another lot.
func newScene(t *testing.T) scene {
	t.Helper()
	db := tenancytest.Start(t)
	s := scene{db: db, owner: db.CreateTenant(t), carrier: db.CreateTenant(t), consignee: db.CreateTenant(t), retailer: db.CreateTenant(t)}
	s.shipments = shipment.NewService(shipment.NewPostgresStore(db.Tenancy), []byte("pickup code pepper for recall tests"), time.Now)
	s.recalls = recall.NewService(recall.NewPostgresStore(db.Tenancy))
	product := db.CreateProduct(t, s.owner)
	plant := db.CreateLocation(t, s.owner)
	s.lot = db.CommissionLot(t, s.owner, product, plant, 1000)
	otherLot := db.CommissionLot(t, s.owner, product, plant, 10)
	s.store = db.CreateLocation(t, s.consignee)
	shop := db.CreateLocation(t, s.retailer)
	s.admin = as(s.owner.Admin)
	s.ownerManager = as(db.CreateUser(t, s.owner.ID, identity.RoleWarehouseManager))
	s.carrierManager = as(db.CreateUser(t, s.carrier.ID, identity.RoleWarehouseManager))
	s.driver = as(db.CreateUser(t, s.carrier.ID, identity.RoleDriver))
	s.receiver = as(db.CreateUser(t, s.consignee.ID, identity.RoleWarehouseManager))

	ctx := t.Context()
	must := func(sh shipment.Shipment, err error) shipment.Shipment {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return sh
	}
	create := func(lot tenancytest.Lot, quantity int) shipment.Shipment {
		return must(s.shipments.Create(ctx, s.ownerManager, shipment.NewShipment{
			LotID: lot.ID, Quantity: quantity, OriginLocationID: plant.ID, DestinationGLN: s.store.GLN, CarrierTenantCode: s.carrier.Code,
		}))
	}
	pickUp := func(sh shipment.Shipment) shipment.Shipment {
		must(s.shipments.AssignDriver(ctx, s.carrierManager, sh.ID, s.driver.UserID))
		issued, err := s.shipments.IssuePickupCode(ctx, s.ownerManager, sh.ID)
		if err != nil {
			t.Fatal(err)
		}
		return must(s.shipments.ConfirmPickup(ctx, s.driver, sh.ID, shipment.Pickup{SSCC: sh.SSCC, Code: issued.Code, Position: atTheDock}))
	}

	s.created = must(s.shipments.AssignDriver(ctx, s.carrierManager, create(s.lot, 100).ID, s.driver.UserID))
	if _, err := s.shipments.IssuePickupCode(ctx, s.ownerManager, s.created.ID); err != nil {
		t.Fatal(err)
	}
	s.inTransit = pickUp(create(s.lot, 100))
	toDeliver := pickUp(create(s.lot, 100))
	s.delivered = must(s.shipments.ConfirmDelivery(ctx, s.receiver, toDeliver.ID,
		shipment.Delivery{SSCC: toDeliver.SSCC, Position: atTheDock}))
	s.cancelled = must(s.shipments.Cancel(ctx, s.ownerManager, create(s.lot, 100).ID, "duplicate order"))
	s.onward = must(s.shipments.Create(ctx, s.receiver, shipment.NewShipment{
		LotID: s.lot.ID, Quantity: 40, OriginLocationID: s.store.ID, DestinationGLN: shop.GLN,
	}))
	s.otherLot = create(otherLot, 5)
	return s
}

func TestRecallReachesEveryHolder(t *testing.T) {
	s := newScene(t)
	ctx := tracecontext.NewContext(t.Context(), tracecontext.New())

	got, err := s.recalls.Recall(ctx, s.admin, s.lot.ID, reason)
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if got.AffectedShipmentCount != 4 || got.LotID != s.lot.ID || got.Reason != reason || got.InitiatedBy != s.admin.UserID {
		t.Errorf("Recall() = %+v, want the CREATED, IN_TRANSIT, DELIVERED, and onward shipments", got)
	}

	// Every shipment of the lot that was not cancelled is RECALLED, and its log ends with a valid
	// shipment.recalled, read from the point of view of one of its participants.
	recalled := map[uuid.UUID]struct {
		viewer   identity.Principal
		previous policy.ShipmentStatus
	}{
		s.created.ID:   {s.ownerManager, policy.ShipmentCreated},
		s.inTransit.ID: {s.driver, policy.ShipmentInTransit},
		s.delivered.ID: {s.receiver, policy.ShipmentDelivered},
		s.onward.ID:    {s.receiver, policy.ShipmentCreated},
	}
	for id, want := range recalled {
		sh, err := s.shipments.Get(t.Context(), want.viewer, id)
		if err != nil || sh.Status != policy.ShipmentRecalled || sh.RecalledAt == nil {
			t.Errorf("shipment %s = %s, %v; want RECALLED", id, sh.Status, err)
			continue
		}
		events, err := s.shipments.Events(t.Context(), want.viewer, id, event.Page{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		last := events[len(events)-1]
		var data struct {
			RecallID       uuid.UUID `json:"recall_id"`
			LotID          uuid.UUID `json:"lot_id"`
			Reason         string
			PreviousStatus policy.ShipmentStatus `json:"previous_status"`
		}
		if err := json.Unmarshal(last.Data, &data); err != nil || last.Type != event.TypeRecalled || data.RecallID != got.ID ||
			data.LotID != s.lot.ID || data.Reason != reason || data.PreviousStatus != want.previous ||
			last.Actor == nil || last.Actor.UserID != s.admin.UserID {
			t.Errorf("shipment %s: last event = %+v %s", id, last, last.Data)
		}
		if result, err := s.shipments.Integrity(t.Context(), want.viewer, id); err != nil || !result.Valid || result.EventCount != len(events) {
			t.Errorf("shipment %s: Integrity() = %+v, %v; the SQL-built event must verify", id, result, err)
		}
	}
	for _, untouched := range []shipment.Shipment{s.cancelled, s.otherLot} {
		if sh, err := s.shipments.Get(t.Context(), s.ownerManager, untouched.ID); err != nil || sh.Status == policy.ShipmentRecalled {
			t.Errorf("shipment %s = %s, %v; want it untouched", untouched.ID, sh.Status, err)
		}
	}

	// Stock stays where it is; the pickup code of the CREATED shipment is void; each event is queued for Kafka.
	var held int
	if err := s.db.Owner.QueryRow(t.Context(), `SELECT quantity_on_hand FROM core.inventory_balances WHERE location_id = $1 AND lot_id = $2`,
		s.store.ID, s.lot.ID).Scan(&held); err != nil || held != 60 {
		t.Errorf("consignee stock = %d, %v; want 60", held, err)
	}
	var activeCodes, queued int
	if err := s.db.Owner.QueryRow(t.Context(), `SELECT count(*) FROM core.pickup_codes WHERE shipment_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL`,
		s.created.ID).Scan(&activeCodes); err != nil || activeCodes != 0 {
		t.Errorf("active pickup codes = %d, %v; want 0", activeCodes, err)
	}
	if err := s.db.Owner.QueryRow(t.Context(), `
		SELECT count(*)
		FROM core.outbox o
		JOIN core.shipment_events e ON e.id = (o.payload ->> 'event_id')::uuid
		WHERE o.headers ->> 'event-type' = 'shipment.recalled'
		  AND o.headers ? 'traceparent'
		  AND o.payload ->> 'event_hash' = e.event_hash
		  AND o.payload ->> 'producer' = 'core-business-service'
		  AND o.message_key = o.payload -> 'subject' ->> 'sscc'`).Scan(&queued); err != nil || queued != 4 {
		t.Errorf("queued recall messages = %d, %v; want 4", queued, err)
	}

	// Every later command on a recalled shipment is refused, and the lot takes no new shipments.
	if _, err := s.shipments.Cancel(t.Context(), s.ownerManager, s.created.ID, "too late"); !denied(err, policy.ReasonShipmentRecalled) {
		t.Errorf("cancel: error = %v, want SHIPMENT_RECALLED_LOCKED", err)
	}
	if _, err := s.shipments.ConfirmDelivery(t.Context(), s.receiver, s.inTransit.ID, shipment.Delivery{SSCC: s.inTransit.SSCC, Position: atTheDock}); !denied(err, policy.ReasonShipmentRecalled) {
		t.Errorf("delivery: error = %v, want SHIPMENT_RECALLED_LOCKED", err)
	}
	if _, err := s.shipments.Create(t.Context(), s.receiver, shipment.NewShipment{
		LotID: s.lot.ID, Quantity: 1, OriginLocationID: s.store.ID, DestinationGLN: s.owner.Headquarters.GLN,
	}); !denied(err, policy.ReasonLotRecalled) {
		t.Errorf("new shipment: error = %v, want LOT_RECALLED", err)
	}
	if _, err := s.recalls.Recall(t.Context(), s.admin, s.lot.ID, "again"); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("second recall: error = %v, want an invalid state", err)
	}
}

func TestOnlyTheOwnersAdminRecalls(t *testing.T) {
	s := newScene(t)
	if _, err := s.recalls.Recall(t.Context(), s.ownerManager, s.lot.ID, "not mine to decide"); !denied(err, policy.ReasonRole) {
		t.Errorf("warehouse manager: error = %v, want a role denial", err)
	}
	if _, err := s.recalls.Recall(t.Context(), as(s.consignee.Admin), s.lot.ID, "holder"); !denied(err, policy.ReasonParty) {
		t.Errorf("holder admin: error = %v, want a party denial", err)
	}
	if _, err := s.recalls.Recall(t.Context(), as(s.carrier.Admin), s.lot.ID, "carrier"); !errors.Is(err, recall.ErrNotFound) {
		t.Errorf("carrier admin: error = %v, want ErrNotFound", err)
	}

	// The function itself refuses another tenant's lot, whoever calls it.
	err := s.db.Tenancy.WithTenantTx(t.Context(), s.consignee.ID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT * FROM core.recall_lot($1, 'x', $2, NULL)`, s.lot.ID, s.consignee.Admin.ID)
		return err
	})
	if err == nil {
		t.Error("core.recall_lot recalled another tenant's lot")
	}
	var status string
	if err := s.db.Owner.QueryRow(t.Context(), `SELECT status FROM core.lots WHERE id = $1`, s.lot.ID).Scan(&status); err != nil || status != "ACTIVE" {
		t.Errorf("lot status = %s, %v; want ACTIVE", status, err)
	}
}
