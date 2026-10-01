package inventory_test

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

	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// fakeInventory records the filter of the handler and returns canned results.
type fakeInventory struct {
	err       error
	listed    []inventory.Balance
	gotFilter inventory.Filter
}

func (f *fakeInventory) List(_ context.Context, _ identity.Principal, flt inventory.Filter) ([]inventory.Balance, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func call(t *testing.T, inv inventory.Inventory, query string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleWarehouseManager}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := inventory.NewHandler(inv, authenticate, slog.New(slog.DiscardHandler))
	router := httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/inventory"+query, nil))
	return rec
}

func balance(location, lot uuid.UUID) inventory.Balance {
	return inventory.Balance{Location: inventory.LocationRef{ID: location}, Lot: inventory.LotRef{ID: lot}, QuantityOnHand: 1}
}

func TestListPagesWithACompositeCursor(t *testing.T) {
	location, lots := uuid.New(), []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	fake := &fakeInventory{listed: []inventory.Balance{balance(location, lots[0]), balance(location, lots[1]), balance(location, lots[2])}}
	rec := call(t, fake, "?limit=2&location_id="+location.String())
	var page struct {
		Items      []inventory.Balance `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil || rec.Code != http.StatusOK || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("status = %d, page = %+v, %v", rec.Code, page, err)
	}
	if f := fake.gotFilter; f.Limit != 3 || f.LocationID == nil || *f.LocationID != location || f.LotID != nil || f.After != nil {
		t.Errorf("first filter = %+v", f)
	}

	// The cursor resumes after the last balance returned.
	call(t, fake, "?cursor="+*page.NextCursor+"&lot_id="+lots[1].String())
	if f := fake.gotFilter; f.After == nil || f.After.LocationID != location || f.After.LotID != lots[1] || f.LotID == nil || *f.LotID != lots[1] {
		t.Errorf("next filter = %+v, want to resume after the second balance", f)
	}
}

func TestListRejectsBadQueries(t *testing.T) {
	short := httpx.UUIDCursor(uuid.New()) // a cursor of another collection
	for query, field := range map[string]string{
		"?limit=0": "limit", "?cursor=" + short: "cursor", "?cursor=***": "cursor", "?location_id=7": "location_id", "?lot_id=x": "lot_id",
	} {
		rec := call(t, &fakeInventory{}, query)
		var p httpx.Problem
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != field {
			t.Errorf("%s: status = %d, problem = %+v; want 400 on %s", query, rec.Code, p, field)
		}
	}
}

func TestListErrors(t *testing.T) {
	if rec := call(t, &fakeInventory{}, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty inventory: status = %d, body %s", rec.Code, rec.Body)
	}
	denied := &policy.DenialError{Action: policy.ViewInventory, Reason: policy.ReasonRole}
	if rec := call(t, &fakeInventory{err: denied}, ""); rec.Code != http.StatusForbidden {
		t.Errorf("denied: status = %d, want 403", rec.Code)
	}
	if rec := call(t, &fakeInventory{err: errors.New("db down")}, ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("failure: status = %d, want 500", rec.Code)
	}
}

func TestServiceRequiresAManager(t *testing.T) {
	svc := inventory.NewService(nil) // a denied caller never reaches the store
	for _, role := range []identity.Role{identity.RoleDriver, identity.RoleInspector} {
		_, err := svc.List(t.Context(), identity.Principal{TenantID: uuid.New(), Role: role}, inventory.Filter{Limit: 10})
		var d *policy.DenialError
		if !errors.As(err, &d) || d.Reason != policy.ReasonRole {
			t.Errorf("%s: error = %v, want a role denial", role, err)
		}
	}
}
