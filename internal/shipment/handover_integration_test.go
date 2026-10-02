//go:build integration

package shipment_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/shipment"
)

// Fixture locations stand at 10.8, 106.65 with a 200 m geo-fence.
var (
	atTheDock = shipment.Position{Latitude: 10.8, Longitude: 106.65, AccuracyMeters: 8}
	farAway   = shipment.Position{Latitude: 10.81, Longitude: 106.65, AccuracyMeters: 8}
)

// readyForPickup creates a shipment carried by the carrier with its driver assigned.
func (w world) readyForPickup(t *testing.T) shipment.Shipment {
	t.Helper()
	created, err := w.svc.Create(t.Context(), w.ownerManager, w.request(100))
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := w.svc.AssignDriver(t.Context(), w.carrierManager, created.ID, w.carrierDriver.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return assigned
}

func (w world) issue(t *testing.T, id uuid.UUID) shipment.IssuedCode {
	t.Helper()
	issued, err := w.svc.IssuePickupCode(t.Context(), w.ownerManager, id)
	if err != nil {
		t.Fatalf("IssuePickupCode() error = %v", err)
	}
	return issued
}

func codeRefusal(err error, reason shipment.PickupCodeReason) (*shipment.PickupCodeError, bool) {
	var refused *shipment.PickupCodeError
	return refused, errors.As(err, &refused) && refused.Reason == reason
}

// wrongCode returns a valid-looking code other than code.
func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestIssuePickupCode(t *testing.T) {
	w := newWorld(t)
	created, err := w.svc.Create(t.Context(), w.ownerManager, w.request(100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.IssuePickupCode(t.Context(), w.ownerManager, created.ID); !denied(err, policy.ReasonCheck) {
		t.Errorf("without a driver: error = %v, want the driver check to fail", err)
	}
	if _, err := w.svc.AssignDriver(t.Context(), w.carrierManager, created.ID, w.carrierDriver.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.IssuePickupCode(t.Context(), w.carrierManager, created.ID); !denied(err, policy.ReasonParty) {
		t.Errorf("carrier issuing: error = %v, want a party denial", err)
	}

	issued := w.issue(t, created.ID)
	if len(issued.Code) != 6 || issued.AttemptsAllowed != 5 || !issued.ExpiresAt.Equal(w.clock.now.Add(15*time.Minute)) {
		t.Errorf("IssuePickupCode() = %+v", issued)
	}
	// Only HMAC-SHA256(pepper, shipment ID ‖ code) is stored, and a new code replaces the old one.
	second := w.issue(t, created.ID)
	rows, err := w.db.Owner.Query(t.Context(), `SELECT code_hash, invalidated_at IS NOT NULL FROM core.pickup_codes WHERE shipment_id = $1 ORDER BY created_at, id`, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var hashes [][]byte
	var invalidated []bool
	for rows.Next() {
		var hash []byte
		var ended bool
		if err := rows.Scan(&hash, &ended); err != nil {
			t.Fatal(err)
		}
		hashes, invalidated = append(hashes, hash), append(invalidated, ended)
	}
	mac := hmac.New(sha256.New, testPepper)
	mac.Write(created.ID[:])
	mac.Write([]byte(second.Code))
	if len(hashes) != 2 || !invalidated[0] || invalidated[1] || !bytes.Equal(hashes[1], mac.Sum(nil)) {
		t.Errorf("stored codes = %x, invalidated %v", hashes, invalidated)
	}
}

func TestConfirmPickup(t *testing.T) {
	w := newWorld(t)
	sh := w.readyForPickup(t)
	issued := w.issue(t, sh.ID)
	pickup := shipment.Pickup{SSCC: sh.SSCC, Code: issued.Code, Position: atTheDock}

	// The checks run in the documented order, and only a wrong code costs an attempt.
	otherDriver := as(w.db.CreateUser(t, w.carrier.ID, identity.RoleDriver))
	if _, err := w.svc.ConfirmPickup(t.Context(), otherDriver, sh.ID, pickup); !denied(err, policy.ReasonRole) {
		t.Errorf("unassigned driver: error = %v, want a role denial", err)
	}
	wrongSSCC := pickup
	wrongSSCC.SSCC = "089300010000000018"
	if _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, wrongSSCC); !errors.Is(err, shipment.ErrSSCCMismatch) {
		t.Errorf("wrong SSCC: error = %v, want ErrSSCCMismatch", err)
	}
	wrong := pickup
	wrong.Code = wrongCode(issued.Code)
	if refused, ok := codeRefusal(func() error { _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, wrong); return err }(), shipment.PickupCodeInvalid); !ok || refused.RemainingAttempts != 4 {
		t.Errorf("wrong code: refusal = %+v, want 4 attempts left", refused)
	}
	away := pickup
	away.Position = farAway
	var outside *shipment.OutsideGeofenceError
	if _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, away); !errors.As(err, &outside) ||
		outside.DistanceMeters < 1000 || outside.AllowedMeters != 208 {
		t.Errorf("away from the origin: error = %v", err)
	}

	picked, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, pickup)
	if err != nil || picked.Status != policy.ShipmentInTransit || picked.PickedUpAt == nil {
		t.Fatalf("ConfirmPickup() = %+v, %v", picked, err)
	}
	events := w.events(t, w.carrierDriver, sh.ID)
	last := events[len(events)-1]
	var data struct {
		DriverUserID   uuid.UUID         `json:"driver_user_id"`
		Position       shipment.Position `json:"position"`
		DistanceMeters float64           `json:"distance_meters"`
	}
	if err := json.Unmarshal(last.Data, &data); err != nil || last.Type != event.TypePickupConfirmed ||
		last.Subject.Status != policy.ShipmentInTransit || data.DriverUserID != w.carrierDriver.UserID ||
		data.Position != atTheDock || data.DistanceMeters != 0 {
		t.Errorf("last event = %+v, %s", last, last.Data)
	}
	// The code is spent, and the shipment has left CREATED.
	if _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, pickup); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("second pickup: error = %v, want an invalid state", err)
	}
	if _, err := w.svc.IssuePickupCode(t.Context(), w.ownerManager, sh.ID); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("code after pickup: error = %v, want an invalid state", err)
	}
	if _, err := w.svc.Cancel(t.Context(), w.ownerManager, sh.ID, "too late"); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("cancel after pickup: error = %v, want an invalid state", err)
	}
}

