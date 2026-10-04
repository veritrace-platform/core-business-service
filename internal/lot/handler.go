package lot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Lots commissions and reads lots.
type Lots interface {
	List(ctx context.Context, p identity.Principal, f Filter) ([]Lot, error)
	Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Lot, error)
	Commission(ctx context.Context, p identity.Principal, nl NewLot) (Lot, error)
}

// Handler serves the lot endpoints.
type Handler struct {
	lots         Lots
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(lots Lots, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{lots: lots, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/lots", h.list)
		r.Post("/lots", h.commission)
		r.Get("/lots/{lot_id}", h.get)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var v rest.Validator
	page, after := v.Page(r)
	f := Filter{ProductID: v.UUIDParam(r, "product_id"), After: after, Limit: page.Limit + 1}
	switch status := policy.LotStatus(r.URL.Query().Get("status")); status {
	case "":
	case policy.LotActive, policy.LotRecalled:
		f.Status = &status
	default:
		v.Add("status", httpx.FieldInvalidValue, "must be ACTIVE or RECALLED")
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	lots, err := h.lots.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(lots, page.Limit, func(l Lot) string {
		return httpx.UUIDCursor(l.ID)
	}))
}

func (h *Handler) commission(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var req commissionRequest
	if problem := httpx.DecodeJSON(w, r, &req); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	nl := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	created, err := h.lots.Commission(r.Context(), p, nl)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "lot commissioned", slog.String("lot_id", created.ID.String()),
		slog.Int("quantity", created.QuantityCommissioned))
	w.Header().Set("Location", "/api/v1/lots/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "lot_id")
	if !ok {
		return
	}
	l, err := h.lots.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, l)
}

// invalidReference answers 400 for a product or location that commissioning cannot use.
func invalidReference(w http.ResponseWriter, r *http.Request, field, message string) {
	httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{
		{Field: field, Code: httpx.FieldInvalidValue, Message: message},
	}))
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var denial *policy.DenialError
	failedCheck := errors.As(err, &denial) && denial.Reason == policy.ReasonCheck
	switch {
	case failedCheck && denial.Check == policy.ProductOwned:
		invalidReference(w, r, "product_id", "is not a product of your tenant")
	case failedCheck && denial.Check == policy.LocationOwned:
		invalidReference(w, r, "commissioned_location_id", "is not a location of your tenant")
	case errors.Is(err, ErrProductInactive):
		invalidReference(w, r, "product_id", "is inactive")
	case errors.Is(err, ErrLocationInactive):
		invalidReference(w, r, "commissioned_location_id", "is inactive")
	case errors.Is(err, ErrLotNumberTaken):
		rest.Conflict(w, r, "lot_number", "is already used for this product")
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
