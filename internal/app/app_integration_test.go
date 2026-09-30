//go:build integration

package app_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/app"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/tenancy/tenancytest"
)

// api is a running core service on a migrated database.
type api struct {
	t   *testing.T
	db  *tenancytest.Database
	url string
}

func startAPI(t *testing.T) *api {
	t.Helper()
	db := tenancytest.Start(t)
	handler, err := app.NewHandler(app.Dependencies{
		Logger:     slog.New(slog.DiscardHandler),
		Registerer: prometheus.NewRegistry(),
		Pool:       db.App,
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &api{t: t, db: db, url: server.URL}
}

// response is the status and headers of a finished request; do has already read and closed the body.
type response struct {
	status int
	header http.Header
}

// do sends a request with an optional JSON body and bearer token. It decodes a successful JSON response into
// out, or a problem when out is a *httpx.Problem.
func (a *api) do(method, path, token string, body any, out any) response {
	a.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.url+path, reader)
	if err != nil {
		a.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode < 300 && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			a.t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
	if problem, ok := out.(*httpx.Problem); ok && resp.StatusCode >= 400 {
		if err := json.NewDecoder(resp.Body).Decode(problem); err != nil {
			a.t.Fatalf("%s %s: decode problem: %v", method, path, err)
		}
	}
	return response{status: resp.StatusCode, header: resp.Header}
}

func registrationBody(code, prefix, gln, email string) map[string]any {
	return map[string]any{
		"tenant": map[string]any{
			"code": code, "legal_name": "Saigon Fresh Foods", "tax_code": "0312345678", "gs1_company_prefix": prefix,
		},
		"headquarters": map[string]any{
			"gln": gln, "name": "Headquarters", "address": "12 Nguyen Hue", "city": "Ho Chi Minh City",
			"latitude": 10.773547, "longitude": 106.703945,
		},
		"admin": map[string]any{"email": email, "password": "correct-horse-battery", "full_name": "Nguyen Van An"},
	}
}

func TestTenantRegistration(t *testing.T) {
	a := startAPI(t)

	var registered struct {
		Tenant struct {
			ID     string `json:"id"`
			Code   string `json:"code"`
			Status string `json:"status"`
		} `json:"tenant"`
		Headquarters struct {
			GLN            string `json:"gln"`
			IsHeadquarters bool   `json:"is_headquarters"`
		} `json:"headquarters"`
		Admin struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"admin"`
	}
	resp := a.do(http.MethodPost, "/api/v1/tenants", "", registrationBody("SGFRESH", "8930001", "8930001001015", "admin@sgfresh.example"), &registered)
	if resp.status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.status)
	}
	if registered.Tenant.Code != "SGFRESH" || registered.Tenant.Status != "ACTIVE" ||
		registered.Headquarters.GLN != "8930001001015" || !registered.Headquarters.IsHeadquarters ||
		registered.Admin.Role != "ADMIN" || resp.header.Get("Location") != "/api/v1/tenant" {
		t.Errorf("registered = %+v", registered)
	}

	var problem httpx.Problem
	resp = a.do(http.MethodPost, "/api/v1/tenants", "", registrationBody("SGFRESH", "8934567", "8934567000017", "other@sgfresh.example"), &problem)
	if resp.status != http.StatusConflict || problem.Code != "IDENTIFIER_ALREADY_REGISTERED" ||
		len(problem.Errors) != 1 || problem.Errors[0].Field != "tenant.code" {
		t.Errorf("duplicate code: status = %d, problem = %+v", resp.status, problem)
	}
}
