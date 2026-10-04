package user_test

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
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// fakeUsers records the calls of the handler and returns canned results.
type fakeUsers struct {
	err       error
	listed    []user.User
	gotFilter user.Filter
	gotNew    user.NewUser
	gotPatch  user.Patch
	gotID     uuid.UUID
}

func (f *fakeUsers) List(_ context.Context, _ identity.Principal, flt user.Filter) ([]user.User, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func (f *fakeUsers) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (user.User, error) {
	f.gotID = id
	return user.User{ID: id}, f.err
}

func (f *fakeUsers) Create(_ context.Context, _ identity.Principal, nu user.NewUser) (user.User, error) {
	f.gotNew = nu
	return user.User{ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e"), Email: nu.Email, Role: nu.Role}, f.err
}

func (f *fakeUsers) Update(_ context.Context, _ identity.Principal, id uuid.UUID, p user.Patch) (user.User, error) {
	f.gotID, f.gotPatch = id, p
	return user.User{ID: id}, f.err
}

func router(users user.Users) http.Handler {
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleAdmin}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := user.NewHandler(users, authenticate, slog.New(slog.DiscardHandler))
	return httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})
}

func call(t *testing.T, users user.Users, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	router(users).ServeHTTP(rec, req)
	return rec
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem (status %d): %v", rec.Code, err)
	}
	return p
}

func TestListUsers(t *testing.T) {
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	fake := &fakeUsers{listed: []user.User{{ID: ids[2]}, {ID: ids[1]}, {ID: ids[0]}}}
	cursor := httpx.UUIDCursor(ids[2])
	rec := call(t, fake, http.MethodGet, "/api/v1/users?limit=2&role=DRIVER&is_active=false&cursor="+cursor, "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var page struct {
		Items      []user.User `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || *page.NextCursor != httpx.UUIDCursor(ids[1]) {
		t.Errorf("page = %+v, want two items and a cursor after the second", page)
	}
	f := fake.gotFilter
	if f.Limit != 3 || f.Role == nil || *f.Role != identity.RoleDriver || f.IsActive == nil || *f.IsActive || f.After != ids[2] {
		t.Errorf("filter = %+v, want limit+1, DRIVER, inactive, after the cursor", f)
	}

	rec = call(t, &fakeUsers{}, http.MethodGet, "/api/v1/users", "", "")
	if !strings.Contains(rec.Body.String(), `"items":[]`) || !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Errorf("empty page = %s, want an empty array and a null cursor", rec.Body)
	}
}

func TestListUsersRejectsBadQueries(t *testing.T) {
	tests := map[string]string{
		"limit=0":           "limit",
		"limit=x":           "limit",
		"cursor=not!base64": "cursor",
		"cursor=AAAA":       "cursor",
		"role=OWNER":        "role",
		"is_active=maybe":   "is_active",
	}
	for query, field := range tests {
		rec := call(t, &fakeUsers{}, http.MethodGet, "/api/v1/users?"+query, "", "")
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != field {
			t.Errorf("%s: status = %d, errors = %+v; want 400 on %s", query, rec.Code, p.Errors, field)
		}
	}
}

func TestCreateUser(t *testing.T) {
	fake := &fakeUsers{}
	rec := call(t, fake, http.MethodPost, "/api/v1/users", "application/json",
		`{"email":" driver@x.example ","password":"a long enough password","full_name":" Tran Van Binh ","role":"DRIVER","phone":""}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/users/0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e" {
		t.Fatalf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	nu := fake.gotNew
	if nu.Email != "driver@x.example" || nu.FullName != "Tran Van Binh" || nu.Role != identity.RoleDriver || nu.Phone != nil {
		t.Errorf("new user = %+v, want trimmed values and no phone", nu)
	}

	tests := []struct {
		body, field, code string
	}{
		{`{"password":"a long enough password","full_name":"X","role":"DRIVER"}`, "email", httpx.FieldRequired},
		{`{"email":"x@x.example","password":"short","full_name":"X","role":"DRIVER"}`, "password", httpx.FieldTooShort},
		{`{"email":"x@x.example","password":"a long enough password","role":"DRIVER"}`, "full_name", httpx.FieldRequired},
		{`{"email":"x@x.example","password":"a long enough password","full_name":"X"}`, "role", httpx.FieldRequired},
		{`{"email":"x@x.example","password":"a long enough password","full_name":"X","role":"OWNER"}`, "role", httpx.FieldInvalidValue},
		{`{"email":"x@x.example","password":"a long enough password","full_name":"X","role":"DRIVER","phone":"call me"}`, "phone", httpx.FieldInvalidFormat},
	}
	for _, tt := range tests {
		rec := call(t, &fakeUsers{}, http.MethodPost, "/api/v1/users", "application/json", tt.body)
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %s on %s", tt.body, rec.Code, p.Errors, tt.code, tt.field)
		}
	}

	rec = call(t, &fakeUsers{err: user.ErrEmailTaken}, http.MethodPost, "/api/v1/users", "application/json",
		`{"email":"x@x.example","password":"a long enough password","full_name":"X","role":"DRIVER"}`)
	if p := problem(t, rec); rec.Code != http.StatusConflict || p.Errors[0].Field != "email" || p.Errors[0].Code != "ALREADY_REGISTERED" {
		t.Errorf("taken email: status = %d, problem = %+v", rec.Code, p)
	}
}

