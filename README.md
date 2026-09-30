# core-business-service

The VeriTrace core REST API. It covers:

- tenants, identity, and access control;
- the GS1 catalog (GLN, GTIN);
- lots and inventory;
- shipments (SSCC), custody handover, and emergency recall;
- the tamper-evident shipment event log, published to Kafka.

Platform documentation, including the architecture, domain rules, contracts, and ADRs, lives in
[`veritrace/docs`](https://github.com/veritrace-platform/veritrace/tree/main/docs).

## Requirements

- Go 1.27 (`GOTOOLCHAIN=auto` downloads it automatically)
- Docker, for integration tests and the local environment
- The local environment from `platform-infrastructure` (`make up`)

## Getting started

```bash
cp .env.example .env
make migrate-up     # create or upgrade the schema (owner role)
make run            # API on :8080, admin on :8081
```

Requests normally go through the gateway on `http://localhost:8000`.

## Commands

The binary exposes these subcommands:

| Command | Purpose |
| --- | --- |
| `serve` | Run the REST API and the admin server |
| `migrate up\|down\|status` | Manage the database schema |
| `healthcheck` | Probe the admin server (used by container health checks) |
| `version` | Print the build version |

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` or `production` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `HTTP_ADDR` | `:8080` | Public API listener |
| `ADMIN_ADDR` | `:8081` | `/healthz`, `/readyz`, `/metrics` listener (never exposed publicly) |
| `DATABASE_URL` | — | Runtime role connection (`veritrace_core_app`) |
| `MIGRATIONS_DATABASE_URL` | — | Owner role connection (`veritrace_core_owner`), used by `migrate` only |
| `SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown budget |
| `JWT_SIGNING_KEYS` | — | Access token signing keys: comma-separated `<kid>:<base64 of 32 random bytes>` (`openssl rand -base64 32`). The first key signs; all are published in the JWKS, so add the new key first and drop the old one after 15 minutes. |
| `REFRESH_TOKEN_TTL` | `168h` | Refresh token lifetime; every refresh starts a new period (minimum `1h`) |
| `TRUSTED_PROXIES` | loopback and private ranges | CIDR ranges whose `X-Forwarded-For` names the client address for rate limits |

## Project layout

```
cmd/core-business-service/   entry point
internal/app/                wiring of stores, services, and handlers
internal/httpapi/            REST router and route mounting
internal/rest/               problem codes, request validation, and error mapping for the handlers
internal/tenancy/            tenant transaction helper; tenancytest: isolation test harness
internal/<domain>/           domain packages (tenant, gs1, password, ...); SQL in <domain>/queries
internal/platform/           config, logging, trace context, HTTP plumbing, admin, database, migrations
migrations/                  goose SQL migrations (embedded)
api/openapi.yaml             REST contract
sqlc.yaml                    query code generation
```

## Tenant isolation

PostgreSQL row-level security limits every query to the rows the caller's tenant may see
([ADR-0002](https://github.com/veritrace-platform/veritrace/blob/main/docs/adr/0002-multi-party-tenancy-with-row-level-security.md)).

- Tenant-scoped work runs inside `tenancy.DB.WithTenantTx`, which sets `app.current_tenant_id` for one
  transaction. Repositories receive that transaction and never begin their own.
- Registration, login, and token refresh run without a tenant context. They reach tenant data only through
  `SECURITY DEFINER` functions.
- Integration tests get a migrated database from `tenancytest.Start` and check isolation as the runtime
  role with `AssertVisible`, `AssertHidden`, and `AssertDenied`.
- `migrations/conventions_integration_test.go` checks every migration against the
  [schema conventions](https://github.com/veritrace-platform/veritrace/blob/main/docs/architecture/data-model.md#36-row-level-security-policies).

## Development

```bash
make test               # unit tests
make test-integration   # unit + integration tests (Docker)
make generate           # regenerate query code after editing SQL (sqlc, in Docker)
make lint               # golangci-lint
make openapi-lint       # validate api/openapi.yaml
make help               # all targets
```

## License

[MIT](LICENSE)
