//go:build integration

package shipment_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/shipment"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// world is an owner that commissioned 500 units at its plant, a carrier with a driver, and a consignee with a
// store, plus a tenant that takes no part.
type world struct {
	db                                  *tenancytest.Database
	svc                                 *shipment.Service
	owner, carrier, consignee, stranger tenancytest.Tenant
	product                             tenancytest.Product
	lot                                 tenancytest.Lot
	plant, store                        tenancytest.Location
	ownerManager, carrierManager        identity.Principal
	ownDriver, carrierDriver            identity.Principal
}

func as(u tenancytest.User) identity.Principal {
	return identity.Principal{UserID: u.ID, TenantID: u.TenantID, Role: u.Role}
}

func newWorld(t *testing.T) world {
	t.Helper()
	db := tenancytest.Start(t)
	w := world{db: db, owner: db.CreateTenant(t), carrier: db.CreateTenant(t), consignee: db.CreateTenant(t), stranger: db.CreateTenant(t)}
	w.svc = shipment.NewService(shipment.NewPostgresStore(db.Tenancy), time.Now)
	w.product = db.CreateProduct(t, w.owner)
	w.plant = db.CreateLocation(t, w.owner)
	w.lot = db.CommissionLot(t, w.owner, w.product, w.plant, 500)
	w.store = db.CreateLocation(t, w.consignee)
	w.ownerManager = as(db.CreateUser(t, w.owner.ID, identity.RoleWarehouseManager))
	w.carrierManager = as(db.CreateUser(t, w.carrier.ID, identity.RoleWarehouseManager))
	w.ownDriver = as(db.CreateUser(t, w.owner.ID, identity.RoleDriver))
	w.carrierDriver = as(db.CreateUser(t, w.carrier.ID, identity.RoleDriver))
	return w
}

func (w world) request(quantity int) shipment.NewShipment {
	return shipment.NewShipment{
		LotID: w.lot.ID, Quantity: quantity, OriginLocationID: w.plant.ID, DestinationGLN: w.store.GLN,
		CarrierTenantCode: w.carrier.Code,
	}
}

func (w world) balance(t *testing.T, location tenancytest.Location) int {
	t.Helper()
	var quantity int
	if err := w.db.Owner.QueryRow(t.Context(), `SELECT quantity_on_hand FROM core.inventory_balances WHERE location_id = $1 AND lot_id = $2`,
		location.ID, w.lot.ID).Scan(&quantity); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return quantity
}

func (w world) events(t *testing.T, p identity.Principal, id uuid.UUID) []event.Event {
	t.Helper()
	events, err := w.svc.Events(t.Context(), p, id, event.Page{Limit: 100})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return events
}

func reference(err error, field string) bool {
	var invalid *shipment.InvalidReferenceError
	return errors.As(err, &invalid) && invalid.Field == field
}

func denied(err error, reason policy.Reason) bool {
	var d *policy.DenialError
	return errors.As(err, &d) && d.Reason == reason
}

