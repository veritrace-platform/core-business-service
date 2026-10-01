package inventory

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Inventory reads the balances of the caller's tenant.
type Inventory interface {
	List(ctx context.Context, p identity.Principal, f Filter) ([]Balance, error)
}

// Handler serves the inventory endpoint.
type Handler struct {
	inventory    Inventory
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(inventory Inventory, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{inventory: inventory, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoint under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.With(h.authenticate).Get("/inventory", h.list)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var v rest.Validator
	page, errs := httpx.ParsePage(r)
	for _, e := range errs {
		v.Add(e.Field, e.Code, e.Message)
	}
	f := Filter{LocationID: v.UUIDParam(r, "location_id"), LotID: v.UUIDParam(r, "lot_id"), Limit: page.Limit + 1}
	if page.Cursor != "" {
		if after, err := parseCursor(page.Cursor); err == nil {
			f.After = &after
		} else {
			e := httpx.InvalidCursorError()
			v.Add(e.Field, e.Code, e.Message)
		}
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	balances, err := h.inventory.List(r.Context(), p, f)
	switch {
	case rest.Denied(w, r, err):
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
	default:
		httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(balances, page.Limit, func(b Balance) string {
			return cursor(Key{LocationID: b.Location.ID, LotID: b.Lot.ID})
		}))
	}
}

// cursor encodes the key of the last balance of a page.
func cursor(k Key) string {
	b := make([]byte, 0, 2*len(uuid.UUID{}))
	b = append(append(b, k.LocationID[:]...), k.LotID[:]...)
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseCursor decodes a cursor made by cursor.
func parseCursor(s string) (Key, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 2*len(uuid.UUID{}) {
		return Key{}, httpx.ErrInvalidCursor
	}
	return Key{LocationID: uuid.UUID(b[:16]), LotID: uuid.UUID(b[16:])}, nil
}