func TestGetUser(t *testing.T) {
	id := uuid.New()
	fake := &fakeUsers{}
	if rec := call(t, fake, http.MethodGet, "/api/v1/users/"+id.String(), "", ""); rec.Code != http.StatusOK || fake.gotID != id {
		t.Errorf("status = %d, id = %s", rec.Code, fake.gotID)
	}
	if rec := call(t, &fakeUsers{}, http.MethodGet, "/api/v1/users/not-a-uuid", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id: status = %d, want 404", rec.Code)
	}
}

func TestUpdateUser(t *testing.T) {
	id := uuid.New()
	fake := &fakeUsers{}
	rec := call(t, fake, http.MethodPatch, "/api/v1/users/"+id.String(), "application/merge-patch+json",
		`{"full_name":" Nguyen Thi Cam ","phone":null,"role":"WAREHOUSE_MANAGER","is_active":false}`)
	if rec.Code != http.StatusOK || fake.gotID != id {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	p := fake.gotPatch
	if p.FullName == nil || *p.FullName != "Nguyen Thi Cam" || !p.ClearPhone || p.Phone != nil ||
		p.Role == nil || *p.Role != identity.RoleWarehouseManager || p.IsActive == nil || *p.IsActive {
		t.Errorf("patch = %+v", p)
	}

	fake = &fakeUsers{}
	call(t, fake, http.MethodPatch, "/api/v1/users/"+id.String(), "application/merge-patch+json", `{"phone":" 0901 234 567 "}`)
	if fake.gotPatch.Phone == nil || *fake.gotPatch.Phone != "0901 234 567" || fake.gotPatch.ClearPhone ||
		fake.gotPatch.FullName != nil || fake.gotPatch.Role != nil || fake.gotPatch.IsActive != nil {
		t.Errorf("phone-only patch = %+v", fake.gotPatch)
	}

	tests := []struct {
		body, field, code string
	}{
		{`{"full_name":null}`, "full_name", httpx.FieldRequired},
		{`{"full_name":""}`, "full_name", httpx.FieldRequired},
		{`{"role":"OWNER"}`, "role", httpx.FieldInvalidValue},
		{`{"role":null}`, "role", httpx.FieldInvalidValue},
		{`{"is_active":null}`, "is_active", httpx.FieldRequired},
		{`{"is_active":"no"}`, "is_active", httpx.FieldInvalidType},
		{`{"email":"new@x.example"}`, "email", httpx.FieldUnknown},
		{`{"phone":"12"}`, "phone", httpx.FieldTooShort},
	}
	for _, tt := range tests {
		rec := call(t, &fakeUsers{}, http.MethodPatch, "/api/v1/users/"+id.String(), "application/merge-patch+json", tt.body)
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %s on %s", tt.body, rec.Code, p.Errors, tt.code, tt.field)
		}
	}
	if rec := call(t, &fakeUsers{}, http.MethodPatch, "/api/v1/users/"+id.String(), "text/plain", `{}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text body: status = %d, want 415", rec.Code)
	}
}

func TestUserErrors(t *testing.T) {
	id := uuid.New().String()
	tests := []struct {
		err  error
		want int
	}{
		{&policy.DenialError{Action: policy.ManageUsers, Reason: policy.ReasonRole}, http.StatusForbidden},
		{user.ErrSelfChange, http.StatusForbidden},
		{user.ErrActorNotAdmin, http.StatusForbidden},
		{user.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/users", ""},
			{http.MethodGet, "/api/v1/users/" + id, ""},
			{http.MethodPatch, "/api/v1/users/" + id, `{}`},
		} {
			rec := call(t, &fakeUsers{err: tt.err}, req.method, req.path, "application/merge-patch+json", req.body)
			if rec.Code != tt.want {
				t.Errorf("%s %s with %v: status = %d, want %d", req.method, req.path, tt.err, rec.Code, tt.want)
			}
		}
	}
}
