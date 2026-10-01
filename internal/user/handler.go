package user

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

// Users manages the users of the caller's tenant.
type Users interface {
	List(ctx context.Context, p identity.Principal, f Filter) ([]User, error)
	Get(ctx context.Context, p identity.Principal, id uuid.UUID) (User, error)
	Create(ctx context.Context, p identity.Principal, u NewUser) (User, error)
	Update(ctx context.Context, p identity.Principal, id uuid.UUID, patch Patch) (User, error)
}

// Handler serves the user management endpoints.
type Handler struct {
	users        Users
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. authenticate requires a valid access token.
func NewHandler(users Users, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{users: users, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/users", h.list)
		r.Post("/users", h.create)
		r.Get("/users/{user_id}", h.get)
		r.Patch("/users/{user_id}", h.update)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	page, errs := httpx.ParsePage(r)
	v := rest.Validator{}
	for _, e := range errs {
		v.Add(e.Field, e.Code, e.Message)
	}
	f := Filter{Limit: page.Limit + 1}
	if page.Cursor != "" {
		after, err := httpx.ParseUUIDCursor(page.Cursor)
		if err != nil {
			e := httpx.InvalidCursorError()
			v.Add(e.Field, e.Code, e.Message)
		}
		f.After = after
	}
	q := r.URL.Query()
	if raw := q.Get("role"); raw != "" {
		role := identity.Role(raw)
		if role.Valid() {
			f.Role = &role
		} else {
			v.Add("role", httpx.FieldInvalidValue, "must be ADMIN, WAREHOUSE_MANAGER, DRIVER, or INSPECTOR")
		}
	}
	switch raw := q.Get("is_active"); raw {
	case "":
	case "true", "false":
		active := raw == "true"
		f.IsActive = &active
	default:
		v.Add("is_active", httpx.FieldInvalidValue, "must be true or false")
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	users, err := h.users.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, httpx.NewCollection(users, page.Limit, func(u User) string {
		return httpx.UUIDCursor(u.ID)
	}))
}

type createRequest struct {
	Email    string  `json:"email"`
	Password string  `json:"password"`
	FullName string  `json:"full_name"`
	Phone    *string `json:"phone"`
	Role     string  `json:"role"`
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
	v.Email("email", &req.Email)
	v.Password("password", req.Password)
	v.Text("full_name", &req.FullName, 1, 255)
	v.Phone("phone", &req.Phone)
	role := identity.Role(req.Role)
	if req.Role == "" {
		v.Add("role", httpx.FieldRequired, "is required")
	} else if !role.Valid() {
		v.Add("role", httpx.FieldInvalidValue, "must be ADMIN, WAREHOUSE_MANAGER, DRIVER, or INSPECTOR")
	}
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	created, err := h.users.Create(r.Context(), p, NewUser{
		Email: req.Email, Password: req.Password, FullName: req.FullName, Phone: req.Phone, Role: role,
	})
	if errors.Is(err, ErrEmailTaken) {
		rest.Conflict(w, r, "email", "is already registered")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "user created", slog.String("created_user_id", created.ID.String()),
		slog.String("role", string(created.Role)))
	w.Header().Set("Location", "/api/v1/users/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}
	u, err := h.users.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, u)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var patch rest.Patch
	if problem := httpx.DecodeJSON(w, r, &patch); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	var v rest.Validator
	var changes Patch
	if name := rest.PatchField[string](&v, patch, "full_name"); name.Set {
		if name.Null {
			v.Add("full_name", httpx.FieldRequired, "cannot be null")
		} else if v.Text("full_name", &name.Value, 1, 255) {
			changes.FullName = &name.Value
		}
	}
	if phone := rest.PatchField[string](&v, patch, "phone"); phone.Set {
		value := &phone.Value
		if phone.Null {
			value = nil
		}
		if v.Phone("phone", &value) {
			changes.Phone, changes.ClearPhone = value, value == nil
		}
	}
	if role := rest.PatchField[identity.Role](&v, patch, "role"); role.Set {
		if role.Null || !role.Value.Valid() {
			v.Add("role", httpx.FieldInvalidValue, "must be ADMIN, WAREHOUSE_MANAGER, DRIVER, or INSPECTOR")
		} else {
			changes.Role = &role.Value
		}
	}
	if active := rest.PatchField[bool](&v, patch, "is_active"); active.Set {
		if active.Null {
			v.Add("is_active", httpx.FieldRequired, "cannot be null")
		} else {
			changes.IsActive = &active.Value
		}
	}
	v.OnlyFields(patch, "full_name", "phone", "role", "is_active")
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	updated, err := h.users.Update(r.Context(), p, id, changes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "user updated", slog.String("updated_user_id", updated.ID.String()))
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

// userID reads the user_id path parameter. An ID that is not a UUID cannot name a visible user, so it answers
// 404 like any other invisible user.
func userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		httpx.NotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, ErrSelfChange), errors.Is(err, ErrActorNotAdmin):
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusForbidden, httpx.CodeForbidden, err.Error()))
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}
