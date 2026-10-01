package tenant_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
)

type fakeProfiles struct {
	err       error
	gotPatch  tenant.ProfilePatch
	principal identity.Principal
}

func (f *fakeProfiles) Profile(_ context.Context, p identity.Principal) (tenant.Tenant, error) {
	f.principal = p
	return tenant.Tenant{ID: p.TenantID, Code: "SGFRESH"}, f.err
}

func (f *fakeProfiles) UpdateProfile(_ context.Context, p identity.Principal, patch tenant.ProfilePatch) (tenant.Tenant, error) {
	f.principal, f.gotPatch = p, patch
	return tenant.Tenant{ID: p.TenantID, Code: "SGFRESH"}, f.err
}

var admin = identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleAdmin, SessionID: uuid.New()}

func send(t *testing.T, router http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/tenant", strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGetProfile(t *testing.T) {
	fake := &fakeProfiles{}
	rec := send(t, newProfileRouter(&fakeRegisterer{}, fake, &admin), http.MethodGet, "")
	var got tenant.Tenant
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || rec.Code != http.StatusOK || got.ID != admin.TenantID ||
		fake.principal != admin {
		t.Errorf("status = %d, tenant = %+v", rec.Code, got)
	}

	rec = send(t, newProfileRouter(&fakeRegisterer{}, fake, nil), http.MethodGet, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without a principal: status = %d, want 401", rec.Code)
	}
}

func TestUpdateProfile(t *testing.T) {
	fake := &fakeProfiles{}
	rec := send(t, newProfileRouter(&fakeRegisterer{}, fake, &admin), http.MethodPatch,
		`{"legal_name":"  Saigon Fresh Foods JSC ","sscc_extension_digit":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if fake.gotPatch.LegalName == nil || *fake.gotPatch.LegalName != "Saigon Fresh Foods JSC" ||
		fake.gotPatch.SSCCExtensionDigit == nil || *fake.gotPatch.SSCCExtensionDigit != 3 {
		t.Errorf("patch = %+v", fake.gotPatch)
	}

	tests := []struct {
		body      string
		wantField string
		wantCode  string
	}{
		{`{"sscc_extension_digit":10}`, "sscc_extension_digit", httpx.FieldOutOfRange},
		{`{"sscc_extension_digit":null}`, "sscc_extension_digit", httpx.FieldRequired},
		{`{"legal_name":null}`, "legal_name", httpx.FieldRequired},
		{`{"legal_name":"   "}`, "legal_name", httpx.FieldRequired},
		{`{"gs1_company_prefix":"8930002"}`, "gs1_company_prefix", httpx.FieldUnknown},
		{`{"code":"OTHER"}`, "code", httpx.FieldUnknown},
		{`{"sscc_extension_digit":"3"}`, "sscc_extension_digit", httpx.FieldInvalidType},
	}
	for _, tt := range tests {
		rec := send(t, newProfileRouter(&fakeRegisterer{}, &fakeProfiles{}, &admin), http.MethodPatch, tt.body)
		var p httpx.Problem
		_ = json.NewDecoder(rec.Body).Decode(&p)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.wantField ||
			p.Errors[0].Code != tt.wantCode {
			t.Errorf("%s: status = %d, errors = %+v", tt.body, rec.Code, p.Errors)
		}
	}
}

func TestProfileErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&policy.DenialError{Action: policy.ManageTenantProfile, Reason: policy.ReasonRole}, http.StatusForbidden},
		{tenant.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			body := ""
			if method == http.MethodPatch {
				body = `{}`
			}
			rec := send(t, newProfileRouter(&fakeRegisterer{}, &fakeProfiles{err: tt.err}, &admin), method, body)
			if rec.Code != tt.want {
				t.Errorf("%s with %v: status = %d, want %d", method, tt.err, rec.Code, tt.want)
			}
		}
	}
}

type fakeProfileStore struct {
	got tenant.ProfilePatch
}

func (f *fakeProfileStore) WithTenantTx(_ context.Context, _ uuid.UUID, fn func(tenant.ProfileRepository) error) error {
	return fn(f)
}

func (f *fakeProfileStore) Get(_ context.Context, id uuid.UUID) (tenant.Tenant, error) {
	return tenant.Tenant{ID: id}, nil
}

func (f *fakeProfileStore) Update(_ context.Context, id uuid.UUID, p tenant.ProfilePatch) (tenant.Tenant, error) {
	f.got = p
	return tenant.Tenant{ID: id}, nil
}

func TestProfileServiceRequiresAdmin(t *testing.T) {
	store := &fakeProfileStore{}
	svc := tenant.NewService(nil, store, nil)
	manager := admin
	manager.Role = identity.RoleWarehouseManager

	if got, err := svc.Profile(t.Context(), admin); err != nil || got.ID != admin.TenantID {
		t.Errorf("admin: %+v, %v", got, err)
	}
	var denial *policy.DenialError
	if _, err := svc.Profile(t.Context(), manager); !errors.As(err, &denial) {
		t.Errorf("warehouse manager reading: error = %v, want a denial", err)
	}
	digit := 4
	if _, err := svc.UpdateProfile(t.Context(), admin, tenant.ProfilePatch{SSCCExtensionDigit: &digit}); err != nil ||
		store.got.SSCCExtensionDigit == nil {
		t.Errorf("admin update: %v, stored %+v", err, store.got)
	}
	if _, err := svc.UpdateProfile(t.Context(), manager, tenant.ProfilePatch{}); !errors.As(err, &denial) {
		t.Errorf("warehouse manager updating: error = %v, want a denial", err)
	}
}