func TestCreate(t *testing.T) {
	w := newWorld(t)
	created, err := w.svc.Create(t.Context(), w.ownerManager, w.request(480))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	firstSSCC, _ := gs1.BuildSSCC(0, w.owner.GCP, 1)
	if created.SSCC != firstSSCC || created.Status != policy.ShipmentCreated || created.Quantity != 480 ||
		created.Product.GTIN != w.product.GTIN || created.Product.MaxTempCelsius != 8 || created.Lot.LotNumber != w.lot.LotNumber ||
		created.Origin.GLN != w.plant.GLN || created.Destination.GLN != w.store.GLN || created.Destination.GeoFenceRadiusMeters != 200 ||
		created.OwnerTenantID != w.owner.ID || created.CarrierTenantID != w.carrier.ID || created.ConsigneeTenantID != w.consignee.ID ||
		created.AssignedDriverID != nil {
		t.Errorf("Create() = %+v", created)
	}
	roles := map[shipment.Role]string{}
	for _, p := range created.Participants {
		roles[p.Role] = p.TenantCode
	}
	if len(created.Participants) != 3 || roles[shipment.RoleOwner] != w.owner.Code || roles[shipment.RoleCarrier] != w.carrier.Code ||
		roles[shipment.RoleConsignee] != w.consignee.Code {
		t.Errorf("participants = %+v", created.Participants)
	}

	// The quantity left the origin with a SHIPMENT_CREATED movement.
	if got := w.balance(t, w.plant); got != 20 {
		t.Errorf("origin balance = %d, want 20", got)
	}
	var delta int
	if err := w.db.Owner.QueryRow(t.Context(), `SELECT quantity_delta FROM core.inventory_movements WHERE shipment_id = $1 AND reason = 'SHIPMENT_CREATED'`,
		created.ID).Scan(&delta); err != nil || delta != -480 {
		t.Errorf("movement = %d, %v; want -480", delta, err)
	}

	// shipment.created opens the log with the contract's payload.
	events := w.events(t, w.ownerManager, created.ID)
	var data struct {
		LotID        uuid.UUID `json:"lot_id"`
		Quantity     int       `json:"quantity"`
		Origin       struct{ GLN string }
		Participants []struct {
			TenantID uuid.UUID `json:"tenant_id"`
			Role     string
		}
		AssignedDriverID *uuid.UUID `json:"assigned_driver_id"`
	}
	if len(events) != 1 || events[0].Type != event.TypeCreated || events[0].Subject.SSCC != created.SSCC {
		t.Fatalf("events = %+v", events)
	}
	if err := json.Unmarshal(events[0].Data, &data); err != nil || data.LotID != w.lot.ID || data.Quantity != 480 ||
		data.Origin.GLN != w.plant.GLN || len(data.Participants) != 3 || data.AssignedDriverID != nil {
		t.Errorf("created data = %s, %v", events[0].Data, err)
	}

	// Every participant reads it, and nobody else.
	for _, p := range []identity.Principal{w.ownerManager, w.carrierManager, as(w.consignee.Admin)} {
		if got, err := w.svc.Get(t.Context(), p, created.ID); err != nil || got.ID != created.ID {
			t.Errorf("participant %s: Get() = %v", p.TenantID, err)
		}
	}
	if _, err := w.svc.Get(t.Context(), as(w.stranger.Admin), created.ID); !errors.Is(err, shipment.ErrNotFound) {
		t.Errorf("stranger: error = %v, want ErrNotFound", err)
	}

	// The rest of the stock does not cover another 30, and the SSCCs count up.
	if _, err := w.svc.Create(t.Context(), w.ownerManager, w.request(30)); !errors.Is(err, shipment.ErrInsufficientStock) {
		t.Errorf("30 of 20 left: error = %v, want ErrInsufficientStock", err)
	}
	second, err := w.svc.Create(t.Context(), w.ownerManager, w.request(20))
	if secondSSCC, _ := gs1.BuildSSCC(0, w.owner.GCP, 2); err != nil || second.SSCC != secondSSCC {
		t.Errorf("second shipment = %s, %v; want %s", second.SSCC, err, secondSSCC)
	}
}

