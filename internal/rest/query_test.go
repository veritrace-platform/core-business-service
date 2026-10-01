package rest_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

func TestPage(t *testing.T) {
	id := uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e")
	var v rest.Validator
	page, after := v.Page(httptest.NewRequest(http.MethodGet, "/x?limit=5&cursor="+httpx.UUIDCursor(id), nil))
	if !v.Valid() || page.Limit != 5 || after != id {
		t.Errorf("Page() = %+v, %s; errors %v", page, after, codes(v.Problem()))
	}

	v = rest.Validator{}
	page, after = v.Page(httptest.NewRequest(http.MethodGet, "/x", nil))
	if !v.Valid() || page.Limit != httpx.DefaultPageLimit || after != uuid.Nil {
		t.Errorf("first page = %+v, %s", page, after)
	}

	v = rest.Validator{}
	v.Page(httptest.NewRequest(http.MethodGet, "/x?limit=0&cursor=bm90LWEtdXVpZA", nil))
	got := codes(v.Problem())
	if got["limit"] != httpx.FieldOutOfRange || got["cursor"] != httpx.FieldInvalidFormat {
		t.Errorf("invalid page: errors = %v", got)
	}
}

func TestQueryParams(t *testing.T) {
	id := uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e")
	var v rest.Validator
	r := httptest.NewRequest(http.MethodGet, "/x?is_active=false&lot_id="+id.String(), nil)
	active, lot, missing := v.BoolParam(r, "is_active"), v.UUIDParam(r, "lot_id"), v.UUIDParam(r, "location_id")
	if !v.Valid() || active == nil || *active || lot == nil || *lot != id || missing != nil {
		t.Errorf("params = %v, %v, %v; errors %v", active, lot, missing, codes(v.Problem()))
	}
	if v.BoolParam(r, "absent") != nil {
		t.Error("an absent boolean was set")
	}

	v = rest.Validator{}
	r = httptest.NewRequest(http.MethodGet, "/x?is_active=yes&lot_id=42", nil)
	if v.BoolParam(r, "is_active") != nil || v.UUIDParam(r, "lot_id") != nil {
		t.Error("invalid parameters were returned")
	}
	got := codes(v.Problem())
	if got["is_active"] != httpx.FieldInvalidValue || got["lot_id"] != httpx.FieldInvalidFormat {
		t.Errorf("invalid parameters: errors = %v", got)
	}
}

func TestPathID(t *testing.T) {
	id := uuid.MustParse("0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e")
	var got uuid.UUID
	r := chi.NewRouter()
	r.Get("/things/{thing_id}", func(w http.ResponseWriter, r *http.Request) {
		if parsed, ok := rest.PathID(w, r, "thing_id"); ok {
			got = parsed
			w.WriteHeader(http.StatusNoContent)
		}
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/"+id.String(), nil))
	if rec.Code != http.StatusNoContent || got != id {
		t.Errorf("valid ID: status = %d, id = %s", rec.Code, got)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/42", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a UUID: status = %d, want 404", rec.Code)
	}
}
