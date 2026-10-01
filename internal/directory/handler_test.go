package directory_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/directory"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// fakeStore returns canned entries and records the keys it was asked for.
type fakeStore struct {
	err     error
	gotGLN  string
	gotCode string
}

func (f *fakeStore) LocationByGLN(_ context.Context, gln string) (directory.Location, error) {
	f.gotGLN = gln
	return directory.Location{GLN: gln, Tenant: directory.Tenant{Code: "SGFRESH"}}, f.err
}

func (f *fakeStore) TenantByCode(_ context.Context, code string) (directory.Tenant, error) {
	f.gotCode = code
	return directory.Tenant{Code: code}, f.err
}

func call(t *testing.T, store directory.Store, role identity.Role, path string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: role}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := directory.NewHandler(directory.NewService(store), authenticate, slog.New(slog.DiscardHandler))
	router := httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestEveryRoleLooksUpTheDirectory(t *testing.T) {
	for _, role := range identity.Roles {
		store := &fakeStore{}
		rec := call(t, store, role, "/api/v1/directory/locations/8930001000018")
		var l directory.Location
		if err := json.NewDecoder(rec.Body).Decode(&l); err != nil || rec.Code != http.StatusOK ||
			store.gotGLN != "8930001000018" || l.Tenant.Code != "SGFRESH" {
			t.Errorf("%s GLN lookup: status = %d, entry = %+v, %v", role, rec.Code, l, err)
		}
		if rec := call(t, store, role, "/api/v1/directory/tenants/sgfresh"); rec.Code != http.StatusOK || store.gotCode != "SGFRESH" {
			t.Errorf("%s tenant lookup: status = %d, code = %q, want the upper-case code", role, rec.Code, store.gotCode)
		}
	}
}

func TestLookupErrors(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, identity.RoleDriver, "/api/v1/directory/locations/8930001000019")
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != http.StatusUnprocessableEntity ||
		p.Code != "INVALID_GS1_IDENTIFIER" || p.Errors[0].Field != "gln" || p.Errors[0].Code != string(gs1.ReasonCheckDigit) {
		t.Errorf("mistyped GLN: status = %d, problem = %+v, %v", rec.Code, p, err)
	}
	if store.gotGLN != "" {
		t.Error("a mistyped GLN was looked up")
	}

	tests := []struct {
		err  error
		want int
	}{
		{directory.ErrNotFound, http.StatusNotFound},
		{&policy.DenialError{Action: policy.LookUpDirectory, Reason: policy.ReasonRole}, http.StatusForbidden},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, path := range []string{"/api/v1/directory/locations/8930001000018", "/api/v1/directory/tenants/SGFRESH"} {
			if rec := call(t, &fakeStore{err: tt.err}, identity.RoleAdmin, path); rec.Code != tt.want {
				t.Errorf("%s with %v: status = %d, want %d", path, tt.err, rec.Code, tt.want)
			}
		}
	}
}
