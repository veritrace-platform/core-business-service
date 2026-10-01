package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/platform/logging"
	"github.com/veritrace-platform/core-business-service/internal/rest"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// maxUserAgentLength bounds the stored User-Agent of a session.
const maxUserAgentLength = 512

// Sessions signs users in and out.
type Sessions interface {
	Login(ctx context.Context, email, password string, userAgent *string) (Session, error)
	Refresh(ctx context.Context, refreshToken string, userAgent *string) (Session, error)
	Logout(ctx context.Context, refreshToken string) error
	Me(ctx context.Context, p identity.Principal) (user.Me, error)
	ChangePassword(ctx context.Context, p identity.Principal, current, replacement string) error
}

// Handler serves the authentication endpoints and the signed-in user's account.
type Handler struct {
	sessions Sessions
	keys     *KeySet
	// throttle limits logins per client address (rest-api.md §1).
	throttle func(http.Handler) http.Handler
	// authenticate requires a valid access token.
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler.
func NewHandler(sessions Sessions, keys *KeySet, throttle, authenticate func(http.Handler) http.Handler, logger *slog.Logger) *Handler {
	return &Handler{sessions: sessions, keys: keys, throttle: throttle, authenticate: authenticate, logger: logger}
}

// Routes registers the endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.With(h.throttle).Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)
	r.Post("/auth/logout", h.logout)
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/me", h.me)
		r.Post("/me/password", h.changePassword)
	})
}

// WellKnownRoutes registers the JWKS document under /.well-known.
func (h *Handler) WellKnownRoutes(r chi.Router) {
	r.Get("/jwks.json", h.jwks)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// sessionResponse is the body of a successful login or refresh.
type sessionResponse struct {
	AccessToken           string  `json:"access_token"`
	TokenType             string  `json:"token_type"`
	ExpiresIn             int     `json:"expires_in"`
	RefreshToken          string  `json:"refresh_token"`
	RefreshTokenExpiresIn int     `json:"refresh_token_expires_in"`
	User                  user.Me `json:"user"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if p := httpx.DecodeJSON(w, r, &req); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}
	var v rest.Validator
	v.Text("email", &req.Email, 1, rest.MaxEmailLength)
	if req.Password == "" {
		v.Add("password", httpx.FieldRequired, "is required")
	}
	if p := v.Problem(); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}

	session, err := h.sessions.Login(r.Context(), req.Email, req.Password, userAgent(r))
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		h.logger.InfoContext(r.Context(), "login rejected")
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, rest.CodeInvalidCredentials,
			"email or password is incorrect"))
		return
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
		return
	}
	h.logger.InfoContext(r.Context(), "login succeeded",
		slog.String("tenant_id", session.User.Tenant.ID.String()), slog.String("user_id", session.User.ID.String()))
	writeSession(w, r, session)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if p := httpx.DecodeJSON(w, r, &req); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}
	if req.RefreshToken == "" {
		httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{
			{Field: "refresh_token", Code: httpx.FieldRequired, Message: "is required"},
		}))
		return
	}

	session, err := h.sessions.Refresh(r.Context(), req.RefreshToken, userAgent(r))
	switch {
	case errors.Is(err, ErrRefreshTokenReused):
		h.logger.WarnContext(r.Context(), "refresh token reused; session family revoked")
		refreshRejected(w, r)
		return
	case errors.Is(err, ErrRefreshTokenInvalid):
		refreshRejected(w, r)
		return
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
		return
	}
	writeSession(w, r, session)
}

func refreshRejected(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, rest.CodeRefreshTokenInvalid,
		"refresh token is unknown, expired, revoked, or reused; sign in again"))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if p := httpx.DecodeJSON(w, r, &req); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}
	if req.RefreshToken == "" {
		httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{
			{Field: "refresh_token", Code: httpx.FieldRequired, Message: "is required"},
		}))
		return
	}
	if err := h.sessions.Logout(r.Context(), req.RefreshToken); err != nil {
		rest.InternalError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) jwks(w http.ResponseWriter, r *http.Request) {
	// Verifiers may cache the keys briefly; a rotation publishes the new key before it signs.
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.WriteJSON(w, r, http.StatusOK, h.keys.JWKS())
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	me, err := h.sessions.Me(r.Context(), p)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
		return
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, me)
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var req passwordChangeRequest
	if p := httpx.DecodeJSON(w, r, &req); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}
	var v rest.Validator
	if req.CurrentPassword == "" {
		v.Add("current_password", httpx.FieldRequired, "is required")
	}
	v.Password("new_password", req.NewPassword)
	if p := v.Problem(); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}

	err := h.sessions.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, ErrIncorrectPassword):
		httpx.WriteProblem(w, r, httpx.ValidationProblem([]httpx.FieldError{
			{Field: "current_password", Code: rest.FieldIncorrect, Message: "is incorrect"},
		}))
		return
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
		return
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
		return
	}
	h.logger.InfoContext(r.Context(), "password changed; other sessions revoked")
	w.WriteHeader(http.StatusNoContent)
}

func writeSession(w http.ResponseWriter, r *http.Request, s Session) {
	// Token responses must never be cached (RFC 6749 §5.1).
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, r, http.StatusOK, sessionResponse{
		AccessToken:           s.AccessToken,
		TokenType:             "Bearer",
		ExpiresIn:             int(s.AccessTokenTTL.Seconds()),
		RefreshToken:          s.RefreshToken,
		RefreshTokenExpiresIn: int(s.RefreshTokenTTL.Seconds()),
		User:                  s.User,
	})
}

// userAgent returns the request's User-Agent, cut to the stored length, or nil when there is none.
func userAgent(r *http.Request) *string {
	ua := strings.TrimSpace(r.UserAgent())
	if ua == "" {
		return nil
	}
	if utf8.RuneCountInString(ua) > maxUserAgentLength {
		ua = string([]rune(ua)[:maxUserAgentLength])
	}
	return &ua
}

// Authenticator requires a valid access token on the requests it wraps.
type Authenticator struct {
	tokens *Tokens
}

// NewAuthenticator returns an Authenticator that verifies tokens with tokens.
func NewAuthenticator(tokens *Tokens) *Authenticator {
	return &Authenticator{tokens: tokens}
}

// Middleware answers 401 without a valid bearer token: TOKEN_EXPIRED for an expired one, which the client
// refreshes, and UNAUTHENTICATED otherwise. It stores the principal in the request context and adds the tenant
// and user to every log record of the request.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="veritrace"`)
			httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthenticated,
				"a bearer access token is required"))
			return
		}
		p, err := a.tokens.Verify(token)
		switch {
		case errors.Is(err, ErrTokenExpired):
			w.Header().Set("WWW-Authenticate", `Bearer realm="veritrace", error="invalid_token", error_description="expired"`)
			httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, rest.CodeTokenExpired,
				"the access token has expired; refresh it and retry"))
			return
		case err != nil:
			w.Header().Set("WWW-Authenticate", `Bearer realm="veritrace", error="invalid_token"`)
			httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthenticated,
				"the access token is invalid"))
			return
		}
		ctx := identity.NewContext(r.Context(), p)
		ctx = logging.WithAttrs(ctx, slog.String("tenant_id", p.TenantID.String()), slog.String("user_id", p.UserID.String()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
