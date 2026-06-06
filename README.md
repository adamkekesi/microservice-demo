# Logistics — Microservices Demo

A minimal but non-trivial parcel-shipping backend split into **three independent
Go services** with their own databases, a real synchronous dependency chain, and
a distributed-transaction-lite (saga) flow. The project demonstrates
microservice separation, hosting, and **first-class observability** (traces,
metrics, logs, profiling) wired through Datadog's `dd-trace-go` v2.

```
                ┌─────────────┐
                │    Auth     │  signs RS256 JWTs (private key) + publishes JWKS
                └─────────────┘
                       │  (cached public-key fetch — NOT per request)
        ┌──────────────┴───────────────┐
        ▼                              ▼
┌─────────────┐   reserve / commit /  ┌─────────────┐
│  Shipment   │ ────── release ─────► │  Inventory  │
│ (orchestr.) │   (sync HTTP, the     │   (stock)   │
└─────────────┘   only runtime call)  └─────────────┘
```

- **Auth** depends on nothing; it is the only holder of the signing key.
- **Inventory** verifies JWTs locally via Auth's cached JWKS; owns stock and the
  reserve/commit/release math.
- **Shipment** is the saga orchestrator; its only per-request cross-service call
  is to Inventory, forwarding the caller's `Authorization` header verbatim.

## Tech stack

| Concern | Choice |
|---|---|
| Language | Go 1.25 (provisioned by mise; a transitive dep requires 1.25) |
| HTTP | Gin, instrumented by `dd-trace-go` |
| ORM / DB | GORM v2 + pgx (stdlib) → PostgreSQL 16 |
| Migrations | `golang-migrate` (plain SQL, run at startup) |
| AuthN | RS256 JWT + JWKS (`golang-jwt/jwt/v5`) |
| Tracing/APM | `dd-trace-go` v2 (`github.com/DataDog/dd-trace-go/v2`) |
| Metrics | DogStatsD (`datadog-go/v5`) |
| Logging | zap (JSON), trace-correlated |
| Tests | stdlib `testing` + Testify + Testcontainers |
| Layout | multi-module monorepo (`go.work`) |

## Repository layout

```
platform/    shared module: observability, authn, database, httpserver, apperror, config
auth/        Auth service module      (cmd, internal/{http,handler,service,repository,model}, migrations, test)
inventory/   Inventory service module (+ internal/stock: pure availability math)
shipment/    Shipment service module  (+ internal/client: Inventory gateway)
deploy/      Dockerfiles + Kubernetes env-wiring notes
scripts/     Postgres init + Inventory seed
```

Each service is its own Go module that requires the **published** `platform`
module (`github.com/adamkekesi/microservice-demo/platform`, git-tagged
`platform/vX.Y.Z`) — there are **no `replace` directives**. The repo is public,
so `go mod tidy`, CI, and Docker image builds resolve `platform` straight from
the Go module proxy with **no credentials**. For day-to-day work, `go.work`
additionally overlays the local `platform/` source, so builds/tests compile your
working copy (including un-tagged edits) offline. Service-private code lives
under `<service>/internal/`; shared code is **exported** under `platform/` (not
`internal/`) because Go's `internal` rule is per-module.

**Changing `platform`:** local builds pick up edits instantly via the go.work
overlay. To publish them for `go mod tidy` / CI / reproducible image builds,
re-tag and bump consumers:

```bash
git tag platform/v0.1.2 && git push origin platform/v0.1.2
# in each service module:
go get github.com/adamkekesi/microservice-demo/platform@v0.1.2   # or: mise run tidy
```

## Prerequisites

