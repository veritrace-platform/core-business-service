//go:build integration

package app_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/app"
	"github.com/veritrace-platform/core-business-service/internal/auth"
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
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	cfg := app.Config{Auth: auth.Config{
		SigningKeys:     "test:" + base64.StdEncoding.EncodeToString(seed),
		RefreshTokenTTL: 168 * time.Hour,
		TrustedProxies:  "127.0.0.0/8,::1/128",
	}}
	handler, err := app.NewHandler(cfg, app.Dependencies{
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

// response is the status and headers of a finished request; the body has already been read and closed.
type response struct {
	status int
	header http.Header
}

// request describes one call to the API.
type request struct {
	method, path string
	token        string            // bearer access token
	body         any               // encoded as JSON when not nil
	header       map[string]string // extra request headers
}

// do sends req. It decodes a successful JSON response into out, or the problem when out is a *httpx.Problem.
func (a *api) do(req request, out any) response {
	a.t.Helper()
	var body bytes.Buffer
	if req.body != nil {
		if err := json.NewEncoder(&body).Encode(req.body); err != nil {
			a.t.Fatalf("encode request: %v", err)
		}
	}
	httpReq, err := http.NewRequestWithContext(a.t.Context(), req.method, a.url+req.path, &body)
	if err != nil {
		a.t.Fatalf("build request: %v", err)
	}
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if req.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.token)
	}
	for k, v := range req.header {
		httpReq.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		a.t.Fatalf("%s %s: %v", req.method, req.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, isProblem := out.(*httpx.Problem)
	if out != nil && (resp.StatusCode >= 400) == isProblem && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			a.t.Fatalf("%s %s: decode response: %v", req.method, req.path, err)
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

// session is the body of a login or refresh.
type session struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	User                  struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Role   string `json:"role"`
		Tenant struct {
			ID               string `json:"id"`
			Code             string `json:"code"`
			GS1CompanyPrefix string `json:"gs1_company_prefix"`
		} `json:"tenant"`
	} `json:"user"`
}

func (a *api) login(email, password string) (session, response) {
	a.t.Helper()
	var s session
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]string{"email": email, "password": password}}, &s)
	return s, resp
}

func (a *api) refresh(token string) (session, response) {
	a.t.Helper()
	var s session
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/refresh",
		body: map[string]string{"refresh_token": token}}, &s)
	return s, resp
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
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/tenants",
		body: registrationBody("SGFRESH", "8930001", "8930001001015", "admin@sgfresh.example")}, &registered)
	if resp.status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.status)
	}
	if registered.Tenant.Code != "SGFRESH" || registered.Tenant.Status != "ACTIVE" ||
		registered.Headquarters.GLN != "8930001001015" || !registered.Headquarters.IsHeadquarters ||
		registered.Admin.Role != "ADMIN" || resp.header.Get("Location") != "/api/v1/tenant" {
		t.Errorf("registered = %+v", registered)
	}

	var problem httpx.Problem
	resp = a.do(request{method: http.MethodPost, path: "/api/v1/tenants",
		body: registrationBody("SGFRESH", "8934567", "8934567000017", "other@sgfresh.example")}, &problem)
	if resp.status != http.StatusConflict || problem.Code != "IDENTIFIER_ALREADY_REGISTERED" ||
		len(problem.Errors) != 1 || problem.Errors[0].Field != "tenant.code" {
		t.Errorf("duplicate code: status = %d, problem = %+v", resp.status, problem)
	}

	// The new admin can sign in right away.
	if s, resp := a.login("Admin@SGFresh.example", "correct-horse-battery"); resp.status != http.StatusOK ||
		s.User.Tenant.Code != "SGFRESH" || s.User.Tenant.GS1CompanyPrefix != "8930001" {
		t.Errorf("login after registration: status = %d, session = %+v", resp.status, s)
	}
}

func TestSessionLifecycle(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)

	first, resp := a.login(tenant.Admin.Email, tenancytest.FixturePassword)
	if resp.status != http.StatusOK || first.TokenType != "Bearer" || first.ExpiresIn != 900 ||
		first.RefreshTokenExpiresIn != 604800 || first.User.Role != "ADMIN" || resp.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("login: status = %d, session = %+v", resp.status, first)
	}

	var me struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/me", token: first.AccessToken}, &me); resp.status != http.StatusOK ||
		me.ID != tenant.Admin.ID.String() || me.Tenant.ID != tenant.ID.String() {
		t.Errorf("GET /me: status = %d, me = %+v", resp.status, me)
	}

	second, resp := a.refresh(first.RefreshToken)
	if resp.status != http.StatusOK || second.RefreshToken == first.RefreshToken || second.AccessToken == "" {
		t.Fatalf("refresh: status = %d", resp.status)
	}

	// Reusing the rotated token revokes the whole family, including the token that replaced it.
	var problem httpx.Problem
	if _, resp := a.refresh(first.RefreshToken); resp.status != http.StatusUnauthorized {
		t.Errorf("reuse: status = %d, want 401", resp.status)
	}
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/refresh",
		body: map[string]string{"refresh_token": second.RefreshToken}}, &problem); resp.status != http.StatusUnauthorized ||
		problem.Code != "REFRESH_TOKEN_INVALID" {
		t.Errorf("after reuse: status = %d, code = %s", resp.status, problem.Code)
	}

	// Logout revokes the family; logging out again still succeeds.
	third, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)
	for range 2 {
		if resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/logout",
			body: map[string]string{"refresh_token": third.RefreshToken}}, nil); resp.status != http.StatusNoContent {
			t.Errorf("logout: status = %d, want 204", resp.status)
		}
	}
	if _, resp := a.refresh(third.RefreshToken); resp.status != http.StatusUnauthorized {
		t.Errorf("refresh after logout: status = %d, want 401", resp.status)
	}

	var problemLogin httpx.Problem
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]string{"email": tenant.Admin.Email, "password": "wrong password"}}, &problemLogin); resp.status != http.StatusUnauthorized ||
		problemLogin.Code != "INVALID_CREDENTIALS" {
		t.Errorf("wrong password: status = %d, code = %s", resp.status, problemLogin.Code)
	}
}

