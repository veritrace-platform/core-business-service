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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/app"
	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
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

func TestUserManagement(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	admin, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)

	type userBody struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		IsActive bool   `json:"is_active"`
	}
	var driver userBody
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/users", token: admin.AccessToken, body: map[string]any{
		"email": "binh@fixture.example", "password": "a long enough password", "full_name": "Tran Van Binh", "role": "DRIVER",
	}}, &driver)
	if resp.status != http.StatusCreated || driver.Role != "DRIVER" || resp.header.Get("Location") != "/api/v1/users/"+driver.ID {
		t.Fatalf("create driver: status = %d, user = %+v", resp.status, driver)
	}
	var problem httpx.Problem
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/users", token: admin.AccessToken, body: map[string]any{
		"email": "BINH@fixture.example", "password": "a long enough password", "full_name": "Copy", "role": "DRIVER",
	}}, &problem); resp.status != http.StatusConflict || problem.Errors[0].Field != "email" {
		t.Errorf("duplicate email: status = %d, problem = %+v", resp.status, problem)
	}

	manager := a.db.CreateUser(t, tenant.ID, "WAREHOUSE_MANAGER")
	managerSession, _ := a.login(manager.Email, tenancytest.FixturePassword)
	var drivers struct {
		Items      []userBody `json:"items"`
		NextCursor *string    `json:"next_cursor"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/users?role=DRIVER", token: managerSession.AccessToken}, &drivers); resp.status != http.StatusOK ||
		len(drivers.Items) != 1 || drivers.Items[0].ID != driver.ID || drivers.NextCursor != nil {
		t.Errorf("manager listing drivers: status = %d, page = %+v", resp.status, drivers)
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/users", token: managerSession.AccessToken}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("manager listing everyone: status = %d, want 403", resp.status)
	}

	// Deactivating the driver signs it out at its next refresh.
	driverSession, resp := a.login("binh@fixture.example", "a long enough password")
	if resp.status != http.StatusOK {
		t.Fatalf("driver login: status = %d", resp.status)
	}
	var updated userBody
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/users/" + driver.ID, token: admin.AccessToken,
		body: map[string]any{"is_active": false}, header: map[string]string{"Content-Type": "application/merge-patch+json"}}, &updated); resp.status != http.StatusOK ||
		updated.IsActive {
		t.Fatalf("deactivate: status = %d, user = %+v", resp.status, updated)
	}
	if _, resp := a.refresh(driverSession.RefreshToken); resp.status != http.StatusUnauthorized {
		t.Errorf("refresh after deactivation: status = %d, want 401", resp.status)
	}
	if _, resp := a.login("binh@fixture.example", "a long enough password"); resp.status != http.StatusUnauthorized {
		t.Errorf("login after deactivation: status = %d, want 401", resp.status)
	}

	// An admin cannot demote itself, so the tenant keeps an admin.
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/users/" + tenant.Admin.ID.String(), token: admin.AccessToken,
		body: map[string]any{"role": "DRIVER"}}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("self demotion: status = %d, want 403", resp.status)
	}

	// Another tenant's admin sees nothing of this tenant's users.
	other := a.db.CreateTenant(t)
	otherAdmin, _ := a.login(other.Admin.Email, tenancytest.FixturePassword)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/users/" + driver.ID, token: otherAdmin.AccessToken}, &problem); resp.status != http.StatusNotFound {
		t.Errorf("another tenant reading the driver: status = %d, want 404", resp.status)
	}
}

func TestTenantProfile(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	admin, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)

	var profile struct {
		ID                 string `json:"id"`
		Code               string `json:"code"`
		LegalName          string `json:"legal_name"`
		SSCCExtensionDigit int    `json:"sscc_extension_digit"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/tenant", token: admin.AccessToken}, &profile); resp.status != http.StatusOK ||
		profile.ID != tenant.ID.String() || profile.Code != tenant.Code {
		t.Fatalf("GET /tenant: status = %d, profile = %+v", resp.status, profile)
	}
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/tenant", token: admin.AccessToken,
		body: map[string]any{"legal_name": "Renamed Company", "sscc_extension_digit": 7}}, &profile); resp.status != http.StatusOK ||
		profile.LegalName != "Renamed Company" || profile.SSCCExtensionDigit != 7 {
		t.Errorf("PATCH /tenant: status = %d, profile = %+v", resp.status, profile)
	}

	var problem httpx.Problem
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/tenant", token: admin.AccessToken,
		body: map[string]any{"gs1_company_prefix": "8930002"}}, &problem); resp.status != http.StatusBadRequest ||
		problem.Errors[0].Code != "UNKNOWN_FIELD" {
		t.Errorf("changing the prefix: status = %d, problem = %+v", resp.status, problem)
	}

	driver := a.db.CreateUser(t, tenant.ID, "DRIVER")
	driverSession, _ := a.login(driver.Email, tenancytest.FixturePassword)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/tenant", token: driverSession.AccessToken}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("driver reading the profile: status = %d, want 403", resp.status)
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/tenant"}, &problem); resp.status != http.StatusUnauthorized ||
		problem.Code != "UNAUTHENTICATED" {
		t.Errorf("anonymous: status = %d, code = %s", resp.status, problem.Code)
	}
}

