package product_test

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
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/product"
)

// fakeProducts records the calls of the handler and returns canned results.
type fakeProducts struct {
	err       error
	listed    []product.Product
	gotFilter product.Filter
	gotNew    product.NewProduct
	gotPatch  product.Patch
	gotID     uuid.UUID
}

func (f *fakeProducts) List(_ context.Context, _ identity.Principal, flt product.Filter) ([]product.Product, error) {
	f.gotFilter = flt
	return f.listed, f.err
}

func (f *fakeProducts) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (product.Product, error) {
	f.gotID = id
	return product.Product{ID: id}, f.err
}

func (f *fakeProducts) Create(_ context.Context, _ identity.Principal, np product.NewProduct) (product.Product, error) {
	f.gotNew = np
	return product.Product{ID: uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e"), GTIN: np.GTIN}, f.err
}

func (f *fakeProducts) Update(_ context.Context, _ identity.Principal, id uuid.UUID, p product.Patch) (product.Product, error) {
	f.gotID, f.gotPatch = id, p
	return product.Product{ID: id}, f.err
}

func call(t *testing.T, products product.Products, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	principal := identity.Principal{UserID: uuid.New(), TenantID: uuid.New(), Role: identity.RoleWarehouseManager}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
		})
	}
	h := product.NewHandler(products, authenticate, slog.New(slog.DiscardHandler))
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

func TestListProducts(t *testing.T) {
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	fake := &fakeProducts{listed: []product.Product{{ID: ids[1]}, {ID: ids[0]}}}
	rec := call(t, fake, http.MethodGet, "/api/v1/products?limit=1&q=%20milk%20&is_active=true", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_cursor":"`+httpx.UUIDCursor(ids[1])+`"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if f := fake.gotFilter; f.Search != "milk" || f.IsActive == nil || !*f.IsActive || f.Limit != 2 {
		t.Errorf("filter = %+v, want trimmed search, active, limit+1", f)
	}

	for query, field := range map[string]string{"q=" + strings.Repeat("x", 101): "q", "cursor=AAAA": "cursor", "is_active=no": "is_active"} {
		rec := call(t, &fakeProducts{}, http.MethodGet, "/api/v1/products?"+query, "")
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != field {
			t.Errorf("%s: status = %d, errors = %+v; want 400 on %s", query, rec.Code, p.Errors, field)
		}
	}
}

func TestCreateProduct(t *testing.T) {
	fake := &fakeProducts{}
	rec := call(t, fake, http.MethodPost, "/api/v1/products",
		`{"gtin":" 08930001000018 ","name":" Chilled milk ","description":" ","min_temp_celsius":2,"max_temp_celsius":8.5}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/products/0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e" {
		t.Fatalf("status = %d, Location = %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	np := fake.gotNew
	if np.GTIN != "08930001000018" || np.Name != "Chilled milk" || np.Description != nil || np.MinTempCelsius != 2 || np.MaxTempCelsius != 8.5 {
		t.Errorf("new product = %+v, want trimmed values and no description", np)
	}

	const name = `"name":"N","min_temp_celsius":-25,"max_temp_celsius":-18`
	tests := []struct {
		body, field, code string
		status            int
	}{
		{`{` + name + `}`, "gtin", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gtin":"08930001000019",` + name + `}`, "gtin", string(gs1.ReasonCheckDigit), http.StatusUnprocessableEntity},
		{`{"gtin":"8930001000018",` + name + `}`, "gtin", string(gs1.ReasonLength), http.StatusUnprocessableEntity},
		{`{"gtin":"08930001000018","min_temp_celsius":2,"max_temp_celsius":8}`, "name", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gtin":"08930001000018",` + name + `,"description":"` + strings.Repeat("d", 1001) + `"}`, "description", httpx.FieldTooLong, http.StatusBadRequest},
		{`{"gtin":"08930001000018","name":"N","max_temp_celsius":8}`, "min_temp_celsius", httpx.FieldRequired, http.StatusBadRequest},
		{`{"gtin":"08930001000018","name":"N","min_temp_celsius":2,"max_temp_celsius":80.5}`, "max_temp_celsius", httpx.FieldOutOfRange, http.StatusBadRequest},
		{`{"gtin":"08930001000018","name":"N","min_temp_celsius":2.125,"max_temp_celsius":8}`, "min_temp_celsius", httpx.FieldInvalidFormat, http.StatusBadRequest},
		{`{"gtin":"08930001000018","name":"N","min_temp_celsius":8,"max_temp_celsius":8}`, "min_temp_celsius", httpx.FieldOutOfRange, http.StatusBadRequest},
		{`{"gtin":"08930001000018",` + name + `,"is_active":false}`, "is_active", httpx.FieldUnknown, http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := call(t, &fakeProducts{}, http.MethodPost, "/api/v1/products", tt.body)
		p := problem(t, rec)
		if rec.Code != tt.status || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%.80s: status = %d, errors = %+v; want %d with %s on %s", tt.body, rec.Code, p.Errors, tt.status, tt.code, tt.field)
		}
	}
}

