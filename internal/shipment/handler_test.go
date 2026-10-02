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
	err       error
	listed    []shipment.Shipment
	events    []event.Event
	gotNew    shipment.NewShipment
	gotFilter shipment.Filter
	gotPage   event.Page
	gotID     uuid.UUID
	gotArg    string
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