func TestLocationCatalog(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	admin, _ := a.login(tenant.Admin.Email, tenancytest.FixturePassword)
	gln := tenant.GCP + "50" + strconv.Itoa(gs1.CheckDigit(tenant.GCP+"50"))

	type locationBody struct {
		ID                   string  `json:"id"`
		GLN                  string  `json:"gln"`
		Name                 string  `json:"name"`
		Latitude             float64 `json:"latitude"`
		GeoFenceRadiusMeters int     `json:"geo_fence_radius_meters"`
		IsActive             bool    `json:"is_active"`
	}
	body := map[string]any{
		"gln": gln, "name": "Cold Store", "address": "5 Tan Thuan", "city": "Ho Chi Minh City",
		"latitude": 10.7626224, "longitude": 106.7428,
	}
	var created locationBody
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/locations", token: admin.AccessToken, body: body}, &created)
	if resp.status != http.StatusCreated || created.GLN != gln || created.Latitude != 10.762622 ||
		created.GeoFenceRadiusMeters != 200 || resp.header.Get("Location") != "/api/v1/locations/"+created.ID {
		t.Fatalf("create: status = %d, location = %+v", resp.status, created)
	}
	var problem httpx.Problem
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/locations", token: admin.AccessToken, body: body}, &problem); resp.status != http.StatusConflict ||
		problem.Errors[0].Field != "gln" {
		t.Errorf("same GLN again: status = %d, problem = %+v", resp.status, problem)
	}
	body["gln"] = "8934567000017"
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/locations", token: admin.AccessToken, body: body}, &problem); resp.status != http.StatusUnprocessableEntity ||
		problem.Errors[0].Code != "PREFIX_MISMATCH" {
		t.Errorf("GLN of another prefix: status = %d, problem = %+v", resp.status, problem)
	}

	// Warehouse managers read the catalog to pick locations, but only admins change it.
	manager := a.db.CreateUser(t, tenant.ID, "WAREHOUSE_MANAGER")
	managerSession, _ := a.login(manager.Email, tenancytest.FixturePassword)
	var page struct {
		Items []locationBody `json:"items"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/locations?is_active=true", token: managerSession.AccessToken}, &page); resp.status != http.StatusOK ||
		len(page.Items) != 2 || page.Items[0].ID != created.ID {
		t.Errorf("manager listing: status = %d, page = %+v; want the new location, then the headquarters", resp.status, page)
	}
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/locations/" + created.ID, token: managerSession.AccessToken,
		body: map[string]any{"name": "Mine"}}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("manager update: status = %d, want 403", resp.status)
	}
	driver := a.db.CreateUser(t, tenant.ID, "DRIVER")
	driverSession, _ := a.login(driver.Email, tenancytest.FixturePassword)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/locations", token: driverSession.AccessToken}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("driver listing: status = %d, want 403", resp.status)
	}

	var updated locationBody
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/locations/" + created.ID, token: admin.AccessToken,
		body:   map[string]any{"is_active": false, "geo_fence_radius_meters": 400},
		header: map[string]string{"Content-Type": "application/merge-patch+json"}}, &updated); resp.status != http.StatusOK ||
		updated.IsActive || updated.GeoFenceRadiusMeters != 400 || updated.Name != "Cold Store" {
		t.Errorf("deactivate: status = %d, location = %+v", resp.status, updated)
	}
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/locations/" + created.ID, token: admin.AccessToken,
		body: map[string]any{"gln": gln}}, &problem); resp.status != http.StatusBadRequest || problem.Errors[0].Code != "UNKNOWN_FIELD" {
		t.Errorf("changing the GLN: status = %d, problem = %+v", resp.status, problem)
	}

	// Another tenant's admin sees nothing of this tenant's locations.
	other := a.db.CreateTenant(t)
	otherAdmin, _ := a.login(other.Admin.Email, tenancytest.FixturePassword)
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		if resp := a.do(request{method: method, path: "/api/v1/locations/" + created.ID, token: otherAdmin.AccessToken,
			body: map[string]any{"name": "Hijacked"}}, &problem); resp.status != http.StatusNotFound {
			t.Errorf("%s by another tenant: status = %d, want 404", method, resp.status)
		}
	}
}

func TestDirectoryLookups(t *testing.T) {
	a := startAPI(t)
	owner := a.db.CreateTenant(t)
	warehouse := a.db.CreateLocation(t, owner)

	// A driver of another tenant confirms a checkpoint facility by its GLN.
	carrier := a.db.CreateTenant(t)
	driver := a.db.CreateUser(t, carrier.ID, "DRIVER")
	session, _ := a.login(driver.Email, tenancytest.FixturePassword)

	var entry struct {
		GLN    string `json:"gln"`
		Name   string `json:"name"`
		Tenant struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"tenant"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/directory/locations/" + warehouse.GLN, token: session.AccessToken}, &entry); resp.status != http.StatusOK ||
		entry.GLN != warehouse.GLN || entry.Name != "Fixture Warehouse" || entry.Tenant.ID != owner.ID.String() || entry.Tenant.Code != owner.Code {
		t.Errorf("GLN lookup: status = %d, entry = %+v", resp.status, entry)
	}
	var tenantEntry struct {
		ID        string `json:"id"`
		Code      string `json:"code"`
		LegalName string `json:"legal_name"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/directory/tenants/" + strings.ToLower(owner.Code), token: session.AccessToken}, &tenantEntry); resp.status != http.StatusOK ||
		tenantEntry.ID != owner.ID.String() || tenantEntry.LegalName == "" {
		t.Errorf("tenant lookup: status = %d, entry = %+v", resp.status, tenantEntry)
	}

	var problem httpx.Problem
	mistyped := warehouse.GLN[:12] + strconv.Itoa((int(warehouse.GLN[12]-'0')+1)%10)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/directory/locations/" + mistyped, token: session.AccessToken}, &problem); resp.status != http.StatusUnprocessableEntity ||
		problem.Errors[0].Code != "CHECK_DIGIT" {
		t.Errorf("mistyped GLN: status = %d, problem = %+v", resp.status, problem)
	}
	if _, err := a.db.Owner.Exec(t.Context(), `UPDATE core.locations SET is_active = false WHERE id = $1`, warehouse.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/directory/locations/" + warehouse.GLN, token: session.AccessToken}, &problem); resp.status != http.StatusNotFound {
		t.Errorf("inactive location: status = %d, want 404", resp.status)
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/directory/tenants/" + owner.Code}, &problem); resp.status != http.StatusUnauthorized {
		t.Errorf("anonymous lookup: status = %d, want 401", resp.status)
	}
}

func TestProductCatalog(t *testing.T) {
	a := startAPI(t)
	tenant := a.db.CreateTenant(t)
	manager := a.db.CreateUser(t, tenant.ID, "WAREHOUSE_MANAGER")
	session, _ := a.login(manager.Email, tenancytest.FixturePassword)
	payload := "0" + tenant.GCP + "01"
	gtin := payload + strconv.Itoa(gs1.CheckDigit(payload))

	type productBody struct {
		ID             string  `json:"id"`
		GTIN           string  `json:"gtin"`
		Name           string  `json:"name"`
		Description    *string `json:"description"`
		MinTempCelsius float64 `json:"min_temp_celsius"`
		MaxTempCelsius float64 `json:"max_temp_celsius"`
	}
	body := map[string]any{"gtin": gtin, "name": "Chilled milk", "min_temp_celsius": 2, "max_temp_celsius": 8}
	var created productBody
	resp := a.do(request{method: http.MethodPost, path: "/api/v1/products", token: session.AccessToken, body: body}, &created)
	if resp.status != http.StatusCreated || created.GTIN != gtin || created.MinTempCelsius != 2 || created.Description != nil ||
		resp.header.Get("Location") != "/api/v1/products/"+created.ID {
		t.Fatalf("create: status = %d, product = %+v", resp.status, created)
	}
	var problem httpx.Problem
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/products", token: session.AccessToken, body: body}, &problem); resp.status != http.StatusConflict {
		t.Errorf("same GTIN again: status = %d, want 409", resp.status)
	}
	body["gtin"] = "08934567001014"
	if resp := a.do(request{method: http.MethodPost, path: "/api/v1/products", token: session.AccessToken, body: body}, &problem); resp.status != http.StatusUnprocessableEntity ||
		problem.Errors[0].Code != "PREFIX_MISMATCH" {
		t.Errorf("GTIN of another prefix: status = %d, problem = %+v", resp.status, problem)
	}

	var page struct {
		Items []productBody `json:"items"`
	}
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/products?q=MILK", token: session.AccessToken}, &page); resp.status != http.StatusOK ||
		len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Errorf("search: status = %d, page = %+v", resp.status, page)
	}

	// The maximum cannot drop below the stored minimum.
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/products/" + created.ID, token: session.AccessToken,
		body: map[string]any{"max_temp_celsius": 1.5}}, &problem); resp.status != http.StatusBadRequest ||
		problem.Errors[0].Field != "max_temp_celsius" {
		t.Errorf("crossing bounds: status = %d, problem = %+v", resp.status, problem)
	}
	var updated productBody
	if resp := a.do(request{method: http.MethodPatch, path: "/api/v1/products/" + created.ID, token: session.AccessToken,
		body:   map[string]any{"description": "Pasteurized, 1 L", "max_temp_celsius": 6.5},
		header: map[string]string{"Content-Type": "application/merge-patch+json"}}, &updated); resp.status != http.StatusOK ||
		updated.MaxTempCelsius != 6.5 || updated.Description == nil || updated.MinTempCelsius != 2 {
		t.Errorf("update: status = %d, product = %+v", resp.status, updated)
	}

	driver := a.db.CreateUser(t, tenant.ID, "DRIVER")
	driverSession, _ := a.login(driver.Email, tenancytest.FixturePassword)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/products/" + created.ID, token: driverSession.AccessToken}, &problem); resp.status != http.StatusForbidden {
		t.Errorf("driver reading a product: status = %d, want 403", resp.status)
	}
	other := a.db.CreateTenant(t)
	otherAdmin, _ := a.login(other.Admin.Email, tenancytest.FixturePassword)
	if resp := a.do(request{method: http.MethodGet, path: "/api/v1/products/" + created.ID, token: otherAdmin.AccessToken}, &problem); resp.status != http.StatusNotFound {
		t.Errorf("another tenant reading the product: status = %d, want 404", resp.status)
	}
}
