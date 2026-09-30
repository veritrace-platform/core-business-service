package tenant_test

import (
	"bytes"
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
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
)

type fakeRegisterer struct {
	got tenant.Registration
	err error
}

func (f *fakeRegisterer) Register(_ context.Context, r tenant.Registration) (tenant.Registered, error) {
	f.got = r
	if f.err != nil {
		return tenant.Registered{}, f.err
	}
	var out tenant.Registered
	out.Tenant.ID = uuid.New()
	out.Tenant.Code = r.Code
	return out, nil
}

func newRouter(registerer tenant.Registerer) http.Handler {
	h := tenant.NewHandler(registerer, func(next http.Handler) http.Handler { return next }, slog.New(slog.DiscardHandler))
	return httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{
		API: []httpapi.Routes{h.Routes},
	})
}

// validRequest returns a registration body; edit mutates it before encoding.
func validRequest(edit func(body map[string]map[string]any)) []byte {
	body := map[string]map[string]any{
		"tenant": {
			"code": "SGFRESH", "legal_name": "Saigon Fresh Foods", "tax_code": "0312345678",
			"gs1_company_prefix": "8930001",
		},
		"headquarters": {
			"gln": "8930001001015", "name": "Headquarters", "address": "12 Nguyen Hue", "city": "Ho Chi Minh City",
			"latitude": 10.7735474, "longitude": 106.7039451,
		},
		"admin": {"email": "admin@sgfresh.example", "password": "correct-horse-battery", "full_name": "Nguyen Van An"},
	}
	if edit != nil {
		edit(body)
	}
	raw, _ := json.Marshal(body)
	return raw
}

func post(t *testing.T, router http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return p
}

func TestRegisterCreatesTheTenant(t *testing.T) {
	fake := &fakeRegisterer{}
	rec := post(t, newRouter(fake), validRequest(func(b map[string]map[string]any) {
		b["tenant"]["legal_name"] = "  Saigon Fresh Foods  "
		b["admin"]["phone"] = " +84 28 3822 1234 "
	}))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/tenant" {
		t.Errorf("Location = %q, want /api/v1/tenant", loc)
	}
	var body tenant.Registered
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Tenant.Code != "SGFRESH" {
		t.Errorf("body = %+v, %v", body, err)
	}

	got := fake.got
	if got.LegalName != "Saigon Fresh Foods" || got.Admin.Phone == nil || *got.Admin.Phone != "+84 28 3822 1234" {
		t.Errorf("text fields not trimmed: %+v", got)
	}
	hq := got.Headquarters
	if hq.CountryCode != "VN" || hq.GeoFenceRadiusMeters != 200 {
		t.Errorf("defaults = %s, %d; want VN, 200", hq.CountryCode, hq.GeoFenceRadiusMeters)
	}
	if hq.Latitude != 10.773547 || hq.Longitude != 106.703945 {
		t.Errorf("coordinates = %v, %v; want them rounded to 6 decimals", hq.Latitude, hq.Longitude)
	}
	if got.Admin.Password != "correct-horse-battery" {
		t.Error("password was altered")
	}
}

func TestRegisterRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name       string
		edit       func(map[string]map[string]any)
		wantStatus int
		wantCode   string
		wantField  string
		wantReason string
	}{
		{"missing code", func(b map[string]map[string]any) { delete(b["tenant"], "code") }, 400, httpx.CodeValidationFailed, "tenant.code", httpx.FieldRequired},
		{"lowercase code", func(b map[string]map[string]any) { b["tenant"]["code"] = "sgfresh" }, 400, httpx.CodeValidationFailed, "tenant.code", httpx.FieldInvalidFormat},
		{"blank legal name", func(b map[string]map[string]any) { b["tenant"]["legal_name"] = "   " }, 400, httpx.CodeValidationFailed, "tenant.legal_name", httpx.FieldRequired},
		{"long legal name", func(b map[string]map[string]any) { b["tenant"]["legal_name"] = strings.Repeat("x", 256) }, 400, httpx.CodeValidationFailed, "tenant.legal_name", httpx.FieldTooLong},
		{"bad tax code", func(b map[string]map[string]any) { b["tenant"]["tax_code"] = "031234567X" }, 400, httpx.CodeValidationFailed, "tenant.tax_code", httpx.FieldInvalidFormat},
		{"missing prefix", func(b map[string]map[string]any) { b["tenant"]["gs1_company_prefix"] = "" }, 400, httpx.CodeValidationFailed, "tenant.gs1_company_prefix", httpx.FieldRequired},
		{"short prefix", func(b map[string]map[string]any) { b["tenant"]["gs1_company_prefix"] = "89300" }, 422, "INVALID_GS1_IDENTIFIER", "tenant.gs1_company_prefix", "LENGTH"},
		{"gln check digit", func(b map[string]map[string]any) { b["headquarters"]["gln"] = "8930001001016" }, 422, "INVALID_GS1_IDENTIFIER", "headquarters.gln", "CHECK_DIGIT"},
		{"gln of another prefix", func(b map[string]map[string]any) { b["headquarters"]["gln"] = "8934567000017" }, 422, "INVALID_GS1_IDENTIFIER", "headquarters.gln", "PREFIX_MISMATCH"},
		{"gln length", func(b map[string]map[string]any) { b["headquarters"]["gln"] = "893000100101" }, 422, "INVALID_GS1_IDENTIFIER", "headquarters.gln", "LENGTH"},
		{"missing gln", func(b map[string]map[string]any) { delete(b["headquarters"], "gln") }, 400, httpx.CodeValidationFailed, "headquarters.gln", httpx.FieldRequired},
		{"missing latitude", func(b map[string]map[string]any) { delete(b["headquarters"], "latitude") }, 400, httpx.CodeValidationFailed, "headquarters.latitude", httpx.FieldRequired},
		{"longitude range", func(b map[string]map[string]any) { b["headquarters"]["longitude"] = 180.5 }, 400, httpx.CodeValidationFailed, "headquarters.longitude", httpx.FieldOutOfRange},
		{"radius range", func(b map[string]map[string]any) { b["headquarters"]["geo_fence_radius_meters"] = 49 }, 400, httpx.CodeValidationFailed, "headquarters.geo_fence_radius_meters", httpx.FieldOutOfRange},
		{"country code", func(b map[string]map[string]any) { b["headquarters"]["country_code"] = "vn" }, 400, httpx.CodeValidationFailed, "headquarters.country_code", httpx.FieldInvalidFormat},
		{"email", func(b map[string]map[string]any) { b["admin"]["email"] = "An <admin@sgfresh.example>" }, 400, httpx.CodeValidationFailed, "admin.email", httpx.FieldInvalidFormat},
		{"short password", func(b map[string]map[string]any) { b["admin"]["password"] = "short" }, 400, httpx.CodeValidationFailed, "admin.password", httpx.FieldTooShort},
		{"phone characters", func(b map[string]map[string]any) { b["admin"]["phone"] = "call me" }, 400, httpx.CodeValidationFailed, "admin.phone", httpx.FieldInvalidFormat},
		{"unknown field", func(b map[string]map[string]any) { b["admin"]["nickname"] = "An" }, 400, httpx.CodeValidationFailed, "nickname", httpx.FieldUnknown},
		{"wrong type", func(b map[string]map[string]any) { b["headquarters"]["latitude"] = "10.77" }, 400, httpx.CodeValidationFailed, "headquarters.latitude", httpx.FieldInvalidType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRegisterer{}
			rec := post(t, newRouter(fake), validRequest(tt.edit))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			p := decodeProblem(t, rec)
			if p.Code != tt.wantCode {
				t.Errorf("code = %s, want %s", p.Code, tt.wantCode)
			}
			found := false
			for _, e := range p.Errors {
				found = found || (e.Field == tt.wantField && e.Code == tt.wantReason)
			}
			if !found {
				t.Errorf("errors = %+v, want %s on %s", p.Errors, tt.wantReason, tt.wantField)
			}
			if fake.got.Code != "" {
				t.Error("the service was called with an invalid request")
			}
		})
	}
}

func TestRegisterReportsEveryInvalidField(t *testing.T) {
	rec := post(t, newRouter(&fakeRegisterer{}), validRequest(func(b map[string]map[string]any) {
		b["tenant"]["code"] = "x"
		b["headquarters"]["gln"] = "8930001001016"
		b["admin"]["password"] = "short"
	}))
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest || len(p.Errors) != 3 {
		t.Errorf("status = %d, errors = %+v; want 400 listing all three fields, GS1 included", rec.Code, p.Errors)
	}
}

func TestRegisterReportsConflicts(t *testing.T) {
	tests := []struct {
		key   tenant.Key
		field string
	}{
		{tenant.KeyCode, "tenant.code"},
		{tenant.KeyTaxCode, "tenant.tax_code"},
		{tenant.KeyCompanyPrefix, "tenant.gs1_company_prefix"},
		{tenant.KeyHeadquartersGLN, "headquarters.gln"},
		{tenant.KeyAdminEmail, "admin.email"},
	}
	for _, tt := range tests {
		fake := &fakeRegisterer{err: &tenant.ConflictError{Key: tt.key}}
		rec := post(t, newRouter(fake), validRequest(nil))

		p := decodeProblem(t, rec)
		if rec.Code != http.StatusConflict || p.Code != "IDENTIFIER_ALREADY_REGISTERED" ||
			len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != "ALREADY_REGISTERED" {
			t.Errorf("%s: status = %d, problem = %+v; want 409 on %s", tt.key, rec.Code, p, tt.field)
		}
	}
}

func TestRegisterHidesUnexpectedErrors(t *testing.T) {
	rec := post(t, newRouter(&fakeRegisterer{err: errors.New("connection refused")}), validRequest(nil))
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusInternalServerError || p.Code != httpx.CodeInternalError || strings.Contains(p.Detail, "refused") {
		t.Errorf("status = %d, problem = %+v; want 500 without details", rec.Code, p)
	}
}
