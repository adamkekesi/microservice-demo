# Kubernetes deployment (local kind cluster)

A reproducible local cluster running the full stack: the three services, a
Postgres holding the three logical databases, an ingress-nginx API gateway, and
per-service autoscaling. Everything is declared as files in this repo — no
manual cluster setup.

## Layout

```
deploy/
├── kind/kind-config.yaml          # the cluster (1 node, host ports 8080/8443)
├── platform/
│   ├── ingress-nginx-values.yaml  # API gateway (Helm)
│   └── metrics-server-values.yaml # metrics for HPA / kubectl top (Helm)
└── k8s/
    ├── base/                      # environment-agnostic manifests
    │   ├── postgres/              # StatefulSet + headless Service + init SQL
    │   ├── auth|inventory|shipment/  # Deployment + Service + HPA each
    │   └── ingress.yaml           # path routing for all three services
    └── overlays/local/            # namespace, secret values, image tags (kind)
```

Tooling: **kind** (cluster) + **Helm** (ingress-nginx, metrics-server) +
**Kustomize** (app manifests). No Terraform — there is no cloud to provision.

## Bring it up

```bash
make k8s-up        # cluster + ingress + metrics + build/load images + deploy
make smoke-k8s     # happy-path test through the gateway (localhost:8080)
```

Or step by step: `make cluster-up`, `ingress-up`, `metrics-up`, `images`,
`deploy`. Tear down with `make undeploy` (app only) or `make cluster-down`
(whole cluster). See `make help`.

## How the pieces map

- **API gateway** — ingress-nginx, reachable at `http://localhost:8080`. Exactly
  three routes, one per service, matching the service name:
  - `/auth/...`      → auth (passed through — auth already serves `/auth/*`)
  - `/inventory/...` → inventory (prefix stripped: `/inventory/warehouses` → `/warehouses`)
  - `/shipment/...`  → shipment (prefix stripped: `/shipment/shipments` → `/shipments`)

  Two Ingress objects (`deploy/k8s/base/ingress.yaml`): auth keeps its prefix,
  the other two strip it via `rewrite-target`. The bare `/health`, `/ready`,
  `/.well-known/jwks.json` and `/docs` paths are intentionally not on the gateway
  (JWKS is fetched in-cluster, not through it).
- **Swagger / OpenAPI** — each service serves Swagger UI at `/docs` and its spec
  at `/openapi.yaml`. They use identical paths, so they can't share one host;
  `deploy/k8s/base/docs-ingress.yaml` gives each its own hostname:
  - http://auth.localhost:8080/docs
  - http://inventory.localhost:8080/docs
  - http://shipment.localhost:8080/docs

  Each spec lists `http://<svc>.localhost:8080` as a server, so "Try it out"
  works. (`*.localhost` resolves to loopback on systemd hosts.)
- **Load balancing** — each service is a `ClusterIP` Service spreading traffic
  across its pods. In-cluster URLs match compose (`http://auth:8001`,
  `http://inventory:8002`).
- **Scaling** — a `HorizontalPodAutoscaler` per service (CPU 70%). Requires
  metrics-server (`make metrics-up`) and the CPU `requests` set on each pod.
- **Config & secrets** — non-secret env is inline in the Deployments; secret
  values (`POSTGRES_*`, `ADMIN_*`) come from a Kustomize `secretGenerator` in
  the local overlay. The name-hash restarts pods automatically on change.

## Startup ordering

Init containers make startup deterministic instead of relying on crash-restart:

- every service waits for Postgres (`nc -z postgres 5432`);
- `inventory` and `shipment` additionally wait for auth's JWKS endpoint, because
  their `/ready` probe only passes once the one-shot JWKS `Prime()` has cached a
  key. Without this they would deadlock (never ready → never get traffic → never
  retry the lazy fetch).

## Auth signing key

`auth` starts at **1 replica** so the first pod generates and persists the RSA
signing key into `auth_db`. Once persisted, the HPA can scale auth up safely
(later pods read the same key). For multi-replica auth from a cold start,
provide a fixed key via `JWT_PRIVATE_KEY` / `JWT_PRIVATE_KEY_PATH` in a `Secret`
instead.

> **If you reset Postgres** (e.g. delete the PVC), auth generates a *new* key
> under the same `kid`. Downstream services that cached the old key won't
> re-fetch (the kid is still "known"), so they'll reject tokens until their JWKS
> cache TTL expires. Just restart them: `kubectl -n logistics rollout restart
> deploy/inventory deploy/shipment`.

## Observability (Datadog) — optional, off by default

The app images speak Datadog APM, but no agent is deployed here, so the
Deployments set `DD_TRACE_ENABLED=false` and the stack runs with zero secrets.
`DD_AGENT_HOST` is already wired from the node host IP, so to enable telemetry:
deploy the Datadog agent as a node-local `DaemonSet` (so each pod reaches it on
its own node), then flip `DD_TRACE_ENABLED=true`:

```yaml
env:
  - name: DD_AGENT_HOST
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
```

## Topology notes

- One Postgres `StatefulSet` with one logical database per service (no
  cross-service tables). For a more production-like split, give each service its
  own Postgres later.
- Liveness `/health`; readiness `/ready` (200 only once DB — and for
  inventory/shipment, JWKS — are reachable).
