package location

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Locations manages the locations of the caller's tenant.
type Locations interface {
	List(ctx context.Context, p identity.Principal, f Filter) ([]Location, error)
	Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Location, error)
	Create(ctx context.Context, p identity.Principal, nl NewLocation) (Location, error)
	Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (Location, error)
}

// Handler serves the location endpoints.
type Handler struct {
	locations    Locations
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(locations Locations, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{locations: locations, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/locations", h.list)
		r.Post("/locations", h.create)
		r.Get("/locations/{location_id}", h.get)
		r.Patch("/locations/{location_id}", h.update)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var v rest.Validator
	page, after := v.Page(r)
	f := Filter{After: after, Limit: page.Limit + 1, IsActive: v.BoolParam(r, "is_active")}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	locations, err := h.locations.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(locations, page.Limit, func(l Location) string {
		return httpx.UUIDCursor(l.ID)
	}))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var req Request
	if problem := httpx.DecodeJSON(w, r, &req); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	// The service checks the company prefix, which only the tenant's record holds.
	nl := req.Validate(&v, "", func(gln string) error { return gs1.Validate(gs1.GLN, gln) })
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	created, err := h.locations.Create(r.Context(), p, nl)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "location created", slog.String("location_id", created.ID.String()))
	w.Header().Set("Location", "/api/v1/locations/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "location_id")
	if !ok {
		return
	}
	l, err := h.locations.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, l)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "location_id")
	if !ok {
		return
	}
	var patch rest.Patch
	if problem := httpx.DecodeJSON(w, r, &patch); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	changes := readPatch(&v, patch)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	updated, err := h.locations.Update(r.Context(), p, id, changes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "location updated", slog.String("location_id", updated.ID.String()))
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalidKey *gs1.InvalidKeyError
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, ErrGLNTaken):
		rest.Conflict(w, r, "gln", "is already registered")
	case errors.As(err, &invalidKey):
		var v rest.Validator
		v.Key("gln", invalidKey)
		httpx.WriteProblem(w, r, *v.Problem())
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
