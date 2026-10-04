package auth_test

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
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// fakeSessions records calls and returns canned results.
type fakeSessions struct {
	err       error
	gotEmail  string
	gotUA     *string
	gotToken  string
	principal identity.Principal
}

var session = auth.Session{
	AccessToken: "access", AccessTokenTTL: auth.AccessTokenTTL, RefreshToken: "refresh",
	RefreshTokenTTL: 7 * 24 * time.Hour,
	User: user.Me{
		User:   user.User{ID: uuid.New(), Email: "an@sgfresh.example", Role: identity.RoleAdmin, IsActive: true},
		Tenant: user.TenantSummary{ID: uuid.New(), Code: "SGFRESH", Status: "ACTIVE"},
	},
}

func (f *fakeSessions) Login(_ context.Context, email, _ string, ua *string) (auth.Session, error) {
	f.gotEmail, f.gotUA = email, ua
	return session, f.err
}

func (f *fakeSessions) Refresh(_ context.Context, token string, _ *string) (auth.Session, error) {
	f.gotToken = token
	return session, f.err
}

func (f *fakeSessions) Logout(_ context.Context, token string) error {
	f.gotToken = token
	return f.err
}

func (f *fakeSessions) Me(_ context.Context, p identity.Principal) (user.Me, error) {
	f.principal = p
	return session.User, f.err
}

func (f *fakeSessions) ChangePassword(_ context.Context, p identity.Principal, _, _ string) error {
	f.principal = p
	return f.err
}

type harness struct {
	router http.Handler
	tokens *auth.Tokens
	now    *time.Time
	logs   *bytes.Buffer
}

func newHarness(t *testing.T, sessions auth.Sessions) harness {
	t.Helper()
	now := testNow
	keys := mustKeys(t, "k1:"+seed(1))
	tokens := auth.NewTokens(keys, clock(&now))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := auth.NewHandler(sessions, keys, func(next http.Handler) http.Handler { return next },
		auth.NewAuthenticator(tokens).Middleware, logger)
	router := httpapi.NewRouter(logger, prometheus.NewRegistry(), httpapi.Mounts{
		API: []httpapi.Routes{h.Routes}, WellKnown: []httpapi.Routes{h.WellKnownRoutes},
	})
	return harness{router: router, tokens: tokens, now: &now, logs: &logs}
}

func (h harness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "VeriTrace-Test/1.0")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h harness) token(t *testing.T) string {
	t.Helper()
	token, _, err := h.tokens.Issue(testPrincipal)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	return token
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v (status %d)", err, rec.Code)
	}
	return p
}

func TestLoginEndpoint(t *testing.T) {
	fake := &fakeSessions{}
	h := newHarness(t, fake)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login", "", `{"email":" an@sgfresh.example ","password":"secret password"}`)

	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, Cache-Control = %q; want 200, no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["access_token"] != "access" || body["token_type"] != "Bearer" || body["expires_in"] != 900.0 ||
		body["refresh_token"] != "refresh" || body["refresh_token_expires_in"] != 604800.0 {
		t.Errorf("body = %v", body)
	}
	if u, _ := body["user"].(map[string]any); u["email"] != "an@sgfresh.example" || u["tenant"] == nil {
		t.Errorf("user = %v", body["user"])
	}
	if fake.gotEmail != "an@sgfresh.example" || fake.gotUA == nil || *fake.gotUA != "VeriTrace-Test/1.0" {
		t.Errorf("service got email %q and user agent %v", fake.gotEmail, fake.gotUA)
	}
}

func TestLoginEndpointErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		body       string
		wantStatus int
		wantCode   string
	}{
		{"wrong credentials", auth.ErrInvalidCredentials, `{"email":"a@b.example","password":"x"}`, 401, "INVALID_CREDENTIALS"},
		{"missing password", nil, `{"email":"a@b.example"}`, 400, httpx.CodeValidationFailed},
		{"missing email", nil, `{"password":"x"}`, 400, httpx.CodeValidationFailed},
		{"service failure", errors.New("db down"), `{"email":"a@b.example","password":"x"}`, 500, httpx.CodeInternalError},
		{"not json", nil, `email=a`, 400, httpx.CodeValidationFailed},
	}
	for _, tt := range tests {
		h := newHarness(t, &fakeSessions{err: tt.err})
		rec := h.do(t, http.MethodPost, "/api/v1/auth/login", "", tt.body)
		if p := problemOf(t, rec); rec.Code != tt.wantStatus || p.Code != tt.wantCode {
			t.Errorf("%s: %d %s, want %d %s", tt.name, rec.Code, p.Code, tt.wantStatus, tt.wantCode)
		}
	}
}

func TestRefreshEndpoint(t *testing.T) {
	fake := &fakeSessions{}
	h := newHarness(t, fake)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", `{"refresh_token":"abc"}`)
	if rec.Code != http.StatusOK || fake.gotToken != "abc" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("status = %d, token = %q", rec.Code, fake.gotToken)
	}

	for name, err := range map[string]error{"invalid": auth.ErrRefreshTokenInvalid, "reused": auth.ErrRefreshTokenReused} {
		h := newHarness(t, &fakeSessions{err: err})
		rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", `{"refresh_token":"abc"}`)
		if p := problemOf(t, rec); rec.Code != http.StatusUnauthorized || p.Code != "REFRESH_TOKEN_INVALID" {
			t.Errorf("%s: %d %s, want 401 REFRESH_TOKEN_INVALID", name, rec.Code, p.Code)
		}
		if reused := strings.Contains(h.logs.String(), "reused"); reused != (name == "reused") {
			t.Errorf("%s: reuse logged = %t", name, reused)
		}
	}

	rec = newHarness(t, &fakeSessions{}).do(t, http.MethodPost, "/api/v1/auth/refresh", "", `{}`)
	if p := problemOf(t, rec); rec.Code != http.StatusBadRequest || p.Errors[0].Field != "refresh_token" {
		t.Errorf("missing token: %d %+v", rec.Code, p)
	}
}

func TestLogoutEndpoint(t *testing.T) {
	fake := &fakeSessions{}
	rec := newHarness(t, fake).do(t, http.MethodPost, "/api/v1/auth/logout", "", `{"refresh_token":"abc"}`)
	if rec.Code != http.StatusNoContent || fake.gotToken != "abc" {
		t.Errorf("status = %d, token = %q; want 204", rec.Code, fake.gotToken)
	}
	rec = newHarness(t, &fakeSessions{}).do(t, http.MethodPost, "/api/v1/auth/logout", "", `{"refresh_token":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty token: status = %d, want 400", rec.Code)
	}
	rec = newHarness(t, &fakeSessions{err: errors.New("db down")}).do(t, http.MethodPost, "/api/v1/auth/logout", "", `{"refresh_token":"abc"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("service failure: status = %d, want 500", rec.Code)
	}
}

func TestJWKSEndpoint(t *testing.T) {
	rec := newHarness(t, &fakeSessions{}).do(t, http.MethodGet, "/.well-known/jwks.json", "", "")
	var doc auth.JWKS
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil || rec.Code != http.StatusOK || len(doc.Keys) != 1 ||
		doc.Keys[0].KeyID != "k1" || rec.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("status = %d, doc = %+v, err = %v", rec.Code, doc, err)
	}
}