func TestCreateRejectsWhatItCannotUse(t *testing.T) {
	w := newWorld(t)
	foreignLot := w.db.CommissionLot(t, w.stranger, w.db.CreateProduct(t, w.stranger), w.stranger.Headquarters, 10)
	closed := w.db.CreateLocation(t, w.owner)
	if _, err := w.db.Owner.Exec(t.Context(), `UPDATE core.locations SET is_active = false WHERE id = $1`, closed.ID); err != nil {
		t.Fatal(err)
	}
	inactiveDriver := w.db.CreateUser(t, w.owner.ID, identity.RoleDriver)
	if _, err := w.db.Owner.Exec(t.Context(), `UPDATE core.users SET is_active = false WHERE id = $1`, inactiveDriver.ID); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		change func(*shipment.NewShipment)
		field  string
	}{
		"lot of another tenant":         {func(n *shipment.NewShipment) { n.LotID = foreignLot.ID }, "lot_id"},
		"origin of another tenant":      {func(n *shipment.NewShipment) { n.OriginLocationID = w.store.ID }, "origin_location_id"},
		"inactive origin":               {func(n *shipment.NewShipment) { n.OriginLocationID = closed.ID }, "origin_location_id"},
		"unknown destination":           {func(n *shipment.NewShipment) { n.DestinationGLN = "4006381333931" }, "destination_gln"},
		"destination equal to origin":   {func(n *shipment.NewShipment) { n.DestinationGLN = w.plant.GLN }, "destination_gln"},
		"unknown carrier":               {func(n *shipment.NewShipment) { n.CarrierTenantCode = "NO_SUCH_CARRIER" }, "carrier_tenant_code"},
		"driver of an external carrier": {func(n *shipment.NewShipment) { n.DriverUserID = &w.carrierDriver.UserID }, "driver_user_id"},
		"inactive own driver": {func(n *shipment.NewShipment) {
			n.CarrierTenantCode, n.DriverUserID = "", &inactiveDriver.ID
		}, "driver_user_id"},
		"another tenant's driver": {func(n *shipment.NewShipment) {
			n.CarrierTenantCode, n.DriverUserID = "", &w.carrierDriver.UserID
		}, "driver_user_id"},
	}
	for name, tt := range tests {
		req := w.request(10)
		tt.change(&req)
		if _, err := w.svc.Create(t.Context(), w.ownerManager, req); !reference(err, tt.field) {
			t.Errorf("%s: error = %v, want an invalid %s", name, err, tt.field)
		}
	}
	if _, err := w.svc.Create(t.Context(), w.ownDriver, w.request(10)); !denied(err, policy.ReasonRole) {
		t.Errorf("driver: error = %v, want a role denial", err)
	}
	if got := w.balance(t, w.plant); got != 500 {
		t.Errorf("origin balance after rejected shipments = %d, want 500", got)
	}

	if _, err := w.db.Owner.Exec(t.Context(), `UPDATE core.lots SET status = 'RECALLED', recalled_at = now() WHERE id = $1`, w.lot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Create(t.Context(), w.ownerManager, w.request(10)); !denied(err, policy.ReasonLotRecalled) {
		t.Errorf("recalled lot: error = %v, want LOT_RECALLED", err)
	}
}