func TestPickupCodeAttemptsAndExpiry(t *testing.T) {
	w := newWorld(t)
	sh := w.readyForPickup(t)
	issued := w.issue(t, sh.ID)
	wrong := shipment.Pickup{SSCC: sh.SSCC, Code: wrongCode(issued.Code), Position: atTheDock}
	for left := 4; left >= 0; left-- {
		_, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, wrong)
		if refused, ok := codeRefusal(err, shipment.PickupCodeInvalid); !ok || refused.RemainingAttempts != left {
			t.Fatalf("attempt with %d left: error = %v", left, err)
		}
	}
	right := wrong
	right.Code = issued.Code
	if _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, right); err == nil {
		t.Fatal("a locked code released the shipment")
	} else if _, ok := codeRefusal(err, shipment.PickupCodeLocked); !ok {
		t.Errorf("locked code: error = %v, want LOCKED", err)
	}

	// A new code starts afresh, until it expires.
	right.Code = w.issue(t, sh.ID).Code
	w.clock.Advance(shipment.PickupCodeTTL)
	if _, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, right); err == nil {
		t.Fatal("an expired code released the shipment")
	} else if _, ok := codeRefusal(err, shipment.PickupCodeExpired); !ok {
		t.Errorf("expired code: error = %v, want EXPIRED", err)
	}

	// Replacing the driver ends the code that was issued for the previous one.
	right.Code = w.issue(t, sh.ID).Code
	replacement := as(w.db.CreateUser(t, w.carrier.ID, identity.RoleDriver))
	if _, err := w.svc.AssignDriver(t.Context(), w.carrierManager, sh.ID, replacement.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.ConfirmPickup(t.Context(), replacement, sh.ID, right); err == nil {
		t.Fatal("a code issued for the previous driver released the shipment")
	} else if _, ok := codeRefusal(err, shipment.PickupCodeExpired); !ok {
		t.Errorf("code of the previous driver: error = %v, want EXPIRED", err)
	}
}

// inTransit returns a shipment that the carrier's driver has picked up.
func (w world) inTransit(t *testing.T) shipment.Shipment {
	t.Helper()
	sh := w.readyForPickup(t)
	issued := w.issue(t, sh.ID)
	picked, err := w.svc.ConfirmPickup(t.Context(), w.carrierDriver, sh.ID, shipment.Pickup{SSCC: sh.SSCC, Code: issued.Code, Position: atTheDock})
	if err != nil {
		t.Fatalf("pickup: %v", err)
	}
	return picked
}