- [mise](https://mise.jdx.dev) (provisions Go 1.25 + golangci-lint v2), Docker +
  Docker Compose.
- For Datadog telemetry: a `DD_API_KEY` (optional — the apps run fine without
  it; traces/metrics simply won't ship).

```bash
mise install        # Go 1.25 + golangci-lint v2 on PATH
mise tasks          # list wrapped tasks: tidy build test test-integration lint up down seed smoke
```

## Quick start (docker-compose)

```bash
cp .env.example .env     # optionally set DD_API_KEY; set POSTGRES_HOST_PORT if 5432 is taken
mise run up              # docker compose up --build (no credentials needed — platform is public)
```

Services listen on `:8001` (auth), `:8002` (inventory), `:8003` (shipment). Each
exposes `GET /health` (liveness) and `GET /ready` (readiness: DB + JWKS).

```bash
mise run seed     # load seed warehouses/items/stock into the compose Postgres
mise run smoke    # happy-path smoke test against the running stack
```

## Configuration (env vars)

Common: `PORT`, `DATABASE_URL`, `LOG_LEVEL`, plus the Datadog vars
(`DD_ENV`, `DD_SERVICE`, `DD_VERSION`, `DD_AGENT_HOST`, `DD_TRACE_AGENT_PORT`,
`DD_DOGSTATSD_PORT`, `DD_LOGS_INJECTION`, `DD_PROFILING_ENABLED`).

| Service | Extra vars |
|---|---|
| Auth | `JWT_PRIVATE_KEY` / `JWT_PRIVATE_KEY_PATH` (else a key is generated and persisted), `JWT_KEY_ID` (def `auth-key-1`), `JWT_TTL_SECONDS` (def 900), `JWT_ISSUER` (def `auth-service`), `ADMIN_EMAIL`, `ADMIN_PASSWORD` |
| Inventory | `AUTH_JWKS_URL`, `JWKS_CACHE_TTL_SECONDS` (def 600), `JWT_ISSUER`, `RESERVATION_TTL_SECONDS` (def 900) |
| Shipment | `AUTH_JWKS_URL`, `JWKS_CACHE_TTL_SECONDS`, `JWT_ISSUER`, `INVENTORY_BASE_URL`, `INVENTORY_TIMEOUT_MS` (def 5000) |

## Happy-path walkthrough (curl)

```bash
AUTH=http://localhost:8001
INV=http://localhost:8002
SHIP=http://localhost:8003

# 1. Register + login a customer.
curl -s -XPOST $AUTH/auth/register -d '{"email":"c@example.com","password":"supersecret"}'
CUST=$(curl -s -XPOST $AUTH/auth/login -d '{"email":"c@example.com","password":"supersecret"}' | jq -r .access_token)

# 2. Log in as the seed admin (set via ADMIN_EMAIL/ADMIN_PASSWORD).
ADMIN=$(curl -s -XPOST $AUTH/auth/login -d '{"email":"admin@example.com","password":"changeme123"}' | jq -r .access_token)

# 3. Admin creates a warehouse, an item, and stock.
WH=$(curl -s -XPOST $INV/warehouses -H "Authorization: Bearer $ADMIN" -d '{"code":"WH1","name":"Central"}' | jq -r .id)
IT=$(curl -s -XPOST $INV/items      -H "Authorization: Bearer $ADMIN" -d '{"sku":"SKU1","name":"Box"}'     | jq -r .id)
curl -s -XPUT $INV/stock -H "Authorization: Bearer $ADMIN" \
  -d "{\"warehouse_id\":\"$WH\",\"item_id\":\"$IT\",\"quantity_on_hand\":100}"

# 4. Customer creates a shipment → reserves stock → PENDING.
SH=$(curl -s -XPOST $SHIP/shipments -H "Authorization: Bearer $CUST" \
  -d "{\"item_id\":\"$IT\",\"warehouse_id\":\"$WH\",\"quantity\":30,\"destination_address\":\"1 Main St\"}" | jq -r .id)

# available is now 70:
curl -s "$INV/stock?warehouse_id=$WH&item_id=$IT" -H "Authorization: Bearer $CUST"

# 5. Confirm → commits the reservation → on_hand drops to 70.
curl -s -XPOST $SHIP/shipments/$SH/confirm -H "Authorization: Bearer $CUST"
curl -s "$INV/stock?warehouse_id=$WH&item_id=$IT" -H "Authorization: Bearer $CUST"
```

All non-2xx responses use the standard envelope:
`{ "error": { "code": "...", "message": "...", "details": { } } }`.

## Observability

This is the headline of the project. With `dd-trace-go` v2 wired through the
shared `platform` module:

- **Traces** — Gin inbound spans (`gintrace.Middleware`), GORM/SQL spans per
  query (every repository call uses `db.WithContext(ctx)` so DB spans attach to
  the request), and the **cross-service hop** Shipment → Inventory propagates
  trace context via a `dd-trace`-wrapped HTTP client. Exercising the happy path
  produces a single distributed trace: `Shipment HTTP → outbound call →
  Inventory HTTP → GORM/SQL on both sides`. View it in Datadog APM (filter by
  `service:shipment-service`).
- **Metrics** (DogStatsD, namespaced `logistics.*`): `shipment.created`,
  `shipment.confirmed`, `shipment.cancelled`, `reservation.insufficient_stock`,
  `inventory.commit.latency`.
- **Logs** — zap JSON with `dd.trace_id` / `dd.span_id` injected so logs
  correlate to traces (`DD_LOGS_INJECTION=true`).
- **Profiling** — set `DD_PROFILING_ENABLED=true` to start the continuous
  profiler.

### AI-based observability — next step

The deliverable here is **extremely precise, well-structured telemetry** as the
data foundation. The AI layer sits on top and is a documented next step:

- **Platform-native AIOps:** point the APM/metrics at Datadog Watchdog for
  automatic anomaly detection and root-cause surfacing on the `logistics.*`
  signals (e.g. a spike in `reservation.insufficient_stock` or
  `inventory.commit.latency`).
- **LLM-assisted analysis:** because traces are distributed and logs are
  trace-correlated, an agent can pull a trace by id (or a metric window) and
  summarize an incident / propose a cause. No code changes are required to the
  services — only a read-side integration against the telemetry backend.

The instrumentation is deliberately vendor-thin (a single `platform/observability`
package), so swapping the backend or adding an OpenTelemetry exporter later is a
localized change.

## Testing

Two tiers (see each module's `internal/**/*_test.go` and `test/integration/`):

```bash
make test               # unit tests, fast, no Docker (stock math, saga, JWKS, validation)
make test-integration   # full-stack tests via Testcontainers (needs Docker)
make test-all
```

Integration tests default to **Testcontainers** (a throwaway `postgres:16`). In
an environment without a Docker daemon, point each service's suite at a
pre-provisioned database instead:

```bash
cd inventory && TEST_DATABASE_URL=postgres://user:pass@localhost:5432/inventory_db?sslmode=disable \
  go test -tags=integration ./test/integration/...
```

Coverage highlights: the pure **stock math** (acceptance 5–8), the **row-locked
concurrent-reserve race** (exactly one success), reservation **expiry → commit
409**, idempotent reserve, the full **shipment saga** incl. compensation and the
state machine, JWKS verification (valid/forged/rotation/expiry), and
authn/authz.

> Note: `go test ./...` from the repo root does not span modules in this
> workspace layout, so the Makefile loops over modules — run commands per module
> (or via `make`).

## Kubernetes

Local runtime is docker-compose; the single app-side change for Kubernetes
(node-local Datadog agent via `status.hostIP`) and the topology notes are in
[`deploy/k8s/README.md`](deploy/k8s/README.md).

## Out of scope

Payments, multi-item shipments, restocking committed stock, async messaging,
refresh tokens / revocation, active key rotation, rate limiting / gateway.