func TestSerialSpaceExhaustion(t *testing.T) {
	w := newWorld(t)
	// Fixture prefixes have ten digits, which leave 10^6 serial references.
	if _, err := w.db.Owner.Exec(t.Context(), `UPDATE core.tenants SET sscc_next_serial = 1000000 WHERE id = $1`, w.owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Create(t.Context(), w.ownerManager, w.request(10)); !errors.Is(err, shipment.ErrSerialSpaceExhausted) {
		t.Errorf("error = %v, want ErrSerialSpaceExhausted", err)
	}
	if got := w.balance(t, w.plant); got != 500 {
		t.Errorf("origin balance = %d, want the stock untouched", got)
	}
}

func TestHoldersShipOnward(t *testing.T) {
	w := newWorld(t)
	w.db.AddBalance(t, w.consignee, w.store, w.lot.ID, 60)
	retailer := w.db.CreateTenant(t)
	shop := w.db.CreateLocation(t, retailer)
	distributor := as(w.consignee.Admin)

	onward, err := w.svc.Create(t.Context(), distributor, shipment.NewShipment{
		LotID: w.lot.ID, Quantity: 25, OriginLocationID: w.store.ID, DestinationGLN: shop.GLN,
	})
	if err != nil || onward.OwnerTenantID != w.consignee.ID || onward.CarrierTenantID != w.consignee.ID ||
		onward.ConsigneeTenantID != retailer.ID || onward.Product.GTIN != w.product.GTIN {
		t.Fatalf("onward shipment = %+v, %v", onward, err)
	}
	if got := w.balance(t, w.store); got != 35 {
		t.Errorf("holder balance = %d, want 35", got)
	}
	// The lot owner takes no part in the onward hop.
	if _, err := w.svc.Get(t.Context(), w.ownerManager, onward.ID); !errors.Is(err, shipment.ErrNotFound) {
		t.Errorf("lot owner reading the onward shipment: error = %v, want ErrNotFound", err)
	}
}

func TestAssignCarrierAndDriver(t *testing.T) {
	w := newWorld(t)
	req := w.request(100)
	req.CarrierTenantCode, req.DriverUserID = "", &w.ownDriver.UserID
	inHouse, err := w.svc.Create(t.Context(), w.ownerManager, req)
	if err != nil || inHouse.CarrierTenantID != w.owner.ID || inHouse.AssignedDriverID == nil || *inHouse.AssignedDriverID != w.ownDriver.UserID {
		t.Fatalf("in-house shipment = %+v, %v", inHouse, err)
	}

	// Outsourcing to a carrier releases the owner's driver.
	outsourced, err := w.svc.AssignCarrier(t.Context(), w.ownerManager, inHouse.ID, w.carrier.Code)
	if err != nil || outsourced.CarrierTenantID != w.carrier.ID || outsourced.AssignedDriverID != nil {
		t.Fatalf("AssignCarrier() = %+v, %v", outsourced, err)
	}
	carriers := 0
	for _, p := range outsourced.Participants {
		if p.Role == shipment.RoleCarrier {
			carriers++
			if p.TenantID != w.carrier.ID || p.TenantCode != w.carrier.Code {
				t.Errorf("carrier participant = %+v", p)
			}
		}
	}
	if carriers != 1 {
		t.Errorf("%d carrier participants, want 1", carriers)
	}
	if _, err := w.svc.AssignCarrier(t.Context(), w.ownerManager, inHouse.ID, w.stranger.Code); !denied(err, policy.ReasonCheck) {
		t.Errorf("second carrier: error = %v, want the external carrier check to fail", err)
	}
	if _, err := w.svc.AssignCarrier(t.Context(), as(w.consignee.Admin), inHouse.ID, w.stranger.Code); !denied(err, policy.ReasonParty) {
		t.Errorf("consignee assigning a carrier: error = %v, want a party denial", err)
	}

	// The carrier assigns one of its drivers; the owner no longer can.
	if _, err := w.svc.AssignDriver(t.Context(), w.ownerManager, inHouse.ID, w.ownDriver.UserID); !denied(err, policy.ReasonParty) {
		t.Errorf("owner assigning a driver: error = %v, want a party denial", err)
	}
	if _, err := w.svc.AssignDriver(t.Context(), w.carrierManager, inHouse.ID, w.ownDriver.UserID); !reference(err, "driver_user_id") {
		t.Errorf("another tenant's driver: error = %v, want an invalid driver_user_id", err)
	}
	assigned, err := w.svc.AssignDriver(t.Context(), w.carrierManager, inHouse.ID, w.carrierDriver.UserID)
	if err != nil || assigned.AssignedDriverID == nil || *assigned.AssignedDriverID != w.carrierDriver.UserID {
		t.Fatalf("AssignDriver() = %+v, %v", assigned, err)
	}
	if _, err := w.svc.AssignDriver(t.Context(), w.carrierManager, inHouse.ID, w.carrierDriver.UserID); err != nil {
		t.Errorf("assigning the same driver again: %v", err)
	}

	// The driver now reads the shipment, and the log shows each change once.
	if _, err := w.svc.Get(t.Context(), w.carrierDriver, inHouse.ID); err != nil {
		t.Errorf("assigned driver: %v", err)
	}
	var types []event.Type
	for _, e := range w.events(t, w.carrierManager, inHouse.ID) {
		types = append(types, e.Type)
	}
	want := []event.Type{event.TypeCreated, event.TypeParticipantAdded, event.TypeDriverAssigned}
	if len(types) != len(want) || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Errorf("event types = %v, want %v", types, want)
	}
	if result, err := w.svc.Integrity(t.Context(), as(w.consignee.Admin), inHouse.ID); err != nil || !result.Valid || result.EventCount != 3 {
		t.Errorf("Integrity() = %+v, %v", result, err)
	}
}

