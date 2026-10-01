package lot_test

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

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/lot"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// fakeLots records the calls of the handler and returns canned results.
type fakeLots struct {
	err       error
	listed    []lot.Lot
	gotFilter lot.Filter
	gotNew    lot.NewLot
	gotID     uuid.UUID
}

func (f *fakeLots) List(_ context.Context, _ identity.Principal, flt lot.Filter) ([]lot.Lot, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func (f *fakeLots) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (lot.Lot, error) {
	f.gotID = id
	return lot.Lot{ID: id}, f.err
}

func (f *fakeLots) Commission(_ context.Context, _ identity.Principal, nl lot.NewLot) (lot.Lot, error) {
	f.gotNew = nl
	return lot.Lot{ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d95"), LotNumber: nl.LotNumber}, f.err
}

func call(t *testing.T, lots lot.Lots, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleWarehouseManager}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := lot.NewHandler(lots, authenticate, slog.New(slog.DiscardHandler))
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
	productID  = "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d94"
	locationID = "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d92"
)

func TestListLots(t *testing.T) {
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	fake := &fakeLots{listed: []lot.Lot{{ID: ids[1]}, {ID: ids[0]}}}
	rec := call(t, fake, http.MethodGet, "/api/v1/lots?limit=1&status=RECALLED&product_id="+productID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_cursor":"`+httpx.UUIDCursor(ids[1])+`"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if f := fake.gotFilter; f.Limit != 2 || f.Status == nil || *f.Status != policy.LotRecalled || f.ProductID == nil || f.ProductID.String() != productID {
		t.Errorf("filter = %+v", f)
	}
	for query, field := range map[string]string{"status=active": "status", "product_id=7": "product_id", "cursor=AAAA": "cursor"} {
		rec := call(t, &fakeLots{}, http.MethodGet, "/api/v1/lots?"+query, "")
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != field {
			t.Errorf("%s: status = %d, errors = %+v; want 400 on %s", query, rec.Code, p.Errors, field)
		}
	}
}

func TestCommissionLot(t *testing.T) {
	fake := &fakeLots{}
	rec := call(t, fake, http.MethodPost, "/api/v1/lots", `{"product_id":"`+productID+`","lot_number":" L2026-09.A ",
		"production_date":"2026-09-30","expiration_date":"2026-09-30","quantity_commissioned":500,"commissioned_location_id":"`+locationID+`"}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/lots/0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d95" {
		t.Fatalf("status = %d, Location = %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	nl := fake.gotNew
	if nl.ProductID.String() != productID || nl.LocationID.String() != locationID || nl.LotNumber != "L2026-09.A" || nl.Quantity != 500 ||
		nl.ProductionDate != calendar.New(2026, time.September, 30) || nl.ExpirationDate != nl.ProductionDate {
		t.Errorf("new lot = %+v", nl)
	}

	body := func(override string) string {
		fields := map[string]any{
			"product_id": productID, "lot_number": "L1", "production_date": "2026-09-30", "expiration_date": "2026-10-14",
			"quantity_commissioned": 500, "commissioned_location_id": locationID,
		}
		if err := json.Unmarshal([]byte(override), &fields); err != nil {
			t.Fatal(err)
		}
		for k, v := range fields {
			if v == nil {
				delete(fields, k)
			}
		}
		b, _ := json.Marshal(fields)
		return string(b)
	}
	tests := []struct {
		override, field, code string
	}{
		{`{"product_id":null}`, "product_id", httpx.FieldRequired},
		{`{"product_id":"milk"}`, "product_id", httpx.FieldInvalidFormat},
		{`{"lot_number":null}`, "lot_number", httpx.FieldRequired},
		{`{"lot_number":"L 1"}`, "lot_number", httpx.FieldInvalidFormat},
		{`{"lot_number":"L123456789012345678901"}`, "lot_number", httpx.FieldInvalidFormat},
		{`{"production_date":null}`, "production_date", httpx.FieldRequired},
		{`{"production_date":"30/09/2026"}`, "production_date", httpx.FieldInvalidFormat},
		{`{"expiration_date":"2026-09-29"}`, "expiration_date", httpx.FieldOutOfRange},
		{`{"quantity_commissioned":null}`, "quantity_commissioned", httpx.FieldRequired},
		{`{"quantity_commissioned":0}`, "quantity_commissioned", httpx.FieldOutOfRange},
		{`{"quantity_commissioned":2147483648}`, "quantity_commissioned", httpx.FieldOutOfRange},
		{`{"commissioned_location_id":null}`, "commissioned_location_id", httpx.FieldRequired},
		{`{"status":"RECALLED"}`, "status", httpx.FieldUnknown},
	}
	for _, tt := range tests {
		rec := call(t, &fakeLots{}, http.MethodPost, "/api/v1/lots", body(tt.override))
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %s on %s", tt.override, rec.Code, p.Errors, tt.code, tt.field)
		}
	}
	if rec := call(t, &fakeLots{}, http.MethodPost, "/api/v1/lots", body(`{"quantity_commissioned":1.5}`)); rec.Code != http.StatusBadRequest {
		t.Errorf("fractional quantity: status = %d, want 400", rec.Code)
	}
}

func TestLotErrors(t *testing.T) {
	valid := `{"product_id":"` + productID + `","lot_number":"L1","production_date":"2026-09-30","expiration_date":"2026-10-14",
		"quantity_commissioned":5,"commissioned_location_id":"` + locationID + `"}`
	check := func(c policy.Check) error {
		return &policy.DenialError{Action: policy.CommissionLot, Reason: policy.ReasonCheck, Check: c}
	}
	tests := []struct {
		err         error
		status      int
		field, code string
		problemCode string
	}{
		{check(policy.ProductOwned), http.StatusBadRequest, "product_id", httpx.FieldInvalidValue, httpx.CodeValidationFailed},
		{check(policy.LocationOwned), http.StatusBadRequest, "commissioned_location_id", httpx.FieldInvalidValue, httpx.CodeValidationFailed},
		{lot.ErrProductInactive, http.StatusBadRequest, "product_id", httpx.FieldInvalidValue, httpx.CodeValidationFailed},
		{lot.ErrLocationInactive, http.StatusBadRequest, "commissioned_location_id", httpx.FieldInvalidValue, httpx.CodeValidationFailed},
		{lot.ErrLotNumberTaken, http.StatusConflict, "lot_number", "ALREADY_REGISTERED", "IDENTIFIER_ALREADY_REGISTERED"},
	}
	for _, tt := range tests {
		rec := call(t, &fakeLots{err: tt.err}, http.MethodPost, "/api/v1/lots", valid)
		p := problem(t, rec)
		if rec.Code != tt.status || p.Code != tt.problemCode || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%v: status = %d, problem = %+v", tt.err, rec.Code, p)
		}
	}

	id := uuid.New().String()
	for _, tt := range []struct {
		err  error
		want int
	}{
		{&policy.DenialError{Action: policy.ViewLots, Reason: policy.ReasonRole}, http.StatusForbidden},
		{lot.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	} {
		for _, path := range []string{"/api/v1/lots", "/api/v1/lots/" + id} {
			if rec := call(t, &fakeLots{err: tt.err}, http.MethodGet, path, ""); rec.Code != tt.want {
				t.Errorf("GET %s with %v: status = %d, want %d", path, tt.err, rec.Code, tt.want)
			}
		}
	}
	if rec := call(t, &fakeLots{}, http.MethodGet, "/api/v1/lots/L1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}
