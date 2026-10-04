package location_test

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

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// fakeLocations records the calls of the handler and returns canned results.
type fakeLocations struct {
	err       error
	listed    []location.Location
	gotFilter location.Filter
	gotNew    location.NewLocation
	gotPatch  location.Patch
	gotID     uuid.UUID
}

func (f *fakeLocations) List(_ context.Context, _ identity.Principal, flt location.Filter) ([]location.Location, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func (f *fakeLocations) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (location.Location, error) {
	f.gotID = id
	return location.Location{ID: id}, f.err
}

func (f *fakeLocations) Create(_ context.Context, _ identity.Principal, nl location.NewLocation) (location.Location, error) {
	f.gotNew = nl
	return location.Location{ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e"), GLN: nl.GLN}, f.err
}

func (f *fakeLocations) Update(_ context.Context, _ identity.Principal, id uuid.UUID, p location.Patch) (location.Location, error) {
	f.gotID, f.gotPatch = id, p
	return location.Location{ID: id}, f.err
}

func call(t *testing.T, locations location.Locations, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleAdmin}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := location.NewHandler(locations, authenticate, slog.New(slog.DiscardHandler))
	router := httpapi.NewRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), httpapi.Mounts{API: []httpapi.Routes{h.Routes}})

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
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

func TestListLocations(t *testing.T) {
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	fake := &fakeLocations{listed: []location.Location{{ID: ids[2]}, {ID: ids[1]}, {ID: ids[0]}}}
	rec := call(t, fake, http.MethodGet, "/api/v1/locations?limit=2&is_active=true&cursor="+httpx.UUIDCursor(ids[2]), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var page struct {
		Items      []location.Location `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || *page.NextCursor != httpx.UUIDCursor(ids[1]) {
		t.Errorf("page = %+v, want two items and a cursor after the second", page)
	}
	if f := fake.gotFilter; f.Limit != 3 || f.IsActive == nil || !*f.IsActive || f.After != ids[2] {
		t.Errorf("filter = %+v, want limit+1, active, after the cursor", f)
	}

	for query, field := range map[string]string{"limit=101": "limit", "cursor=AAAA": "cursor", "is_active=1": "is_active"} {
		rec := call(t, &fakeLocations{}, http.MethodGet, "/api/v1/locations?"+query, "")
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != field {
			t.Errorf("%s: status = %d, errors = %+v; want 400 on %s", query, rec.Code, p.Errors, field)
		}
	}
}

func TestCreateLocation(t *testing.T) {
	fake := &fakeLocations{}
	rec := call(t, fake, http.MethodPost, "/api/v1/locations", `{"gln":" 8930001000018 ","name":" Cold Store ",
		"address":"5 Tan Thuan","city":"Ho Chi Minh City","latitude":10.76262249,"longitude":106.7428,"geo_fence_radius_meters":300}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/locations/0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e" {
		t.Fatalf("status = %d, Location = %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	nl := fake.gotNew
	if nl.GLN != "8930001000018" || nl.Name != "Cold Store" || nl.CountryCode != "VN" || nl.Latitude != 10.762622 ||
		nl.Longitude != 106.7428 || nl.GeoFenceRadiusMeters != 300 {
		t.Errorf("new location = %+v, want trimmed values, the default country, and rounded coordinates", nl)
	}
	call(t, fake, http.MethodPost, "/api/v1/locations",
		`{"gln":"8930001000018","name":"N","address":"A","city":"C","country_code":"TH","latitude":0,"longitude":0}`)
	if fake.gotNew.GeoFenceRadiusMeters != location.DefaultGeoFenceRadiusMeters || fake.gotNew.CountryCode != "TH" {
		t.Errorf("new location = %+v, want the default radius and country TH", fake.gotNew)
	}

	const rest = `"name":"N","address":"A","city":"C","latitude":10,"longitude":106`
	tests := []struct {
		body, field, code string
		status            int
	}{
		{`{` + rest + `}`, "gln", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gln":"8930001000019",` + rest + `}`, "gln", string(gs1.ReasonCheckDigit), http.StatusUnprocessableEntity},
		{`{"gln":"893000100001",` + rest + `}`, "gln", string(gs1.ReasonLength), http.StatusUnprocessableEntity},
		{`{"gln":"8930001000018","address":"A","city":"C","latitude":10,"longitude":106}`, "name", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gln":"8930001000018",` + rest + `,"country_code":"vn"}`, "country_code", httpx.FieldInvalidFormat, http.StatusBadRequest},
		{`{"gln":"8930001000018","name":"N","address":"A","city":"C","longitude":106}`, "latitude", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gln":"8930001000018","name":"N","address":"A","city":"C","latitude":10,"longitude":181}`, "longitude", httpx.FieldOutOfRange, http.StatusBadRequest},
		{`{"gln":"8930001000018",` + rest + `,"geo_fence_radius_meters":49}`, "geo_fence_radius_meters", httpx.FieldOutOfRange, http.StatusBadRequest},
		{`{"gln":"8930001000018",` + rest + `,"is_headquarters":true}`, "is_headquarters", httpx.FieldUnknown, http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := call(t, &fakeLocations{}, http.MethodPost, "/api/v1/locations", tt.body)
		p := problem(t, rec)
		if rec.Code != tt.status || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %d with %s on %s", tt.body, rec.Code, p.Errors, tt.status, tt.code, tt.field)
		}
	}

	// A mistyped GLN next to another invalid field is listed with it in a 400.
	rec = call(t, &fakeLocations{}, http.MethodPost, "/api/v1/locations", `{"gln":"8930001000019","address":"A","city":"C","latitude":10,"longitude":106}`)
	if p := problem(t, rec); rec.Code != http.StatusBadRequest || p.Code != httpx.CodeValidationFailed || len(p.Errors) != 2 {
		t.Errorf("GLN and name invalid: status = %d, problem = %+v", rec.Code, p)
	}
}

func TestUpdateLocation(t *testing.T) {
	id := uuid.New()
	fake := &fakeLocations{}
	rec := call(t, fake, http.MethodPatch, "/api/v1/locations/"+id.String(), `{"name":" Hub 2 ","address":"9 Le Loi",
		"city":"Da Nang","country_code":"VN","latitude":16.0544074,"longitude":108.2021667,"geo_fence_radius_meters":500,"is_active":false}`)
	if rec.Code != http.StatusOK || fake.gotID != id {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	p := fake.gotPatch
	if p.Name == nil || *p.Name != "Hub 2" || p.Address == nil || p.City == nil || p.CountryCode == nil ||
		p.Latitude == nil || *p.Latitude != 16.054407 || p.Longitude == nil || *p.Longitude != 108.202167 ||
		p.GeoFenceRadiusMeters == nil || *p.GeoFenceRadiusMeters != 500 || p.IsActive == nil || *p.IsActive {
		t.Errorf("patch = %+v", p)
	}

	fake = &fakeLocations{}
	call(t, fake, http.MethodPatch, "/api/v1/locations/"+id.String(), `{"is_active":true}`)
	if p := fake.gotPatch; p.IsActive == nil || !*p.IsActive || p.Name != nil || p.Latitude != nil || p.GeoFenceRadiusMeters != nil {
		t.Errorf("activation-only patch = %+v", p)
	}

	tests := []struct {
		body, field, code string
	}{
		{`{"gln":"8930001000025"}`, "gln", httpx.FieldUnknown},
		{`{"is_headquarters":true}`, "is_headquarters", httpx.FieldUnknown},
		{`{"name":null}`, "name", httpx.FieldRequired},
		{`{"address":" "}`, "address", httpx.FieldRequired},
		{`{"city":7}`, "city", httpx.FieldInvalidType},
		{`{"country_code":null}`, "country_code", httpx.FieldRequired},
		{`{"country_code":"Vietnam"}`, "country_code", httpx.FieldInvalidFormat},
		{`{"latitude":null}`, "latitude", httpx.FieldRequired},
		{`{"latitude":-91}`, "latitude", httpx.FieldOutOfRange},
		{`{"longitude":"106"}`, "longitude", httpx.FieldInvalidType},
		{`{"geo_fence_radius_meters":5001}`, "geo_fence_radius_meters", httpx.FieldOutOfRange},
		{`{"geo_fence_radius_meters":null}`, "geo_fence_radius_meters", httpx.FieldRequired},
		{`{"is_active":null}`, "is_active", httpx.FieldRequired},
	}
	for _, tt := range tests {
		rec := call(t, &fakeLocations{}, http.MethodPatch, "/api/v1/locations/"+id.String(), tt.body)
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %s on %s", tt.body, rec.Code, p.Errors, tt.code, tt.field)
		}
	}
}

func TestLocationErrors(t *testing.T) {
	id := uuid.New().String()
	valid := `{"gln":"8930001000018","name":"N","address":"A","city":"C","latitude":10,"longitude":106}`
	tests := []struct {
		err  error
		want int
	}{
		{&policy.DenialError{Action: policy.ManageLocations, Reason: policy.ReasonRole}, http.StatusForbidden},
		{location.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/locations", ""},
			{http.MethodPost, "/api/v1/locations", valid},
			{http.MethodGet, "/api/v1/locations/" + id, ""},
			{http.MethodPatch, "/api/v1/locations/" + id, `{}`},
		} {
			if req.method == http.MethodPost && errors.Is(tt.err, location.ErrNotFound) {
				continue
			}
			if rec := call(t, &fakeLocations{err: tt.err}, req.method, req.path, req.body); rec.Code != tt.want {
				t.Errorf("%s %s with %v: status = %d, want %d", req.method, req.path, tt.err, rec.Code, tt.want)
			}
		}
	}

	rec := call(t, &fakeLocations{err: location.ErrGLNTaken}, http.MethodPost, "/api/v1/locations", valid)
	if p := problem(t, rec); rec.Code != http.StatusConflict || p.Code != "IDENTIFIER_ALREADY_REGISTERED" || p.Errors[0].Field != "gln" {
		t.Errorf("taken GLN: status = %d, problem = %+v", rec.Code, p)
	}

	foreign := gs1.ValidateGLN("8934567000017", "8930001")
	rec = call(t, &fakeLocations{err: foreign}, http.MethodPost, "/api/v1/locations", valid)
	if p := problem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != "INVALID_GS1_IDENTIFIER" ||
		p.Errors[0].Field != "gln" || p.Errors[0].Code != string(gs1.ReasonPrefixMismatch) {
		t.Errorf("foreign GLN: status = %d, problem = %+v", rec.Code, p)
	}

	if rec := call(t, &fakeLocations{}, http.MethodGet, "/api/v1/locations/42", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}
