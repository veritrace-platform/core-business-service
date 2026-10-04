package product

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Products manages the products of the caller's tenant.
type Products interface {
	List(ctx context.Context, p identity.Principal, f Filter) ([]Product, error)
	Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Product, error)
	Create(ctx context.Context, p identity.Principal, np NewProduct) (Product, error)
	Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (Product, error)
}

// Handler serves the product endpoints.
type Handler struct {
	products     Products
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(products Products, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{products: products, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/products", h.list)
		r.Post("/products", h.create)
		r.Get("/products/{product_id}", h.get)
		r.Patch("/products/{product_id}", h.update)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var v rest.Validator
	page, after := v.Page(r)
	f := Filter{
		Search: strings.TrimSpace(r.URL.Query().Get("q")), IsActive: v.BoolParam(r, "is_active"),
		After: after, Limit: page.Limit + 1,
	}
	if utf8.RuneCountInString(f.Search) > maxSearchLength {
		v.Add("q", httpx.FieldTooLong, fmt.Sprintf("must be at most %d characters", maxSearchLength))
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	products, err := h.products.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(products, page.Limit, func(product Product) string {
		return httpx.UUIDCursor(product.ID)
	}))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if problem := httpx.DecodeJSON(w, r, &req); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	np := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	created, err := h.products.Create(r.Context(), p, np)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "product created", slog.String("product_id", created.ID.String()))
	w.Header().Set("Location", "/api/v1/products/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "product_id")
	if !ok {
		return
	}
	product, err := h.products.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, product)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "product_id")
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

	updated, err := h.products.Update(r.Context(), p, id, changes)
	if errors.Is(err, ErrTemperatureRange) {
		// Only one bound was patched, or the other would have been checked with it.
		e := httpx.FieldError{Field: "min_temp_celsius", Code: httpx.FieldOutOfRange, Message: minBelowMax}
		if changes.MinTempCelsius == nil {
			e = httpx.FieldError{Field: "max_temp_celsius", Code: httpx.FieldOutOfRange, Message: maxAboveMin}
		}
		httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{e}))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "product updated", slog.String("product_id", updated.ID.String()))
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalidKey *gs1.InvalidKeyError
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, ErrGTINTaken):
		rest.Conflict(w, r, "gtin", "is already registered")
	case errors.As(err, &invalidKey):
		var v rest.Validator
		v.Key("gtin", invalidKey)
		httpx.WriteProblem(w, r, *v.Problem())
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
