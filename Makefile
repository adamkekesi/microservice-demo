# Logistics monorepo. Commands run per-module because go.work + a non-module
# repo root means `go test ./...` from the root cannot span modules. Build/test
# use the go.work overlay for the local `platform` (offline, no credentials);
# `tidy` fetches the PUBLISHED platform module (needs GOPRIVATE + git creds).

MODULES  := platform auth inventory shipment
SERVICES := auth inventory shipment

# Used by seed (compose Postgres container). Override as needed.
DB_USER ?= logistics

GO := go

.PHONY: help tidy fmt build test test-integration test-all lint \
        run-auth run-inventory run-shipment seed compose-up compose-down docker-build

help:
	@echo "Targets:"
	@echo "  tidy             go mod tidy in each module (fetches published platform)"
	@echo "  fmt              gofmt -w all modules"
	@echo "  build            build service binaries into ./bin"
	@echo "  test             unit tests (fast, no Docker)"
	@echo "  test-integration integration tests (Testcontainers/Docker, or TEST_DATABASE_URL)"
	@echo "  test-all         unit + integration"
	@echo "  lint             golangci-lint per module"
	@echo "  run-<svc>        run a service locally"
	@echo "  seed             load Inventory seed data into the compose Postgres"
	@echo "  compose-up       docker compose up --build"
	@echo "  compose-down     docker compose down -v"
	@echo "  docker-build     build all three images"

# tidy runs in module mode (GOWORK=off) so each go.mod is tidied against the
# published platform; needs GOPRIVATE (set in mise.toml) + git credentials.
tidy:
	@for m in $(MODULES); do echo "== tidy $$m =="; (cd $$m && GOWORK=off go mod tidy) || exit 1; done

fmt:
	gofmt -w $(MODULES)

build:
	@mkdir -p bin
	@for s in $(SERVICES); do echo "== build $$s =="; (cd $$s && $(GO) build -o ../bin/$$s ./cmd/$$s) || exit 1; done

test:
	@for m in $(MODULES); do echo "== test $$m =="; (cd $$m && $(GO) test ./...) || exit 1; done

# Needs a Docker daemon (Testcontainers). For a pre-provisioned Postgres set
# TEST_DATABASE_URL per service to its own database, e.g.:
#   cd inventory && TEST_DATABASE_URL=postgres://u:p@localhost:5432/inventory_db?sslmode=disable \
#     go test -tags=integration ./test/integration/...
test-integration:
	@for s in $(SERVICES); do echo "== integration $$s =="; (cd $$s && $(GO) test -tags=integration ./test/integration/...) || exit 1; done

test-all: test test-integration

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; skipping"; exit 0; }
	@for m in $(MODULES); do echo "== lint $$m =="; (cd $$m && golangci-lint run ./...) || exit 1; done

run-auth: export PORT=8001
run-auth: export DATABASE_URL=postgres://logistics:logistics@localhost:5432/auth_db?sslmode=disable
run-auth: export MIGRATIONS_DIR=migrations
run-auth: export ADMIN_EMAIL=admin@example.com
run-auth: export ADMIN_PASSWORD=changeme123
run-auth:
	cd auth && $(GO) run ./cmd/auth

run-inventory: export PORT=8002
run-inventory: export DATABASE_URL=postgres://logistics:logistics@localhost:5432/inventory_db?sslmode=disable
run-inventory: export MIGRATIONS_DIR=migrations
run-inventory: export AUTH_JWKS_URL=http://localhost:8001/.well-known/jwks.json
run-inventory:
	cd inventory && $(GO) run ./cmd/inventory

run-shipment: export PORT=8003
run-shipment: export DATABASE_URL=postgres://logistics:logistics@localhost:5432/shipment_db?sslmode=disable
run-shipment: export MIGRATIONS_DIR=migrations
run-shipment: export AUTH_JWKS_URL=http://localhost:8001/.well-known/jwks.json
run-shipment: export INVENTORY_BASE_URL=http://localhost:8002
run-shipment:
	cd shipment && $(GO) run ./cmd/shipment

# Seeds via the compose Postgres container (no local psql needed). Stack must be up.
seed:
	docker compose exec -T postgres psql -U $(DB_USER) -d inventory_db < scripts/seed_inventory.sql

# GH_TOKEN lets the build fetch the private published platform module (passed as
# a BuildKit secret; never baked into a layer).
compose-up:
	GH_TOKEN="$$(gh auth token)" docker compose up --build

compose-down:
	docker compose down -v

docker-build:
	@for s in $(SERVICES); do \
	  echo "== image $$s =="; \
	  GH_TOKEN="$$(gh auth token)" docker build --secret id=gh_token,env=GH_TOKEN \
	    -f deploy/docker/$$s.Dockerfile -t logistics-$$s . || exit 1; \
	done