func TestCheckpoints(t *testing.T) {
	w := newWorld(t)
	sh := w.inTransit(t)
	hub := w.db.CreateLocation(t, w.stranger) // a facility of any tenant
	checkpoint := shipment.Checkpoint{SSCC: sh.SSCC, GLN: hub.GLN, Position: atTheDock}

	recorded, err := w.svc.RecordCheckpoint(t.Context(), w.carrierDriver, sh.ID, checkpoint)
	if err != nil || recorded.Status != policy.ShipmentInTransit {
		t.Fatalf("RecordCheckpoint() = %+v, %v", recorded, err)
	}
	events := w.events(t, w.ownerManager, sh.ID)
	last := events[len(events)-1]
	var data struct {
		Facility struct{ GLN, Name string }
		Position shipment.Position
	}
	if err := json.Unmarshal(last.Data, &data); err != nil || last.Type != event.TypeCheckpointRecorded || data.Facility.GLN != hub.GLN ||
		data.Facility.Name != "Fixture Warehouse" || data.Position != atTheDock {
		t.Errorf("checkpoint event = %+v %s", last, last.Data)
	}

	unknown := checkpoint
	unknown.GLN = "4006381333931"
	if _, err := w.svc.RecordCheckpoint(t.Context(), w.carrierDriver, sh.ID, unknown); !reference(err, "gln") {
		t.Errorf("unknown facility: error = %v, want an invalid gln", err)
	}
	away := checkpoint
	away.Position = farAway
	var outside *shipment.OutsideGeofenceError
	if _, err := w.svc.RecordCheckpoint(t.Context(), w.carrierDriver, sh.ID, away); !errors.As(err, &outside) {
		t.Errorf("away from the facility: error = %v", err)
	}
	if _, err := w.svc.RecordCheckpoint(t.Context(), w.carrierManager, sh.ID, checkpoint); !denied(err, policy.ReasonRole) {
		t.Errorf("carrier manager: error = %v, want a role denial", err)
	}
	created, err := w.svc.Create(t.Context(), w.ownerManager, w.request(10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RecordCheckpoint(t.Context(), w.carrierDriver, created.ID, checkpoint); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("before pickup: error = %v, want an invalid state", err)
	}
}

func TestConfirmDelivery(t *testing.T) {
	w := newWorld(t)
	sh := w.inTransit(t)
	receiver := as(w.db.CreateUser(t, w.consignee.ID, identity.RoleWarehouseManager))
	delivery := shipment.Delivery{SSCC: sh.SSCC, Position: atTheDock}

	if _, err := w.svc.ConfirmDelivery(t.Context(), w.ownerManager, sh.ID, delivery); !denied(err, policy.ReasonParty) {
		t.Errorf("owner confirming: error = %v, want a party denial", err)
	}
	away := delivery
	away.Position = farAway
	var outside *shipment.OutsideGeofenceError
	if _, err := w.svc.ConfirmDelivery(t.Context(), receiver, sh.ID, away); !errors.As(err, &outside) {
		t.Errorf("away from the destination: error = %v", err)
	}
	wrong := delivery
	wrong.SSCC = "089300010000000018"
	if _, err := w.svc.ConfirmDelivery(t.Context(), receiver, sh.ID, wrong); !errors.Is(err, shipment.ErrSSCCMismatch) {
		t.Errorf("wrong pallet: error = %v, want ErrSSCCMismatch", err)
	}

	delivered, err := w.svc.ConfirmDelivery(t.Context(), receiver, sh.ID, delivery)
	if err != nil || delivered.Status != policy.ShipmentDelivered || delivered.DeliveredAt == nil {
		t.Fatalf("ConfirmDelivery() = %+v, %v", delivered, err)
	}
	// The quantity lands on the consignee's balance at the destination, so it now holds the lot.
	if got := w.balance(t, w.store); got != 100 {
		t.Errorf("destination balance = %d, want 100", got)
	}
	var tenantID uuid.UUID
	if err := w.db.Owner.QueryRow(t.Context(), `SELECT tenant_id FROM core.inventory_movements WHERE shipment_id = $1 AND reason = 'SHIPMENT_DELIVERED'`,
		sh.ID).Scan(&tenantID); err != nil || tenantID != w.consignee.ID {
		t.Errorf("delivery movement tenant = %s, %v; want the consignee", tenantID, err)
	}
	events := w.events(t, receiver, sh.ID)
	if last := events[len(events)-1]; last.Type != event.TypeDeliveryConfirmed || last.Subject.Status != policy.ShipmentDelivered {
		t.Errorf("last event = %+v", last)
	}
	if result, err := w.svc.Integrity(t.Context(), w.carrierDriver, sh.ID); err != nil || !result.Valid || result.EventCount != len(events) {
		t.Errorf("Integrity() = %+v, %v", result, err)
	}
	if _, err := w.svc.ConfirmDelivery(t.Context(), receiver, sh.ID, delivery); !denied(err, policy.ReasonInvalidState) {
		t.Errorf("second delivery: error = %v, want an invalid state", err)
	}
}
