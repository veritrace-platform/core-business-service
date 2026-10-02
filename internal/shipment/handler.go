package shipment

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Shipments runs shipment commands and reads for the caller.
type Shipments interface {
	Create(ctx context.Context, p identity.Principal, ns NewShipment) (Shipment, error)
	Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Shipment, error)
	List(ctx context.Context, p identity.Principal, f Filter) ([]Shipment, error)
	Summary(ctx context.Context, p identity.Principal) (Summary, error)
	AssignCarrier(ctx context.Context, p identity.Principal, id uuid.UUID, carrierCode string) (Shipment, error)
	AssignDriver(ctx context.Context, p identity.Principal, id, driverID uuid.UUID) (Shipment, error)
	Cancel(ctx context.Context, p identity.Principal, id uuid.UUID, reason string) (Shipment, error)
	Events(ctx context.Context, p identity.Principal, id uuid.UUID, page event.Page) ([]event.Event, error)
	Integrity(ctx context.Context, p identity.Principal, id uuid.UUID) (event.Integrity, error)
}

// Handler serves the shipment endpoints.
type Handler struct {
	shipments    Shipments
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(shipments Shipments, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{shipments: shipments, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/shipments", h.list)
		r.Post("/shipments", h.create)
		r.Get("/shipments/summary", h.summary)
		r.Get("/shipments/{shipment_id}", h.get)
		r.Post("/shipments/{shipment_id}/carrier", h.assignCarrier)
		r.Post("/shipments/{shipment_id}/driver", h.assignDriver)
		r.Post("/shipments/{shipment_id}/cancel", h.cancel)
		r.Get("/shipments/{shipment_id}/events", h.events)
		r.Get("/shipments/{shipment_id}/integrity", h.integrity)
	})
}

var (
	statuses = []policy.ShipmentStatus{
		policy.ShipmentCreated, policy.ShipmentInTransit, policy.ShipmentDelivered, policy.ShipmentCancelled,
		policy.ShipmentRecalled,
	}
	roles = []Role{RoleOwner, RoleCarrier, RoleConsignee, RoleInspector}
)

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var v rest.Validator
	page, after := v.Page(r)
	q := r.URL.Query()
	f := Filter{After: after, Limit: page.Limit + 1, LotID: v.UUIDParam(r, "lot_id")}
	if raw := q.Get("status"); raw != "" {
		if status := policy.ShipmentStatus(raw); slices.Contains(statuses, status) {
			f.Status = &status
		} else {
			v.Add("status", httpx.FieldInvalidValue, "must be CREATED, IN_TRANSIT, DELIVERED, CANCELLED, or RECALLED")
		}
	}
	if raw := q.Get("party"); raw != "" {
		if party := Role(raw); slices.Contains(roles, party) {
			f.Party = &party
		} else {
			v.Add("party", httpx.FieldInvalidValue, "must be OWNER, CARRIER, CONSIGNEE, or INSPECTOR")
		}
	}
	if raw := q.Get("sscc"); raw != "" && v.Key("sscc", gs1.ValidateSSCC(raw)) {
		f.SSCC = raw
	}
	if mine := v.BoolParam(r, "assigned_to_me"); mine != nil && *mine {
		f.DriverID = &p.UserID
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	shipments, err := h.shipments.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(shipments, page.Limit, func(s Shipment) string {
		return httpx.UUIDCursor(s.ID)
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
	ns := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	created, err := h.shipments.Create(r.Context(), p, ns)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "shipment created", slog.String("shipment_id", created.ID.String()),
		slog.String("sscc", created.SSCC))
	w.Header().Set("Location", "/api/v1/shipments/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	summary, err := h.shipments.Summary(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, summary)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "shipment_id")
	if !ok {
		return
	}
	s, err := h.shipments.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, s)
}

// decodeCommand reads the shipment ID and the JSON body of a command, and reports whether both are valid.
func decodeCommand(w http.ResponseWriter, r *http.Request, body any) (identity.Principal, uuid.UUID, bool) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return p, uuid.Nil, false
	}
	id, ok := rest.PathID(w, r, "shipment_id")
	if !ok {
		return p, uuid.Nil, false
	}
	if problem := httpx.DecodeJSON(w, r, body); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return p, uuid.Nil, false
	}
	return p, id, true
}

// respond answers a command with the updated shipment, after logging it.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request, s Shipment, err error, msg string) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), msg, slog.String("shipment_id", s.ID.String()), slog.String("status", string(s.Status)))
	httpx.WriteJSON(w, r, http.StatusOK, s)
}

func (h *Handler) assignCarrier(w http.ResponseWriter, r *http.Request) {
	var req carrierRequest
	p, id, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	var v rest.Validator
	code := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	s, err := h.shipments.AssignCarrier(r.Context(), p, id, code)
	h.respond(w, r, s, err, "carrier assigned")
}

func (h *Handler) assignDriver(w http.ResponseWriter, r *http.Request) {
	var req driverRequest
	p, id, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	var v rest.Validator
	driverID := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	s, err := h.shipments.AssignDriver(r.Context(), p, id, driverID)
	h.respond(w, r, s, err, "driver assigned")
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	p, id, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	var v rest.Validator
	reason := req.validate(&v)
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	s, err := h.shipments.Cancel(r.Context(), p, id, reason)
	h.respond(w, r, s, err, "shipment cancelled")
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "shipment_id")
	if !ok {
		return
	}
	var v rest.Validator
	page, errs := httpx.ParsePage(r)
	for _, e := range errs {
		v.Add(e.Field, e.Code, e.Message)
	}
	after := 0
	if page.Cursor != "" {
		var err error
		if after, err = parseSequenceCursor(page.Cursor); err != nil {
			e := httpx.InvalidCursorError()
			v.Add(e.Field, e.Code, e.Message)
		}
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	events, err := h.shipments.Events(r.Context(), p, id, event.Page{After: after, Limit: page.Limit + 1})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(events, page.Limit, func(e event.Event) string {
		return sequenceCursor(e.Sequence)
	}))
}

// sequenceCursor encodes the sequence of the last event of a page.
func sequenceCursor(sequence int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(sequence)))
}

// parseSequenceCursor decodes a cursor made by sequenceCursor.
func parseSequenceCursor(cursor string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, httpx.ErrInvalidCursor
	}
	sequence, err := strconv.Atoi(string(b))
	if err != nil || sequence < 1 {
		return 0, httpx.ErrInvalidCursor
	}
	return sequence, nil
}

func (h *Handler) integrity(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := rest.PathID(w, r, "shipment_id")
	if !ok {
		return
	}
	result, err := h.shipments.Integrity(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !result.Valid {
		h.logger.WarnContext(r.Context(), "shipment event chain broken", slog.String("shipment_id", id.String()),
			slog.Int("first_invalid_sequence", *result.FirstInvalidSequence))
	}
	httpx.WriteJSON(w, r, http.StatusOK, result)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *InvalidReferenceError
	var denial *policy.DenialError
	switch {
	case errors.As(err, &invalid):
		httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{
			{Field: invalid.Field, Code: httpx.FieldInvalidValue, Message: invalid.Message},
		}))
	case errors.Is(err, ErrInsufficientStock):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusConflict, rest.CodeInsufficientStock, err.Error()))
	case errors.Is(err, ErrSerialSpaceExhausted):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusConflict, rest.CodeSSCCSerialExhausted,
			"the SSCC serial space is used up; change the tenant's SSCC extension digit"))
	case errors.As(err, &denial) && denial.Check == policy.NoExternalCarrier:
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusConflict, rest.CodeInvalidStateTransition,
			"another tenant already carries the shipment"))
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
