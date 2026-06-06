# Kubernetes deployment notes

Full manifests are intentionally **out of scope** for this iteration (local
runtime is `docker-compose`). This file documents the only app-side change
needed to run on Kubernetes, so the manifests are a small, well-understood
next step.

## Datadog agent: DaemonSet, node-local

The Datadog agent runs as a **DaemonSet**; each app pod talks to the agent on
its own node via the node's host IP. The single required change versus compose
is sourcing `DD_AGENT_HOST` from the node IP:

```yaml
env:
  - name: DD_AGENT_HOST
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
  - name: DD_ENV
    value: dev
  - name: DD_SERVICE
    value: inventory-service     # auth-service | inventory-service | shipment-service
  - name: DD_VERSION
    value: "1.0.0"
  - name: DD_TRACE_AGENT_PORT
    value: "8126"
  - name: DD_DOGSTATSD_PORT
    value: "8125"
```

## Probes

Wire the liveness/readiness probes to the endpoints every service exposes:

```yaml
livenessProbe:
  httpGet: { path: /health, port: 8002 }
readinessProbe:
  httpGet: { path: /ready, port: 8002 }   # 200 only once DB + JWKS are reachable
```

## Topology

- One `Deployment` + `Service` per app service.
- Postgres per service (a `StatefulSet` or a managed database); keep one logical
  database per service — no cross-service tables.
- The RSA signing key for Auth belongs in a `Secret` mounted via
  `JWT_PRIVATE_KEY_PATH` (or injected as `JWT_PRIVATE_KEY`). All other services
  only ever fetch the public JWKS.
