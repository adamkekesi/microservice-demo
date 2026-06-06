# Logistics monorepo. Commands run per-module because go.work + a non-module
# repo root means `go test ./...` from the root cannot span modules. Build/test
# use the go.work overlay for the local `platform` (offline, no credentials);
# `tidy` fetches the PUBLISHED platform module (needs GOPRIVATE + git creds).

# Load the local, git-ignored .env (if present) so secrets like DD_API_KEY are
# available to targets such as datadog-up without exporting them by hand. Keep
# .env to simple KEY=value lines (no shell expansion). docker compose reads it
# automatically; this makes `make` see it too.
-include .env
export

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
DD_OPERATOR_VALUES := deploy/platform/datadog-operator-values.yaml
DD_AGENT_CR        := deploy/platform/datadog-agent.yaml
K8S_OVERLAY    := deploy/k8s/overlays/local
KUBECTL        := kubectl --context $(KIND_CONTEXT)

.PHONY: help tidy fmt build test test-integration test-all lint \
        run-auth run-inventory run-shipment seed compose-up compose-down docker-build \
        cluster-up ingress-up metrics-up datadog-up cluster-verify cluster-down \
        images deploy undeploy smoke-k8s k8s-up \
        k6-operator-up k6-operator-down loadtest loadtest-logs loadtest-clean

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
	@echo "  datadog-up       install Datadog Operator + Agent (needs DD_API_KEY)"
	@echo "  cluster-verify   prove ingress routing works (deploys + deletes a smoke app)"
	@echo "  images           build the 3 service images and load them into kind"
	@echo "  deploy           apply the app manifests (kubectl apply -k overlays/local)"
	@echo "  smoke-k8s        happy-path smoke test through the gateway (localhost:8080)"
	@echo "  undeploy         delete the app manifests from the cluster"
	@echo "  k8s-up           full bring-up: cluster + ingress + metrics + images + deploy"
	@echo "  cluster-down     delete the local kind cluster"
	@echo "  k6-operator-up   install the Grafana k6 operator (Helm)"
	@echo "  loadtest         run a single ad-hoc k6 TestRun (parallelism across runner pods)"
	@echo "  loadtest-logs    tail the k6 runner pods (live progress + end-of-test summary)"
	@echo "  loadtest-clean   delete the TestRun + script ConfigMap"
	@echo "  k6-operator-down uninstall the k6 operator"
	@echo "  soak-up          start the continuous diurnal soak (operator + CronJobs + first run)"
	@echo "  soak-status      TestRun / runner pods / CronJobs / HPAs / DB row counts"
	@echo "  soak-prune-now   trigger one hourly delete wave immediately"
	@echo "  soak-down        stop the soak (CronJobs + TestRun + ConfigMaps)"

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

# Datadog Operator + node-local Agent. Optional opt-in (not part of k8s-up)
# because it needs a real API key. Provide it via the environment:
#   DD_API_KEY=<your key> make datadog-up
# The key is written to a Secret (idempotently) and never committed. The Agent
# receives APM on host port 8126 / DogStatsD on 8125; the services already point
# DD_AGENT_HOST at status.hostIP, and overlays/local enables DD_TRACE_ENABLED.
datadog-up:
	@test -n "$$DD_API_KEY" || { echo "ERROR: set DD_API_KEY=<your key> first"; exit 1; }
	helm repo add datadog https://helm.datadoghq.com >/dev/null 2>&1 || true
	helm repo update datadog >/dev/null
	helm upgrade --install datadog-operator datadog/datadog-operator \
	  --kube-context $(KIND_CONTEXT) \
	  --namespace datadog --create-namespace \
	  -f $(DD_OPERATOR_VALUES) --wait --timeout 5m
	$(KUBECTL) create secret generic datadog-secret -n datadog \
	  --from-literal api-key=$$DD_API_KEY --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) apply -f $(DD_AGENT_CR)
	$(KUBECTL) -n datadog rollout status daemonset/datadog-agent --timeout=300s
	# Re-apply the overlay so the datadog component's DD_TRACE_ENABLED=true reaches
	# the Deployments (a plain `rollout restart` would keep the old, disabled spec).
	$(KUBECTL) apply -k $(K8S_OVERLAY)
	$(KUBECTL) -n logistics rollout status deploy/auth deploy/inventory deploy/shipment --timeout=180s
	@echo "Datadog up. Generate traffic with 'make smoke-k8s', then view APM at https://app.datadoghq.eu"

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
k8s-up: cluster-up ingress-up metrics-up images deploy datadog-up
	@echo "Stack is up. Try: make smoke-k8s"

cluster-down:
	kind delete cluster --name $(KIND_CLUSTER)

# --- k6 load simulator (Grafana k6 operator) ----------------------------------

K6_OPERATOR_NS := k6-operator-system
LOADTEST_NS    := logistics
K6_SCRIPT      := loadtest/k6/scenarios.js
K6_TESTRUN     := loadtest/k8s/testrun.yaml
K6_CONFIGMAP   := k6-load-test

