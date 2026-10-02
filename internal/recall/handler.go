package recall

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Recaller recalls lots for the caller.
type Recaller interface {
	Recall(ctx context.Context, p identity.Principal, lotID uuid.UUID, reason string) (Recall, error)
}

// Handler serves the recall endpoint.
type Handler struct {
	recaller     Recaller
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(recaller Recaller, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{recaller: recaller, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoint under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.With(h.authenticate).Post("/lots/{lot_id}/recall", h.recall)
}

const maxReasonLength = 1000

type recallRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) recall(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	lotID, ok := rest.PathID(w, r, "lot_id")
	if !ok {
		return
	}
	var req recallRequest
	if problem := httpx.DecodeJSON(w, r, &req); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	v.Text("reason", &req.Reason, 1, maxReasonLength)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	recall, err := h.recaller.Recall(r.Context(), p, lotID, req.Reason)
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
	default:
		h.logger.WarnContext(r.Context(), "lot recalled", slog.String("lot_id", lotID.String()),
			slog.String("recall_id", recall.ID.String()), slog.Int("affected_shipments", recall.AffectedShipmentCount))
		httpx.WriteJSON(w, r, http.StatusCreated, recall)
	}
}
