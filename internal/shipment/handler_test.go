package shipment_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/shipment"
)

var caller = identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleWarehouseManager}

// fakeShipments records the calls of the handler and returns canned results.
type fakeShipments struct {
	err           error
	listed        []shipment.Shipment
	events        []event.Event
	gotNew        shipment.NewShipment
	gotFilter     shipment.Filter
	gotPage       event.Page
	gotID         uuid.UUID
	gotArg        string
	gotPickup     shipment.Pickup
	gotCheckpoint shipment.Checkpoint
	gotDelivery   shipment.Delivery
}

func (f *fakeShipments) Create(_ context.Context, _ identity.Principal, ns shipment.NewShipment) (shipment.Shipment, error) {
	f.gotNew = ns
	return shipment.Shipment{ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7da0")}, f.err
}

func (f *fakeShipments) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (shipment.Shipment, error) {
	f.gotID = id
	return shipment.Shipment{ID: id}, f.err
}

func (f *fakeShipments) List(_ context.Context, _ identity.Principal, flt shipment.Filter) ([]shipment.Shipment, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func (f *fakeShipments) Summary(context.Context, identity.Principal) (shipment.Summary, error) {
	return shipment.Summary{Created: 2, InTransit: 1}, f.err
}

func (f *fakeShipments) AssignCarrier(_ context.Context, _ identity.Principal, id uuid.UUID, code string) (shipment.Shipment, error) {
	f.gotID, f.gotArg = id, code
	return shipment.Shipment{ID: id}, f.err
}

func (f *fakeShipments) AssignDriver(_ context.Context, _ identity.Principal, id, driverID uuid.UUID) (shipment.Shipment, error) {
	f.gotID, f.gotArg = id, driverID.String()
	return shipment.Shipment{ID: id}, f.err
}

func (f *fakeShipments) Cancel(_ context.Context, _ identity.Principal, id uuid.UUID, reason string) (shipment.Shipment, error) {
	f.gotID, f.gotArg = id, reason
	return shipment.Shipment{ID: id, Status: policy.ShipmentCancelled}, f.err
}

func (f *fakeShipments) IssuePickupCode(_ context.Context, _ identity.Principal, id uuid.UUID) (shipment.IssuedCode, error) {
	f.gotID = id
	return shipment.IssuedCode{Code: "042917", ExpiresAt: time.Date(2026, 10, 2, 8, 15, 0, 0, time.UTC), AttemptsAllowed: 5}, f.err
}

func (f *fakeShipments) ConfirmPickup(_ context.Context, _ identity.Principal, id uuid.UUID, pickup shipment.Pickup) (shipment.Shipment, error) {
	f.gotID, f.gotPickup = id, pickup
	return shipment.Shipment{ID: id, Status: policy.ShipmentInTransit}, f.err
}

func (f *fakeShipments) RecordCheckpoint(_ context.Context, _ identity.Principal, id uuid.UUID, cp shipment.Checkpoint) (shipment.Shipment, error) {
	f.gotID, f.gotCheckpoint = id, cp
	return shipment.Shipment{ID: id, Status: policy.ShipmentInTransit}, f.err
}

func (f *fakeShipments) ConfirmDelivery(_ context.Context, _ identity.Principal, id uuid.UUID, d shipment.Delivery) (shipment.Shipment, error) {
	f.gotID, f.gotDelivery = id, d
	return shipment.Shipment{ID: id, Status: policy.ShipmentDelivered}, f.err
}

func (f *fakeShipments) Events(_ context.Context, _ identity.Principal, id uuid.UUID, page event.Page) ([]event.Event, error) {
	f.gotID, f.gotPage = id, page
	return f.events, f.err
}

func (f *fakeShipments) Integrity(_ context.Context, _ identity.Principal, id uuid.UUID) (event.Integrity, error) {
	f.gotID = id
	first := 2
	return event.Integrity{Valid: false, EventCount: 3, FirstInvalidSequence: &first}, f.err
}

func call(t *testing.T, shipments shipment.Shipments, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), caller)))
		})
	}
	h := shipment.NewHandler(shipments, authenticate, slog.New(slog.DiscardHandler))
	router := httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem (status %d): %v", rec.Code, err)
	}
	return p
}

const (
	lotID      = "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d95"
	locationID = "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d92"
	sscc       = "089300010000000018"
)