func TestUpdateProduct(t *testing.T) {
	id := uuid.New()
	fake := &fakeProducts{}
	rec := call(t, fake, http.MethodPatch, "/api/v1/products/"+id.String(),
		`{"name":" Chilled milk 1 L ","description":"Pasteurized","min_temp_celsius":1.5,"max_temp_celsius":6,"is_active":false}`)
	if rec.Code != http.StatusOK || fake.gotID != id {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	p := fake.gotPatch
	if p.Name == nil || *p.Name != "Chilled milk 1 L" || p.Description == nil || *p.Description != "Pasteurized" ||
		p.ClearDescription || *p.MinTempCelsius != 1.5 || *p.MaxTempCelsius != 6 || p.IsActive == nil || *p.IsActive {
		t.Errorf("patch = %+v", p)
	}
	fake = &fakeProducts{}
	call(t, fake, http.MethodPatch, "/api/v1/products/"+id.String(), `{"description":null}`)
	if p := fake.gotPatch; !p.ClearDescription || p.Description != nil || p.Name != nil || p.MinTempCelsius != nil {
		t.Errorf("clearing the description: patch = %+v", p)
	}

	tests := []struct {
		body, field, code string
	}{
		{`{"gtin":"08930001000025"}`, "gtin", httpx.FieldUnknown},
		{`{"name":null}`, "name", httpx.FieldRequired},
		{`{"description":5}`, "description", httpx.FieldInvalidType},
		{`{"min_temp_celsius":null}`, "min_temp_celsius", httpx.FieldRequired},
		{`{"max_temp_celsius":-51}`, "max_temp_celsius", httpx.FieldOutOfRange},
		{`{"max_temp_celsius":4.001}`, "max_temp_celsius", httpx.FieldInvalidFormat},
		{`{"min_temp_celsius":9,"max_temp_celsius":4}`, "min_temp_celsius", httpx.FieldOutOfRange},
		{`{"is_active":null}`, "is_active", httpx.FieldRequired},
	}
	for _, tt := range tests {
		rec := call(t, &fakeProducts{}, http.MethodPatch, "/api/v1/products/"+id.String(), tt.body)
		p := problem(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tt.field || p.Errors[0].Code != tt.code {
			t.Errorf("%s: status = %d, errors = %+v; want %s on %s", tt.body, rec.Code, p.Errors, tt.code, tt.field)
		}
	}

	// A bound that crosses the stored other bound is reported on the bound that was patched.
	for body, field := range map[string]string{`{"min_temp_celsius":9}`: "min_temp_celsius", `{"max_temp_celsius":1}`: "max_temp_celsius"} {
		rec := call(t, &fakeProducts{err: product.ErrTemperatureRange}, http.MethodPatch, "/api/v1/products/"+id.String(), body)
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[0].Field != field || p.Errors[0].Code != httpx.FieldOutOfRange {
			t.Errorf("%s crossing the stored bound: status = %d, problem = %+v", body, rec.Code, p)
		}
	}
}

func TestProductErrors(t *testing.T) {
	id := uuid.New().String()
	valid := `{"gtin":"08930001000018","name":"N","min_temp_celsius":2,"max_temp_celsius":8}`
	tests := []struct {
		err  error
		want int
	}{
		{&policy.DenialError{Action: policy.ManageProducts, Reason: policy.ReasonRole}, http.StatusForbidden},
		{product.ErrNotFound, http.StatusNotFound},
		{errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/products", ""},
			{http.MethodGet, "/api/v1/products/" + id, ""},
			{http.MethodPatch, "/api/v1/products/" + id, `{}`},
		} {
			if rec := call(t, &fakeProducts{err: tt.err}, req.method, req.path, req.body); rec.Code != tt.want {
				t.Errorf("%s %s with %v: status = %d, want %d", req.method, req.path, tt.err, rec.Code, tt.want)
			}
		}
	}

	rec := call(t, &fakeProducts{err: product.ErrGTINTaken}, http.MethodPost, "/api/v1/products", valid)
	if p := problem(t, rec); rec.Code != http.StatusConflict || p.Code != "IDENTIFIER_ALREADY_REGISTERED" || p.Errors[0].Field != "gtin" {
		t.Errorf("taken GTIN: status = %d, problem = %+v", rec.Code, p)
	}
	foreign := gs1.ValidateGTIN14("08934567001014", "8930001")
	rec = call(t, &fakeProducts{err: foreign}, http.MethodPost, "/api/v1/products", valid)
	if p := problem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Errors[0].Field != "gtin" ||
		p.Errors[0].Code != string(gs1.ReasonPrefixMismatch) {
		t.Errorf("foreign GTIN: status = %d, problem = %+v", rec.Code, p)
	}
	if rec := call(t, &fakeProducts{}, http.MethodGet, "/api/v1/products/42", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}
