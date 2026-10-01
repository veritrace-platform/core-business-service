package directory

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Directory looks up directory entries.
type Directory interface {
	Location(ctx context.Context, p identity.Principal, gln string) (Location, error)
	Tenant(ctx context.Context, p identity.Principal, code string) (Tenant, error)
}

// Handler serves the directory endpoints.
type Handler struct {
	directory    Directory
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(directory Directory, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{directory: directory, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/directory/locations/{gln}", h.location)
		r.Get("/directory/tenants/{code}", h.tenant)
	})
}

// location answers 422 for a mistyped GLN, so a scanner or form can tell it from an unknown one (404).
func (h *Handler) location(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	gln := chi.URLParam(r, "gln")
	var v rest.Validator
	v.Key("gln", gs1.Validate(gs1.GLN, gln))
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	l, err := h.directory.Location(r.Context(), p, gln)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, l)
}

// tenant matches the code ignoring case, since tenant codes are upper case.
func (h *Handler) tenant(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	t, err := h.directory.Tenant(r.Context(), p, strings.ToUpper(chi.URLParam(r, "code")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, t)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
