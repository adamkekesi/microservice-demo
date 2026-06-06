# Logistics monorepo. Commands run per-module because go.work + a non-module
# repo root means `go test ./...` from the root cannot span modules. Build/test
# use the go.work overlay for the local `platform` (offline, no credentials);
# `tidy` fetches the PUBLISHED platform module (needs GOPRIVATE + git creds).

MODULES  := platform auth inventory shipment
SERVICES := auth inventory shipment

# Used by seed (compose Postgres container). Override as needed.
DB_USER ?= logistics

GO := go

# Local kind cluster (deploy/kind, deploy/platform). Host ports are 8080/8443 so
# this cluster coexists with any other local kind cluster bound to 80/443.
KIND_CLUSTER   ?= logistics-microservice
KIND_CONTEXT   := kind-$(KIND_CLUSTER)
KIND_CONFIG    := deploy/kind/kind-config.yaml
INGRESS_VALUES := deploy/platform/ingress-nginx-values.yaml
METRICS_VALUES := deploy/platform/metrics-server-values.yaml
K8S_OVERLAY    := deploy/k8s/overlays/local
KUBECTL        := kubectl --context $(KIND_CONTEXT)

.PHONY: help tidy fmt build test test-integration test-all lint \
        run-auth run-inventory run-shipment seed compose-up compose-down docker-build \
        cluster-up ingress-up metrics-up cluster-verify cluster-down \
        images deploy undeploy smoke-k8s k8s-up

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
	@echo "  cluster-up       create the local kind cluster (deploy/kind/kind-config.yaml)"
	@echo "  ingress-up       install ingress-nginx into the cluster (Helm)"
	@echo "  metrics-up       install metrics-server (enables HPA + kubectl top)"
	@echo "  cluster-verify   prove ingress routing works (deploys + deletes a smoke app)"
	@echo "  images           build the 3 service images and load them into kind"
	@echo "  deploy           apply the app manifests (kubectl apply -k overlays/local)"
	@echo "  smoke-k8s        happy-path smoke test through the gateway (localhost:8080)"
	@echo "  undeploy         delete the app manifests from the cluster"
	@echo "  k8s-up           full bring-up: cluster + ingress + metrics + images + deploy"
	@echo "  cluster-down     delete the local kind cluster"

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
	@for m in $(MODULES); do echo "== test $$m =="; (cd $$m && $(GO) test -count=1 ./...) || exit 1; done

# Needs a Docker daemon (Testcontainers). For a pre-provisioned Postgres set
# TEST_DATABASE_URL per service to its own database, e.g.:
#   cd inventory && TEST_DATABASE_URL=postgres://u:p@localhost:5432/inventory_db?sslmode=disable \
#     go test -tags=integration ./test/integration/...
test-integration:
	@for s in $(SERVICES); do echo "== integration $$s =="; (cd $$s && $(GO) test -count=1 -tags=integration ./test/integration/...) || exit 1; done

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

compose-up:
	docker compose up --build

compose-down:
	docker compose down -v

docker-build:
	@for s in $(SERVICES); do \
	  echo "== image $$s =="; \
	  docker build -f deploy/docker/$$s.Dockerfile -t logistics-$$s . || exit 1; \
	done

# --- Local Kubernetes (kind + ingress-nginx) ----------------------------------

cluster-up:
	kind create cluster --name $(KIND_CLUSTER) --config $(KIND_CONFIG)

ingress-up:
	helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx >/dev/null 2>&1 || true
	helm repo update ingress-nginx >/dev/null
	helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
	  --kube-context $(KIND_CONTEXT) \
	  --namespace ingress-nginx --create-namespace \
	  -f $(INGRESS_VALUES) --wait --timeout 5m
	$(KUBECTL) -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=120s

# Deploys a tiny echo app behind an Ingress, curls it through host:8080, then
# tears it down. Proves the full path works before any real service exists.
cluster-verify:
	$(KUBECTL) apply -f deploy/kind/whoami-smoke.yaml
	$(KUBECTL) -n ingress-smoke rollout status deploy/whoami --timeout=120s
	@echo "== GET http://localhost:8080/whoami (retrying until ingress syncs) =="
	@for i in $$(seq 1 15); do \
	  code=$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/whoami || true); \
	  if [ "$$code" = "200" ]; then echo "OK (HTTP 200):"; curl -fsS http://localhost:8080/whoami | head -n 8; break; fi; \
	  echo "attempt $$i: HTTP $$code, waiting for route sync..."; sleep 2; \
	  if [ "$$i" = "15" ]; then echo "FAILED: ingress never returned 200"; $(KUBECTL) delete -f deploy/kind/whoami-smoke.yaml; exit 1; fi; \
	done
	$(KUBECTL) delete -f deploy/kind/whoami-smoke.yaml

metrics-up:
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server >/dev/null 2>&1 || true
	helm repo update metrics-server >/dev/null
	helm upgrade --install metrics-server metrics-server/metrics-server \
	  --kube-context $(KIND_CONTEXT) \
	  --namespace kube-system \
	  -f $(METRICS_VALUES) --wait --timeout 5m

# Build the three service images and load them into the kind node (no registry).
images:
	@for s in $(SERVICES); do \
	  echo "== image logistics-$$s:dev =="; \
	  docker build -f deploy/docker/$$s.Dockerfile -t logistics-$$s:dev . || exit 1; \
	  kind load docker-image logistics-$$s:dev --name $(KIND_CLUSTER) || exit 1; \
	done

deploy:
	$(KUBECTL) apply -k $(K8S_OVERLAY)
	$(KUBECTL) -n logistics rollout status statefulset/postgres --timeout=180s
	$(KUBECTL) -n logistics rollout status deploy/auth --timeout=180s
	$(KUBECTL) -n logistics rollout status deploy/inventory --timeout=180s
	$(KUBECTL) -n logistics rollout status deploy/shipment --timeout=180s

undeploy:
	$(KUBECTL) delete -k $(K8S_OVERLAY) --ignore-not-found

# Runs the happy path through the gateway. Each service sits behind its own
# prefix (/auth, /inventory, /shipment); auth keeps its prefix, the other two
# have it stripped. The bare /health route isn't exposed, so skip the preflight.
smoke-k8s:
	AUTH_URL=http://localhost:8080 \
	INV_URL=http://localhost:8080/inventory \
	SHIP_URL=http://localhost:8080/shipment \
	SKIP_HEALTHCHECK=1 scripts/smoke.sh

# One command from nothing to a running, smoke-tested stack.
k8s-up: cluster-up ingress-up metrics-up images deploy
	@echo "Stack is up. Try: make smoke-k8s"

cluster-down:
	kind delete cluster --name $(KIND_CLUSTER)