func TestCancel(t *testing.T) {
	w := newWorld(t)
	created, err := w.svc.Create(t.Context(), w.ownerManager, w.request(200))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Cancel(t.Context(), as(w.consignee.Admin), created.ID, "not ours"); !denied(err, policy.ReasonParty) {
		t.Errorf("consignee cancelling: error = %v, want a party denial", err)
	}
	cancelled, err := w.svc.Cancel(t.Context(), w.ownerManager, created.ID, "Customer order withdrawn")
	if err != nil || cancelled.Status != policy.ShipmentCancelled || cancelled.CancelledAt == nil {
		t.Fatalf("Cancel() = %+v, %v", cancelled, err)
	}
	if got := w.balance(t, w.plant); got != 500 {
		t.Errorf("origin balance after cancelling = %d, want 500", got)
	}
	events := w.events(t, w.ownerManager, created.ID)
	if last := events[len(events)-1]; last.Type != event.TypeCancelled || last.Subject.Status != policy.ShipmentCancelled ||
		string(last.Data) != `{"reason": "Customer order withdrawn"}` {
		t.Errorf("last event = %+v %s", last, last.Data)
	}
	if _, err := w.svc.Cancel(t.Context(), w.ownerManager, created.ID, "again"); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("cancelling twice: error = %v, want an invalid state", err)
	}
}

func TestListsAndSummaries(t *testing.T) {
	w := newWorld(t)
	first, err := w.svc.Create(t.Context(), w.ownerManager, w.request(100))
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.svc.Create(t.Context(), w.ownerManager, w.request(100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AssignDriver(t.Context(), w.carrierManager, second.ID, w.carrierDriver.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Cancel(t.Context(), w.ownerManager, first.ID, "duplicate"); err != nil {
		t.Fatal(err)
	}

	list := func(p identity.Principal, f shipment.Filter) []uuid.UUID {
		t.Helper()
		f.Limit = 100
		shipments, err := w.svc.List(t.Context(), p, f)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		ids := make([]uuid.UUID, len(shipments))
		for i, s := range shipments {
			ids[i] = s.ID
		}
		return ids
	}
	if got := list(w.ownerManager, shipment.Filter{}); len(got) != 2 || got[0] != second.ID {
		t.Errorf("owner list = %v, want both, newest first", got)
	}
	carrierRole, ownerRole := shipment.RoleCarrier, shipment.RoleOwner
	if got := list(w.carrierManager, shipment.Filter{Party: &carrierRole}); len(got) != 2 {
		t.Errorf("carrier list = %v", got)
	}
	if got := list(w.carrierManager, shipment.Filter{Party: &ownerRole}); len(got) != 0 {
		t.Errorf("carrier as owner = %v, want none", got)
	}
	cancelled := policy.ShipmentCancelled
	if got := list(as(w.consignee.Admin), shipment.Filter{Status: &cancelled}); len(got) != 1 || got[0] != first.ID {
		t.Errorf("cancelled = %v", got)
	}
	if got := list(w.ownerManager, shipment.Filter{SSCC: second.SSCC, LotID: &w.lot.ID}); len(got) != 1 || got[0] != second.ID {
		t.Errorf("by SSCC = %v", got)
	}
	if got := list(as(w.stranger.Admin), shipment.Filter{}); len(got) != 0 {
		t.Errorf("stranger list = %v", got)
	}
	// Drivers see their assignments only, whatever they ask for.
	if got := list(w.carrierDriver, shipment.Filter{}); len(got) != 1 || got[0] != second.ID {
		t.Errorf("driver list = %v, want only the assigned shipment", got)
	}
	if _, err := w.svc.Get(t.Context(), w.carrierDriver, first.ID); !denied(err, policy.ReasonRole) {
		t.Errorf("driver reading an unassigned shipment: error = %v, want a role denial", err)
	}

	summary, err := w.svc.Summary(t.Context(), as(w.consignee.Admin))
	if err != nil || summary != (shipment.Summary{Created: 1, Cancelled: 1}) {
		t.Errorf("Summary() = %+v, %v", summary, err)
	}
	if summary, err := w.svc.Summary(t.Context(), w.carrierDriver); err != nil || summary != (shipment.Summary{Created: 1}) {
		t.Errorf("driver Summary() = %+v, %v", summary, err)
	}

	// Event pages continue after the last sequence.
	page, err := w.svc.Events(t.Context(), w.ownerManager, second.ID, event.Page{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Sequence != 1 {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if rest, err := w.svc.Events(t.Context(), w.ownerManager, second.ID, event.Page{After: 1, Limit: 10}); err != nil || len(rest) != 1 || rest[0].Sequence != 2 {
		t.Errorf("second page = %+v, %v", rest, err)
	}
}