# Install the operator that turns a TestRun + `parallelism` into N runner pods.
# The chart templates its own Namespace, which deadlocks with helm: --create-
# namespace races it ("already exists"), but without the namespace helm can't
# store its release metadata ("not found"). So we pre-create the namespace and
# tell the chart not to template it (namespace.create=false).
k6-operator-up:
	helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
	helm repo update grafana >/dev/null
	$(KUBECTL) create namespace $(K6_OPERATOR_NS) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	helm upgrade --install k6-operator grafana/k6-operator \
	  --kube-context $(KIND_CONTEXT) \
	  --namespace $(K6_OPERATOR_NS) --set namespace.create=false --wait --timeout 5m
	$(KUBECTL) -n $(K6_OPERATOR_NS) rollout status deploy/k6-operator-controller-manager --timeout=120s

# Recreate the script ConfigMap from the file, then (re)start the TestRun.
# Deleting any prior TestRun first lets this be re-run cleanly.
loadtest:
	$(KUBECTL) -n $(LOADTEST_NS) create configmap $(K6_CONFIGMAP) \
	  --from-file=$(K6_SCRIPT) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) delete -f $(K6_TESTRUN) --ignore-not-found
	$(KUBECTL) apply -f $(K6_TESTRUN)
	@echo "TestRun started. Watch: make loadtest-logs   (and: kubectl get hpa -n $(LOADTEST_NS) -w)"

# Tail the runner pods: live k6 progress and the end-of-test summary/thresholds.
loadtest-logs:
	$(KUBECTL) -n $(LOADTEST_NS) logs -l k6_cr=logistics-load -f --max-log-requests 10

loadtest-clean:
	$(KUBECTL) delete -f $(K6_TESTRUN) --ignore-not-found
	$(KUBECTL) -n $(LOADTEST_NS) delete configmap $(K6_CONFIGMAP) --ignore-not-found

k6-operator-down:
	helm uninstall k6-operator --kube-context $(KIND_CONTEXT) --namespace $(K6_OPERATOR_NS) || true

# --- continuous soak (diurnal, resumable, hourly delete wave) -----------------

K6_SOAK_DIR     := loadtest/k8s/soak
K6_TESTRUN_CM   := k6-testrun-manifest

# Start the continuous soak: the k6 script ConfigMap, a ConfigMap holding the
# TestRun manifest (mounted by the launcher), the soak RBAC + two CronJobs
# (hourly launcher = resumability; hourly delete wave = retention), and an
# initial TestRun so load starts immediately instead of at the next top-of-hour.
soak-up: k6-operator-up
	$(KUBECTL) -n $(LOADTEST_NS) create configmap $(K6_CONFIGMAP) \
	  --from-file=$(K6_SCRIPT) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(LOADTEST_NS) create configmap $(K6_TESTRUN_CM) \
	  --from-file=testrun.yaml=$(K6_TESTRUN) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) apply -f $(K6_SOAK_DIR)
	$(KUBECTL) delete -f $(K6_TESTRUN) --ignore-not-found
	$(KUBECTL) apply -f $(K6_TESTRUN)
	@echo "Soak started. Launcher relaunches hourly (resumable); delete wave runs at :30."
	@echo "Watch: make soak-status   |   logs: make loadtest-logs"

# Snapshot of the soak: TestRun, runner pods, CronJobs, and live DB row counts
# per service (proof the hourly delete wave keeps storage bounded).
soak-status:
	@echo "== TestRun =="; $(KUBECTL) -n $(LOADTEST_NS) get testruns 2>/dev/null || true
	@echo "== runner pods =="; $(KUBECTL) -n $(LOADTEST_NS) get pods -l k6_cr=logistics-load 2>/dev/null || true
	@echo "== CronJobs =="; $(KUBECTL) -n $(LOADTEST_NS) get cronjobs
	@echo "== HPAs =="; $(KUBECTL) -n $(LOADTEST_NS) get hpa
	@echo "== DB row counts (single postgres, 3 logical DBs) =="; \
	  $(KUBECTL) -n $(LOADTEST_NS) exec statefulset/postgres -- sh -c '\
	    psql -U logistics -d auth_db      -tAc "select '\''users='\''||count(*) from users"; \
	    psql -U logistics -d inventory_db -tAc "select '\''reservations='\''||count(*) from reservations"; \
	    psql -U logistics -d shipment_db  -tAc "select '\''shipments='\''||count(*) from shipments"' 2>/dev/null || true

# Manually trigger one delete wave now (don't wait for :30).
soak-prune-now:
	$(KUBECTL) -n $(LOADTEST_NS) create job --from=cronjob/k6-delete-wave k6-delete-wave-manual-$$(date +%s)

soak-down:
	$(KUBECTL) delete -f $(K6_SOAK_DIR) --ignore-not-found
	$(KUBECTL) delete -f $(K6_TESTRUN) --ignore-not-found
	$(KUBECTL) -n $(LOADTEST_NS) delete configmap $(K6_CONFIGMAP) $(K6_TESTRUN_CM) --ignore-not-found