func TestAccessTokensVerifyWithTheJWKS(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	s, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)

	var jwks auth.JWKS
	if resp := a.do(request{method: http.MethodGet, path: "/.well-known/jwks.json"}, &jwks); resp.status != http.StatusOK || len(jwks.Keys) != 1 {
		t.Fatalf("GET /.well-known/jwks.json: status = %d, keys = %+v", resp.status, jwks.Keys)
	}
	x, err := base64.RawURLEncoding.DecodeString(jwks.Keys[0].X)
	if err != nil {
		t.Fatalf("decode x: %v", err)
	}
	// This is how another service verifies tokens (ADR-0007).
	var claims jwt.MapClaims
	token, err := jwt.ParseWithClaims(s.AccessToken, &claims, func(tok *jwt.Token) (any, error) {
		if tok.Header["kid"] != jwks.Keys[0].KeyID {
			t.Errorf("kid = %v, want %s", tok.Header["kid"], jwks.Keys[0].KeyID)
		}
		return ed25519.PublicKey(x), nil
	}, jwt.WithValidMethods([]string{"EdDSA"}))
	if err != nil || !token.Valid || claims["tid"] != tenant.ID.String() || claims["sub"] != tenant.Admin.ID.String() {
		t.Errorf("verify with JWKS: %v, claims = %v", err, claims)
	}
}

func TestPublicEndpointsAreRateLimited(t *testing.T) {
	a := startAPI(t)
	client := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	body := map[string]string{"email": "nobody@fixture.example", "password": "whatever password"}
	for i := range 10 {
		if resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: body, header: client}, nil); resp.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, resp.status)
		}
	}
	var problem httpx.Problem
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: body, header: client}, &problem)
	if resp.status != http.StatusTooManyRequests || problem.Code != "RATE_LIMITED" || resp.header.Get("Retry-After") == "" {
		t.Errorf("11th attempt: status = %d, code = %s, Retry-After = %q", resp.status, problem.Code, resp.header.Get("Retry-After"))
	}

	other := map[string]string{"X-Forwarded-For": "203.0.113.10"}
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: body, header: other}, nil); resp.status != http.StatusUnauthorized {
		t.Errorf("another client: status = %d, want 401", resp.status)
	}
	// Registration has its own budget.
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/tenants", body: map[string]any{}, header: client}, nil); resp.status != http.StatusBadRequest {
		t.Errorf("registration from the throttled client: status = %d, want 400", resp.status)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	mine, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)
	other, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)

	var problem httpx.Problem
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/me/password", token: mine.AccessToken,
		body: map[string]string{"current_password": "wrong password", "new_password": "a brand new password"}}, &problem)
	if resp.status != http.StatusBadRequest || len(problem.Errors) != 1 || problem.Errors[0].Code != "INCORRECT" {
		t.Errorf("wrong current password: status = %d, problem = %+v", resp.status, problem)
	}

	resp = a.do(request{method: http.MethodPost, path: "/api/v1/me/password", token: mine.AccessToken,
		body: map[string]string{"current_password": tenancytest.FixturePassword, "new_password": "a brand new password"}}, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("change password: status = %d, want 204", resp.status)
	}
	if _, resp := a.refresh(other.RefreshToken); resp.status != http.StatusUnauthorized {
		t.Errorf("another session after the change: status = %d, want 401", resp.status)
	}
	if _, resp := a.refresh(mine.RefreshToken); resp.status != http.StatusOK {
		t.Errorf("the session that changed the password: status = %d, want 200", resp.status)
	}
	if _, resp := a.login(tenant.Admin.Email, tenancytest.FixturePassword); resp.status != http.StatusUnauthorized {
		t.Errorf("old password: status = %d, want 401", resp.status)
	}
	if _, resp := a.login(tenant.Admin.Email, "a brand new password"); resp.status != http.StatusOK {
		t.Errorf("new password: status = %d, want 200", resp.status)
	}
}
