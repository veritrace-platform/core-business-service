// Package app assembles the core service: it wires stores, services, and handlers into the public API
// handler. The serve command and the end-to-end tests build the service the same way.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/veritrace-platform/core-business-service/internal/auth"
	"github.com/veritrace-platform/core-business-service/internal/directory"
	"github.com/veritrace-platform/core-business-service/internal/httpapi"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/lot"
	"github.com/veritrace-platform/core-business-service/internal/outbox"
	"github.com/veritrace-platform/core-business-service/internal/password"
	"github.com/veritrace-platform/core-business-service/internal/product"
	"github.com/veritrace-platform/core-business-service/internal/ratelimit"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
	"github.com/veritrace-platform/core-business-service/internal/tenant"
	"github.com/veritrace-platform/core-business-service/internal/user"
)

// passwordHashConcurrency bounds concurrent Argon2id computations. Each holds 19 MiB, so four stay well
// inside the container's memory limit.
const passwordHashConcurrency = 4

// Public endpoints that anyone can call allow this many requests per minute per client address
// (rest-api.md §1).
const publicRequestsPerMinute = 10

// Config holds the settings of the core service beyond the shared platform configuration.
type Config struct {
	Auth auth.Config
	// KafkaBrokers are the bootstrap brokers that the outbox relay publishes to.
	KafkaBrokers []string `env:"KAFKA_BROKERS" envSeparator:","`
}

// LoadConfig reads the core settings from the environment.
func LoadConfig() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	return cfg, nil
}

// Validate reports the settings that are missing or invalid.
func (c Config) Validate() error {
	var errs []error
	if err := c.Auth.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(c.KafkaBrokers) == 0 {
		errs = append(errs, errors.New("KAFKA_BROKERS is required"))
	}
	return errors.Join(errs...)
}

// Dependencies are the resources the service runs on.
type Dependencies struct {
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Pool connects as the runtime role, to which row-level security applies.
	Pool *pgxpool.Pool
	// Now reads the clock; tests inject a fixed one.
	Now func() time.Time
}

// NewHandler returns the public API handler.
func NewHandler(cfg Config, deps Dependencies) (http.Handler, error) {
	if err := cfg.Auth.Validate(); err != nil {
		return nil, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	keys, err := auth.ParseKeySet(cfg.Auth.SigningKeys)
	if err != nil {
		return nil, err
	}
	clientIP, err := ratelimit.NewClientIP(cfg.Auth.TrustedProxies)
	if err != nil {
		return nil, err
	}
	hasher, err := password.NewHasher(passwordHashConcurrency)
	if err != nil {
		return nil, fmt.Errorf("create password hasher: %w", err)
	}
	db := tenancy.NewDB(deps.Pool)
	tokens := auth.NewTokens(keys, now)
	authenticate := auth.NewAuthenticator(tokens).Middleware
	throttle := func() func(http.Handler) http.Handler {
		return ratelimit.New(publicRequestsPerMinute, time.Minute, now).Middleware(clientIP.Of)
	}

	sessions := auth.NewHandler(
		auth.NewService(auth.NewPostgresStore(deps.Pool, db), hasher, tokens, cfg.Auth.RefreshTokenTTL, now),
		keys, throttle(), authenticate, deps.Logger,
	)
	tenantStore := tenant.NewStore(deps.Pool, db)
	tenants := tenant.NewService(tenantStore, tenantStore, hasher)
	tenantHandler := tenant.NewHandler(tenants, tenants, throttle(), authenticate, deps.Logger)
	users := user.NewHandler(user.NewService(user.NewPostgresStore(db), hasher, now), authenticate, deps.Logger)
	locations := location.NewHandler(location.NewService(location.NewPostgresStore(db)), authenticate, deps.Logger)
	products := product.NewHandler(product.NewService(product.NewPostgresStore(db)), authenticate, deps.Logger)
	lots := lot.NewHandler(lot.NewService(lot.NewPostgresStore(db)), authenticate, deps.Logger)
	stock := inventory.NewHandler(inventory.NewService(inventory.NewPostgresStore(db)), authenticate, deps.Logger)
	directoryHandler := directory.NewHandler(directory.NewService(directory.NewPostgresStore(deps.Pool)), authenticate,
		deps.Logger)

	return httpapi.NewRouter(deps.Logger, deps.Registerer, httpapi.Mounts{
		API: []httpapi.Routes{
			tenantHandler.Routes, sessions.Routes, users.Routes, locations.Routes, products.Routes, lots.Routes,
			stock.Routes, directoryHandler.Routes,
		},
		WellKnown: []httpapi.Routes{sessions.WellKnownRoutes},
	}), nil
}

// NewRelay returns the outbox relay that publishes shipment events to Kafka, and a function that closes its
// broker connections.
func NewRelay(cfg Config, deps Dependencies) (*outbox.Relay, func(), error) {
	publisher, err := outbox.NewKafkaPublisher(cfg.KafkaBrokers)
	if err != nil {
		return nil, nil, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return outbox.NewRelay(deps.Pool, publisher, deps.Logger, now), publisher.Close, nil
}
