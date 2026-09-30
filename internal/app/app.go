// Package app assembles the core service: it wires stores, services, and handlers into the public API
// handler. The serve command and the end-to-end tests build the service the same way.
package app

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/password"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
)

// passwordHashConcurrency bounds concurrent Argon2id computations. Each holds 19 MiB, so four stay well
// inside the container's memory limit.
const passwordHashConcurrency = 4

// Dependencies are the resources the service runs on.
type Dependencies struct {
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Pool connects as the runtime role, to which row-level security applies.
	Pool *pgxpool.Pool
}

// NewHandler returns the public API handler.
func NewHandler(deps Dependencies) (http.Handler, error) {
	hasher, err := password.NewHasher(passwordHashConcurrency)
	if err != nil {
		return nil, fmt.Errorf("create password hasher: %w", err)
	}

	tenants := tenant.NewHandler(
		tenant.NewService(tenant.NewStore(deps.Pool), hasher),
		passThrough,
		deps.Logger,
	)

	return httpapi.NewRouter(deps.Logger, deps.Registerer, httpapi.Mounts{
		API: []httpapi.Routes{tenants.Routes},
	}), nil
}

func passThrough(next http.Handler) http.Handler {
	return next
}
