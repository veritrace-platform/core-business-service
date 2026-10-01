package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
)

// Page reads the limit and cursor of a collection whose cursor is the UUIDv7 key of the last item. after is
// uuid.Nil on the first page.
func (v *Validator) Page(r *http.Request) (page httpx.Page, after uuid.UUID) {
	page, errs := httpx.ParsePage(r)
	for _, e := range errs {
		v.Add(e.Field, e.Code, e.Message)
	}
	if page.Cursor != "" {
		var err error
		if after, err = httpx.ParseUUIDCursor(page.Cursor); err != nil {
			e := httpx.InvalidCursorError()
			v.Add(e.Field, e.Code, e.Message)
		}
	}
	return page, after
}

// BoolParam reads an optional query parameter that is true or false.
func (v *Validator) BoolParam(r *http.Request, name string) *bool {
	switch raw := r.URL.Query().Get(name); raw {
	case "":
		return nil
	case "true", "false":
		value := raw == "true"
		return &value
	default:
		v.Add(name, httpx.FieldInvalidValue, "must be true or false")
		return nil
	}
}

// UUIDParam reads an optional query parameter that is a UUID.
func (v *Validator) UUIDParam(r *http.Request, name string) *uuid.UUID {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		v.Add(name, httpx.FieldInvalidFormat, "must be a UUID")
		return nil
	}
	return &id
}

// PathID reads a path parameter that identifies a resource. An ID that is not a UUID cannot name a visible
// resource, so it answers 404 like any other invisible resource, and ok is false.
func PathID(w http.ResponseWriter, r *http.Request, name string) (id uuid.UUID, ok bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.NotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}