func TestListShipments(t *testing.T) {
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	fake := &fakeShipments{listed: []shipment.Shipment{{ID: ids[1]}, {ID: ids[0]}}}
	rec := call(t, fake, http.MethodGet, "/api/v1/shipments?limit=1&status=IN_TRANSIT&party=CONSIGNEE&sscc="+sscc+
		"&lot_id="+lotID+"&assigned_to_me=true", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_cursor":"`+httpx.UUIDCursor(ids[1])+`"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	f := fake.gotFilter
	if f.Limit != 2 || f.Status == nil || *f.Status != policy.ShipmentInTransit || f.Party == nil || *f.Party != shipment.RoleConsignee ||
		f.SSCC != sscc || f.LotID == nil || f.LotID.String() != lotID || f.DriverID == nil || *f.DriverID != caller.UserID {
		t.Errorf("filter = %+v", f)
	}

	for query, want := range map[string]struct {
		field  string
		status int
	}{
		"status=LOST":                      {"status", http.StatusBadRequest},
		"party=DRIVER":                     {"party", http.StatusBadRequest},
		"sscc=089300010000000019":          {"sscc", http.StatusUnprocessableEntity},
		"lot_id=7":                         {"lot_id", http.StatusBadRequest},
		"assigned_to_me=sometimes":         {"assigned_to_me", http.StatusBadRequest},
		"cursor=" + strings.Repeat("A", 5): {"cursor", http.StatusBadRequest},
	} {
		rec := call(t, &fakeShipments{}, http.MethodGet, "/api/v1/shipments?"+query, "")
		if p := problem(t, rec); rec.Code != want.status || len(p.Errors) != 1 || p.Errors[0].Field != want.field {
			t.Errorf("%s: status = %d, errors = %+v; want %d on %s", query, rec.Code, p.Errors, want.status, want.field)
		}
	}

	rec = call(t, &fakeShipments{}, http.MethodGet, "/api/v1/shipments/summary", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":2,"in_transit":1,"delivered":0`) {
		t.Errorf("summary: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestCreateShipment(t *testing.T) {
	fake := &fakeShipments{}
	rec := call(t, fake, http.MethodPost, "/api/v1/shipments", `{"lot_id":"`+lotID+`","quantity":480,
		"origin_location_id":"`+locationID+`","destination_gln":" 8934567000017 ","carrier_tenant_code":" mekong_logistics ",
		"driver_user_id":""}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/shipments/0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7da0" {
		t.Fatalf("status = %d, Location = %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	ns := fake.gotNew
	if ns.LotID.String() != lotID || ns.Quantity != 480 || ns.OriginLocationID.String() != locationID ||
		ns.DestinationGLN != "8934567000017" || ns.CarrierTenantCode != "MEKONG_LOGISTICS" || ns.DriverUserID != nil {
		t.Errorf("new shipment = %+v", ns)
	}

	valid := map[string]any{"lot_id": lotID, "quantity": 1, "origin_location_id": locationID, "destination_gln": "8934567000017"}
	tests := []struct {
		change map[string]any
		field  string
		code   string
		status int
	}{
		{map[string]any{"lot_id": nil}, "lot_id", httpx.FieldRequired, http.StatusBadRequest},
		{map[string]any{"quantity": nil}, "quantity", httpx.FieldRequired, http.StatusBadRequest},
		{map[string]any{"quantity": 0}, "quantity", httpx.FieldOutOfRange, http.StatusBadRequest},
		{map[string]any{"origin_location_id": "x"}, "origin_location_id", httpx.FieldInvalidFormat, http.StatusBadRequest},
		{map[string]any{"destination_gln": nil}, "destination_gln", httpx.FieldRequired, http.StatusBadRequest},
		{map[string]any{"destination_gln": "8934567000018"}, "destination_gln", string(gs1.ReasonCheckDigit), http.StatusUnprocessableEntity},
		{map[string]any{"driver_user_id": "driver-7"}, "driver_user_id", httpx.FieldInvalidFormat, http.StatusBadRequest},
		{map[string]any{"sscc": sscc}, "sscc", httpx.FieldUnknown, http.StatusBadRequest},
	}
	for _, tt := range tests {
		body := map[string]any{}
		for k, v := range valid {
			body[k] = v
		}
		for k, v := range tt.change {
			if v == nil {
				delete(body, k)
			} else {
				body[k] = v
			}
		}
		b, _ := json.Marshal(body)
		rec := call(t, &fakeShipments{}, http.MethodPost, "/api/v1/shipments", string(b))
		p := problem(t, rec)
		if rec.Code != tt.status || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%v: status = %d, errors = %+v; want %d with %s on %s", tt.change, rec.Code, p.Errors, tt.status, tt.code, tt.field)
		}
	}
}

func TestCommands(t *testing.T) {
	id := uuid.New()
	fake := &fakeShipments{}
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/carrier", `{"carrier_tenant_code":"mekong"}`); rec.Code != http.StatusOK ||
		fake.gotID != id || fake.gotArg != "MEKONG" {
		t.Errorf("carrier: status = %d, call %s %q", rec.Code, fake.gotID, fake.gotArg)
	}
	driver := uuid.New()
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/driver", `{"driver_user_id":"`+driver.String()+`"}`); rec.Code != http.StatusOK ||
		fake.gotArg != driver.String() {
		t.Errorf("driver: status = %d, call %q", rec.Code, fake.gotArg)
	}
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/cancel", `{"reason":" Order withdrawn "}`); rec.Code != http.StatusOK ||
		fake.gotArg != "Order withdrawn" || !strings.Contains(rec.Body.String(), `"status":"CANCELLED"`) {
		t.Errorf("cancel: status = %d, reason %q, body %s", rec.Code, fake.gotArg, rec.Body)
	}

	for path, body := range map[string]string{
		"/carrier": `{}`, "/driver": `{"driver_user_id":"7"}`, "/cancel": `{"reason":"` + strings.Repeat("r", 1001) + `"}`,
	} {
		if rec := call(t, &fakeShipments{}, http.MethodPost, "/api/v1/shipments/"+id.String()+path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with %s: status = %d, want 400", path, body[:min(len(body), 30)], rec.Code)
		}
	}
	if rec := call(t, &fakeShipments{}, http.MethodPost, "/api/v1/shipments/42/cancel", `{"reason":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}

func TestEventsAndIntegrity(t *testing.T) {
	id := uuid.New()
	fake := &fakeShipments{events: []event.Event{{Sequence: 3}, {Sequence: 4}, {Sequence: 5}}}
	rec := call(t, fake, http.MethodGet, "/api/v1/shipments/"+id.String()+"/events?limit=2", "")
	var page struct {
		Items      []event.Event `json:"items"`
		NextCursor *string       `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil || rec.Code != http.StatusOK || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("events: status = %d, page = %+v, %v", rec.Code, page, err)
	}
	if fake.gotPage != (event.Page{After: 0, Limit: 3}) {
		t.Errorf("first page = %+v", fake.gotPage)
	}
	call(t, fake, http.MethodGet, "/api/v1/shipments/"+id.String()+"/events?cursor="+*page.NextCursor, "")
	if fake.gotPage.After != 4 {
		t.Errorf("next page starts after %d, want 4", fake.gotPage.After)
	}
	for _, cursor := range []string{"AAAA", "MA", "***"} {
		if rec := call(t, fake, http.MethodGet, "/api/v1/shipments/"+id.String()+"/events?cursor="+cursor, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("cursor %s: status = %d, want 400", cursor, rec.Code)
		}
	}

	rec = call(t, &fakeShipments{}, http.MethodGet, "/api/v1/shipments/"+id.String()+"/integrity", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":false,"event_count":3,"head_hash":null,"first_invalid_sequence":2`) {
		t.Errorf("integrity: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestShipmentErrors(t *testing.T) {
	id := uuid.New().String()
	create := `{"lot_id":"` + lotID + `","quantity":1,"origin_location_id":"` + locationID + `","destination_gln":"8934567000017"}`
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&shipment.InvalidReferenceError{Field: "lot_id", Message: "is not a lot that your tenant holds"}, http.StatusBadRequest, httpx.CodeValidationFailed},
		{shipment.ErrInsufficientStock, http.StatusConflict, "INSUFFICIENT_STOCK"},
		{shipment.ErrSerialSpaceExhausted, http.StatusConflict, "SSCC_SERIAL_EXHAUSTED"},
		{&policy.DenialError{Action: policy.CreateShipment, Reason: policy.ReasonLotRecalled}, http.StatusConflict, "LOT_RECALLED"},
		{&policy.DenialError{Action: policy.AssignCarrier, Reason: policy.ReasonCheck, Check: policy.NoExternalCarrier}, http.StatusConflict, "INVALID_STATE_TRANSITION"},
		{&policy.DenialError{Action: policy.CancelShipment, Reason: policy.ReasonInvalidState}, http.StatusConflict, "INVALID_STATE_TRANSITION"},
		{&policy.DenialError{Action: policy.CancelShipment, Reason: policy.ReasonShipmentRecalled}, http.StatusConflict, "SHIPMENT_RECALLED_LOCKED"},
		{&policy.DenialError{Action: policy.ViewShipment, Reason: policy.ReasonRole}, http.StatusForbidden, httpx.CodeForbidden},
		{shipment.ErrNotFound, http.StatusNotFound, httpx.CodeNotFound},
		{errors.New("db down"), http.StatusInternalServerError, httpx.CodeInternalError},
	}
	for _, tt := range tests {
		rec := call(t, &fakeShipments{err: tt.err}, http.MethodPost, "/api/v1/shipments", create)
		if p := problem(t, rec); rec.Code != tt.status || p.Code != tt.code {
			t.Errorf("%v: status = %d, code = %s; want %d %s", tt.err, rec.Code, p.Code, tt.status, tt.code)
		}
	}
	rec := call(t, &fakeShipments{err: &shipment.InvalidReferenceError{Field: "destination_gln", Message: "is not listed"}}, http.MethodPost, "/api/v1/shipments", create)
	if p := problem(t, rec); len(p.Errors) != 1 || p.Errors[0].Field != "destination_gln" || p.Errors[0].Code != httpx.FieldInvalidValue {
		t.Errorf("invalid reference: problem = %+v", p)
	}
	for _, path := range []string{"/api/v1/shipments/" + id, "/api/v1/shipments/" + id + "/events", "/api/v1/shipments/" + id + "/integrity", "/api/v1/shipments", "/api/v1/shipments/summary"} {
		if rec := call(t, &fakeShipments{err: shipment.ErrNotFound}, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestPickupEndpoints(t *testing.T) {
	id := uuid.New()
	fake := &fakeShipments{}
	rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/pickup-code", "")
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(rec.Body.String(), `"code":"042917","expires_at":"2026-10-02T08:15:00Z","attempts_allowed":5`) {
		t.Errorf("issue: status = %d, headers %v, body %s", rec.Code, rec.Header(), rec.Body)
	}

	body := `{"sscc":" ` + sscc + ` ","code":"042917","position":{"latitude":10.80000049,"longitude":106.65,"accuracy_meters":12.34}}`
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/pickup", body); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"status":"IN_TRANSIT"`) {
		t.Fatalf("pickup: status = %d, body %s", rec.Code, rec.Body)
	}
	if p := fake.gotPickup; p.SSCC != sscc || p.Code != "042917" || p.Position != (shipment.Position{Latitude: 10.8, Longitude: 106.65, AccuracyMeters: 12.3}) {
		t.Errorf("pickup = %+v", p)
	}

	tests := []struct {
		body, field, code string
		status            int
	}{
		{`{"code":"042917","position":{"latitude":1,"longitude":1,"accuracy_meters":1}}`, "sscc", httpx.FieldRequired, http.StatusBadRequest},
		{`{"sscc":"089300010000000017","code":"042917","position":{"latitude":1,"longitude":1,"accuracy_meters":1}}`, "sscc", string(gs1.ReasonCheckDigit), http.StatusUnprocessableEntity},
		{`{"sscc":"` + sscc + `","code":"42917","position":{"latitude":1,"longitude":1,"accuracy_meters":1}}`, "code", httpx.FieldInvalidFormat, http.StatusBadRequest},
		{`{"sscc":"` + sscc + `","code":"042917"}`, "position", httpx.FieldRequired, http.StatusBadRequest},
		{`{"sscc":"` + sscc + `","code":"042917","position":{"latitude":91,"longitude":1,"accuracy_meters":1}}`, "position.latitude", httpx.FieldOutOfRange, http.StatusBadRequest},
		{`{"sscc":"` + sscc + `","code":"042917","position":{"latitude":1,"longitude":1}}`, "position.accuracy_meters", httpx.FieldRequired, http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := call(t, &fakeShipments{}, http.MethodPost, "/api/v1/shipments/"+id.String()+"/pickup", tt.body)
		p := problem(t, rec)
		if rec.Code != tt.status || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %d with %s on %s", tt.body, rec.Code, p.Errors, tt.status, tt.code, tt.field)
		}
	}
}

func TestHandoverProblems(t *testing.T) {
	path := "/api/v1/shipments/" + uuid.New().String() + "/pickup"
	body := `{"sscc":"` + sscc + `","code":"042917","position":{"latitude":10.8,"longitude":106.65,"accuracy_meters":8}}`
	tests := []struct {
		err        error
		status     int
		code       string
		extensions map[string]any
	}{
		{shipment.ErrSSCCMismatch, http.StatusUnprocessableEntity, "SSCC_MISMATCH", nil},
		{&shipment.PickupCodeError{Reason: shipment.PickupCodeInvalid, RemainingAttempts: 3}, http.StatusUnprocessableEntity, "PICKUP_CODE_INVALID",
			map[string]any{"remaining_attempts": 3.0}},
		{&shipment.PickupCodeError{Reason: shipment.PickupCodeExpired}, http.StatusUnprocessableEntity, "PICKUP_CODE_EXPIRED", nil},
		{&shipment.PickupCodeError{Reason: shipment.PickupCodeLocked}, http.StatusLocked, "PICKUP_CODE_LOCKED", nil},
		{&shipment.OutsideGeofenceError{DistanceMeters: 1112.2, AllowedMeters: 208}, http.StatusUnprocessableEntity, "OUTSIDE_GEOFENCE",
			map[string]any{"distance_meters": 1112.2, "allowed_meters": 208.0}},
		{&policy.DenialError{Action: policy.ConfirmPickup, Reason: policy.ReasonShipmentRecalled}, http.StatusConflict, "SHIPMENT_RECALLED_LOCKED", nil},
	}
	for _, tt := range tests {
		rec := call(t, &fakeShipments{err: tt.err}, http.MethodPost, path, body)
		var got map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tt.status || got["code"] != tt.code {
			t.Errorf("%v: status = %d, problem = %v", tt.err, rec.Code, got)
		}
		for member, want := range tt.extensions {
			if got[member] != want {
				t.Errorf("%v: %s = %v, want %v", tt.err, member, got[member], want)
			}
		}
	}
	rec := call(t, &fakeShipments{err: &policy.DenialError{Action: policy.IssuePickupCode, Reason: policy.ReasonCheck, Check: policy.DriverAssigned}},
		http.MethodPost, "/api/v1/shipments/"+uuid.New().String()+"/pickup-code", "")
	if p := problem(t, rec); rec.Code != http.StatusConflict || p.Code != "INVALID_STATE_TRANSITION" {
		t.Errorf("code without a driver: status = %d, problem = %+v", rec.Code, p)
	}
}

func TestCheckpointAndDeliveryEndpoints(t *testing.T) {
	id := uuid.New()
	position := `"position":{"latitude":10.8,"longitude":106.65,"accuracy_meters":8}`
	fake := &fakeShipments{}
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/checkpoints",
		`{"sscc":"`+sscc+`","gln":" 8934567000017 ",`+position+`}`); rec.Code != http.StatusOK {
		t.Fatalf("checkpoint: status = %d, body %s", rec.Code, rec.Body)
	}
	if cp := fake.gotCheckpoint; cp.SSCC != sscc || cp.GLN != "8934567000017" || cp.Position.AccuracyMeters != 8 {
		t.Errorf("checkpoint = %+v", cp)
	}
	if rec := call(t, fake, http.MethodPost, "/api/v1/shipments/"+id.String()+"/delivery", `{"sscc":"`+sscc+`",`+position+`}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"status":"DELIVERED"`) {
		t.Fatalf("delivery: status = %d, body %s", rec.Code, rec.Body)
	}
	if d := fake.gotDelivery; d.SSCC != sscc || d.Position.Latitude != 10.8 {
		t.Errorf("delivery = %+v", d)
	}

	tests := []struct {
		path, body, field, code string
		status                  int
	}{
		{"/checkpoints", `{"sscc":"` + sscc + `",` + position + `}`, "gln", httpx.FieldRequired, http.StatusBadRequest},
		{"/checkpoints", `{"sscc":"` + sscc + `","gln":"8934567000018",` + position + `}`, "gln", string(gs1.ReasonCheckDigit), http.StatusUnprocessableEntity},
		{"/delivery", `{` + position + `}`, "sscc", httpx.FieldRequired, http.StatusBadRequest},
		{"/delivery", `{"sscc":"` + sscc + `","position":{"latitude":10.8,"longitude":200,"accuracy_meters":8}}`, "position.longitude", httpx.FieldOutOfRange, http.StatusBadRequest},
		{"/delivery", `{"sscc":"` + sscc + `",` + position + `,"code":"042917"}`, "code", httpx.FieldUnknown, http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := call(t, &fakeShipments{}, http.MethodPost, "/api/v1/shipments/"+id.String()+tt.path, tt.body)
		p := problem(t, rec)
		if rec.Code != tt.status || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s %s: status = %d, errors = %+v; want %d with %s on %s", tt.path, tt.body, rec.Code, p.Errors, tt.status, tt.code, tt.field)
		}
	}
	rec := call(t, &fakeShipments{err: &shipment.OutsideGeofenceError{DistanceMeters: 400, AllowedMeters: 250}}, http.MethodPost,
		"/api/v1/shipments/"+id.String()+"/delivery", `{"sscc":"`+sscc+`",`+position+`}`)
	if p := problem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != "OUTSIDE_GEOFENCE" {
		t.Errorf("outside the destination: status = %d, problem = %+v", rec.Code, p)
	}
}
