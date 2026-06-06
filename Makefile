# Logistics monorepo. Commands run per-module because go.work + a non-module
# repo root means `go test ./...` from the root cannot span modules; the
# explicit platform replace directive makes each module build standalone.

MODULES  := platform auth inventory shipment
SERVICES := auth inventory shipment

# Local (non-compose) Postgres connection used by run-*/seed. Override as needed.
DB_USER ?= logistics
DB_PASS ?= logistics
DB_HOST ?= localhost

GO := GOWORK=off go

.PHONY: help tidy fmt build test test-integration test-all lint \
        run-auth run-inventory run-shipment seed compose-up compose-down docker-build

help:
	@echo "Targets:"
	@echo "  tidy             go mod tidy in each module"
	@echo "  fmt              gofmt -w all modules"
	@echo "  build            build service binaries into ./bin"
	@echo "  test             unit tests (fast, no Docker)"
	@echo "  test-integration integration tests (Testcontainers/Docker, or TEST_DATABASE_URL)"
	@echo "  test-all         unit + integration"
	@echo "  lint             golangci-lint per module (if installed)"
	@echo "  run-<svc>        run a service locally against \$$DB_HOST"
	@echo "  seed             load the Inventory seed data"
	@echo "  compose-up       docker compose up --build"
	@echo "  compose-down     docker compose down -v"
	@echo "  docker-build     build all three images"

tidy:
	@for m in $(MODULES); do echo "== tidy $$m =="; (cd $$m && $(GO) mod tidy) || exit 1; done

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
	@for m in $(MODULES); do echo "== lint $$m =="; (cd $$m && GOWORK=off golangci-lint run ./...) || exit 1; done

run-auth: export PORT=8001
run-auth: export DATABASE_URL=postgres://$(DB_USER):$(DB_PASS)@$(DB_HOST):5432/auth_db?sslmode=disable
run-auth: export MIGRATIONS_DIR=migrations
run-auth: export ADMIN_EMAIL=admin@example.com
run-auth: export ADMIN_PASSWORD=changeme123
run-auth:
	cd auth && $(GO) run ./cmd/auth

run-inventory: export PORT=8002
run-inventory: export DATABASE_URL=postgres://$(DB_USER):$(DB_PASS)@$(DB_HOST):5432/inventory_db?sslmode=disable
run-inventory: export MIGRATIONS_DIR=migrations
run-inventory: export AUTH_JWKS_URL=http://localhost:8001/.well-known/jwks.json
run-inventory:
	cd inventory && $(GO) run ./cmd/inventory

run-shipment: export PORT=8003
run-shipment: export DATABASE_URL=postgres://$(DB_USER):$(DB_PASS)@$(DB_HOST):5432/shipment_db?sslmode=disable
run-shipment: export MIGRATIONS_DIR=migrations
run-shipment: export AUTH_JWKS_URL=http://localhost:8001/.well-known/jwks.json
run-shipment: export INVENTORY_BASE_URL=http://localhost:8002
run-shipment:
	cd shipment && $(GO) run ./cmd/shipment

seed:
	psql "postgres://$(DB_USER):$(DB_PASS)@$(DB_HOST):5432/inventory_db?sslmode=disable" -f scripts/seed_inventory.sql

compose-up:
	docker compose up --build

compose-down:
	docker compose down -v

docker-build:
	docker build -f deploy/docker/auth.Dockerfile -t logistics-auth .
	docker build -f deploy/docker/inventory.Dockerfile -t logistics-inventory .
	docker build -f deploy/docker/shipment.Dockerfile -t logistics-shipment .
