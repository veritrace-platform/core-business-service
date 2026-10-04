package recall_test

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

	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/recall"
)

type fakeRecaller struct {
	err       error
	gotLot    uuid.UUID
	gotReason string
}

func (f *fakeRecaller) Recall(_ context.Context, p identity.Principal, lotID uuid.UUID, reason string) (recall.Recall, error) {
	f.gotLot, f.gotReason = lotID, reason
	return recall.Recall{
		ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7dc1"), LotID: lotID, Reason: reason, AffectedShipmentCount: 4,
		InitiatedBy: p.UserID, CreatedAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
	}, f.err
}

func post(t *testing.T, recaller recall.Recaller, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleAdmin}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := recall.NewHandler(recaller, authenticate, slog.New(slog.DiscardHandler))
	router := httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRecallEndpoint(t *testing.T) {
	lot := uuid.New()
	fake := &fakeRecaller{}
	rec := post(t, fake, "/api/v1/lots/"+lot.String()+"/recall", `{"reason":" Supplier reported contamination "}`)
	if rec.Code != http.StatusCreated || fake.gotLot != lot || fake.gotReason != "Supplier reported contamination" ||
		!strings.Contains(rec.Body.String(), `"affected_shipment_count":4`) {
		t.Errorf("recall: status = %d, call %s %q, body %s", rec.Code, fake.gotLot, fake.gotReason, rec.Body)
	}

	for _, body := range []string{`{}`, `{"reason":"  "}`, `{"reason":"` + strings.Repeat("r", 1001) + `"}`, `{"reason":"x","lot":"y"}`} {
		if rec := post(t, &fakeRecaller{}, "/api/v1/lots/"+lot.String()+"/recall", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%.40s: status = %d, want 400", body, rec.Code)
		}
	}
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&policy.DenialError{Action: policy.RecallLot, Reason: policy.ReasonRole}, http.StatusForbidden, httpx.CodeForbidden},
		{&policy.DenialError{Action: policy.RecallLot, Reason: policy.ReasonInvalidState}, http.StatusConflict, "INVALID_STATE_TRANSITION"},
		{recall.ErrNotFound, http.StatusNotFound, httpx.CodeNotFound},
		{errors.New("db down"), http.StatusInternalServerError, httpx.CodeInternalError},
	}
	for _, tt := range tests {
		rec := post(t, &fakeRecaller{err: tt.err}, "/api/v1/lots/"+lot.String()+"/recall", `{"reason":"x"}`)
		var p httpx.Problem
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != tt.status || p.Code != tt.code {
			t.Errorf("%v: status = %d, problem = %+v, %v", tt.err, rec.Code, p, err)
		}
	}
	if rec := post(t, &fakeRecaller{}, "/api/v1/lots/L1/recall", `{"reason":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}
