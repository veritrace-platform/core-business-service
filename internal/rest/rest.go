// Package rest holds the HTTP conventions shared by the core domain handlers: problem codes, request
// validation, and the mapping of unexpected errors.
package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
)

// Problem codes of the core service (rest-api.md §1.1). The platform codes are in httpx.
const (
	CodeTokenExpired                = "TOKEN_EXPIRED"
	CodeInvalidCredentials          = "INVALID_CREDENTIALS" //nolint:gosec // an error code, not a credential
	CodeRefreshTokenInvalid         = "REFRESH_TOKEN_INVALID"
	CodeIdentifierAlreadyRegistered = "IDENTIFIER_ALREADY_REGISTERED"
	CodeInvalidGS1Identifier        = "INVALID_GS1_IDENTIFIER"
)

// Field error codes of the core service. The shared codes are in httpx; GS1 keys use the gs1 reasons.
const (
	FieldAlreadyRegistered = "ALREADY_REGISTERED"
	FieldIncorrect         = "INCORRECT"
)

// InternalError answers 500 for an unexpected error and logs it. The response carries only the trace ID,
// which leads to the log record.
func InternalError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		// The client went away; nobody reads this response.
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusServiceUnavailable, httpx.CodeServiceUnavailable,
			"request canceled"))
		return
	}
	logger.ErrorContext(r.Context(), "request failed", slog.String("path", r.URL.Path), slog.Any("error", err))
	httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusInternalServerError, httpx.CodeInternalError,
		"internal error"))
}

// Conflict answers 409 IDENTIFIER_ALREADY_REGISTERED for a unique key that another record holds.
func Conflict(w http.ResponseWriter, r *http.Request, field, message string) {
	p := httpx.NewProblem(http.StatusConflict, CodeIdentifierAlreadyRegistered, field+" "+message)
	p.Errors = []httpx.FieldError{{Field: field, Code: FieldAlreadyRegistered, Message: message}}
	httpx.WriteProblem(w, r, p)
}

// Principal returns the authenticated caller of r. Handlers behind the authentication middleware always have
// one; without it, the request fails closed with 401 UNAUTHENTICATED and ok is false.
func Principal(w http.ResponseWriter, r *http.Request) (p identity.Principal, ok bool) {
	if p, ok = identity.FromContext(r.Context()); !ok {
		httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusUnauthorized, httpx.CodeUnauthenticated,
			"a bearer access token is required"))
	}
	return p, ok
}