func TestMeRequiresAValidToken(t *testing.T) {
	fake := &fakeSessions{}
	h := newHarness(t, fake)

	rec := h.do(t, http.MethodGet, "/api/v1/me", h.token(t), "")
	if rec.Code != http.StatusOK || fake.principal != testPrincipal {
		t.Fatalf("valid token: status = %d, principal = %+v", rec.Code, fake.principal)
	}

	rec = h.do(t, http.MethodGet, "/api/v1/me", "", "")
	if p := problemOf(t, rec); rec.Code != 401 || p.Code != httpx.CodeUnauthenticated ||
		rec.Header().Get("WWW-Authenticate") != `Bearer realm="veritrace"` {
		t.Errorf("no token: %d %s %q", rec.Code, p.Code, rec.Header().Get("WWW-Authenticate"))
	}

	rec = h.do(t, http.MethodGet, "/api/v1/me", "not.a.token", "")
	if p := problemOf(t, rec); rec.Code != 401 || p.Code != httpx.CodeUnauthenticated ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("invalid token: %d %s", rec.Code, p.Code)
	}

	token := h.token(t)
	*h.now = testNow.Add(auth.AccessTokenTTL + time.Second)
	rec = h.do(t, http.MethodGet, "/api/v1/me", token, "")
	if p := problemOf(t, rec); rec.Code != 401 || p.Code != "TOKEN_EXPIRED" ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), `error_description="expired"`) {
		t.Errorf("expired token: %d %s", rec.Code, p.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec = httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("basic credentials: status = %d, want 401", rec.Code)
	}
}

func TestMeErrors(t *testing.T) {
	for err, want := range map[error]int{auth.ErrNotFound: 404, errors.New("db down"): 500} {
		h := newHarness(t, &fakeSessions{err: err})
		if rec := h.do(t, http.MethodGet, "/api/v1/me", h.token(t), ""); rec.Code != want {
			t.Errorf("%v: status = %d, want %d", err, rec.Code, want)
		}
	}
}

func TestChangePasswordEndpoint(t *testing.T) {
	fake := &fakeSessions{}
	h := newHarness(t, fake)
	rec := h.do(t, http.MethodPost, "/api/v1/me/password", h.token(t), `{"current_password":"old one","new_password":"a brand new password"}`)
	if rec.Code != http.StatusNoContent || fake.principal != testPrincipal {
		t.Fatalf("status = %d, principal = %+v; want 204 for the caller", rec.Code, fake.principal)
	}

	tests := []struct {
		name      string
		err       error
		body      string
		wantField string
		wantCode  string
	}{
		{"incorrect current", auth.ErrIncorrectPassword, `{"current_password":"x","new_password":"a brand new password"}`, "current_password", "INCORRECT"},
		{"short new", nil, `{"current_password":"x","new_password":"short"}`, "new_password", httpx.FieldTooShort},
		{"missing current", nil, `{"new_password":"a brand new password"}`, "current_password", httpx.FieldRequired},
	}
	for _, tt := range tests {
		h := newHarness(t, &fakeSessions{err: tt.err})
		rec := h.do(t, http.MethodPost, "/api/v1/me/password", h.token(t), tt.body)
		p := problemOf(t, rec)
		if rec.Code != 400 || p.Code != httpx.CodeValidationFailed || len(p.Errors) != 1 ||
			p.Errors[0].Field != tt.wantField || p.Errors[0].Code != tt.wantCode {
			t.Errorf("%s: %d %+v", tt.name, rec.Code, p)
		}
	}

	h = newHarness(t, &fakeSessions{err: errors.New("db down")})
	rec = h.do(t, http.MethodPost, "/api/v1/me/password", h.token(t), `{"current_password":"x","new_password":"a brand new password"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("service failure: status = %d, want 500", rec.Code)
	}
}

func TestConfigValidate(t *testing.T) {
	valid := auth.Config{SigningKeys: "k1:" + seed(1), RefreshTokenTTL: 168 * time.Hour, TrustedProxies: "10.0.0.0/8"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := map[string]func(*auth.Config){
		"JWT_SIGNING_KEYS is required": func(c *auth.Config) { c.SigningKeys = "" },
		"JWT_SIGNING_KEYS:":            func(c *auth.Config) { c.SigningKeys = "k1" },
		"REFRESH_TOKEN_TTL":            func(c *auth.Config) { c.RefreshTokenTTL = time.Minute },
		"TRUSTED_PROXIES":              func(c *auth.Config) { c.TrustedProxies = "nope" },
	}
	for want, mutate := range tests {
		c := valid
		mutate(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v, want one mentioning %s", err, want)
		}
	}
}
