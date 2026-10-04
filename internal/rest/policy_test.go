package rest_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

func TestDenied(t *testing.T) {
	tests := []struct {
		reason     policy.Reason
		wantStatus int
		wantCode   string
	}{
		{policy.ReasonShipmentRecalled, 409, "SHIPMENT_RECALLED_LOCKED"},
		{policy.ReasonInvalidState, 409, "INVALID_STATE_TRANSITION"},
		{policy.ReasonLotRecalled, 409, "LOT_RECALLED"},
		{policy.ReasonParty, 403, httpx.CodeForbidden},
		{policy.ReasonRole, 403, httpx.CodeForbidden},
		{policy.ReasonUnknownAction, 403, httpx.CodeForbidden},
		{policy.ReasonCheck, 403, httpx.CodeForbidden},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		denial := &policy.DenialError{Action: policy.ManageUsers, Reason: tt.reason}
		if !rest.Denied(rec, httptest.NewRequest(http.MethodPost, "/api/v1/users", nil), denial) {
			t.Fatalf("%s: Denied() = false", tt.reason)
		}
		var p httpx.Problem
		_ = json.NewDecoder(rec.Body).Decode(&p)
		if rec.Code != tt.wantStatus || p.Code != tt.wantCode {
			t.Errorf("%s: %d %s, want %d %s", tt.reason, rec.Code, p.Code, tt.wantStatus, tt.wantCode)
		}
	}
	if rest.Denied(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), errors.New("other")) {
		t.Error("Denied() handled an error that is not a denial")
	}
}
