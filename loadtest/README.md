# Load simulator — k6 + Kubernetes operator (continuous diurnal soak)

A realistic, week-capable soak test for the logistics stack, driven by the
[Grafana k6 operator](https://github.com/grafana/k6-operator).

## The technique

You write **one** k6 script and pick a **parallelism** number. The operator
spawns that many runner pods and k6 splits the configured load across them via
execution segments — load generated *inside* the cluster, distributed across
pods, managed declaratively as a `TestRun` custom resource.

```
loadtest/
├── k6/scenarios.js          # the visitor session: log in / register → browse → order → self-delete
├── k8s/testrun.yaml         # TestRun CR: parallelism + diurnal env, run for ~58m
├── k8s/soak/
│   ├── rbac.yaml            # ServiceAccount/Role for the launcher
│   ├── cronjob-launcher.yaml# hourly: relaunch the TestRun (resumability)
│   └── cronjob-prune.yaml   # hourly: the "giant delete wave" (retention)
└── README.md
```

## How the requirements are met

**Diurnal traffic, wall-clock driven.** `scenarios.js` reads the current hour at
init and picks the arrival rate: **40 visitors/s during 08:00–12:00**, **10/s**
the rest of the day (all tunable via env on the TestRun). A `constant-arrival-rate`
executor holds that rate; the operator divides it across the runner pods.

**Resumable for a week.** A single multi-day k6 process would be fragile (one
pod eviction restarts it from zero). Instead the **`k6-load-launcher` CronJob**
relaunches the TestRun at the top of every hour (each run lasts ~58m). Because
the rate is a pure function of the wall clock, a crash or cluster downtime
self-heals: the next hourly tick resumes at the correct level. No checkpoint to
lose.

**Returning vs new visitors, with token reuse.** 70% of visits are returning
users; 30% are first-time signups (register + log in). A returning visitor has a
stable per-VU identity and **reuses a cached access token for most of its 900s
life**, logging in again only when the token nears expiry — exactly how a real
client with a 15-minute session behaves. So most returning visits make no auth
call at all, which keeps login/bcrypt load low and lets auth scale with real
demand rather than with raw visitor count. The cached JWT keeps working even
after the delete wave reclaims the account (services verify the signature, not
row existence); the next re-login then re-registers it on demand.

**The request mix deletes data continuously.** Every visitor that places an
order confirms/cancels it and then `DELETE`s its own shipment and reservation —
using the new per-resource delete endpoints.

**Giant delete wave every hour.** The **`k6-delete-wave` CronJob** (at :30) logs
in as admin and calls each service's bulk-purge endpoint
(`DELETE /shipment/shipments?before=…`, `…/inventory/reservations?before=…`,
`…/auth/users?before=…`). Three big server-side deletes remove the hour's
accumulated terminal shipments/reservations and throwaway users, keeping each
service's Postgres (a dedicated instance per service, each a 10Gi PVC) bounded.
Active (PENDING) records and the fixtures are left untouched.

**No k6 metrics exported.** The services already emit Datadog traces/metrics
(including the new `logistics.*.deleted` / `logistics.*.purged` series), so k6
runs without an output; its end-of-test summary still lands in the runner logs.

## Run it

Prerequisites: the app stack is up — `make k8s-up` (cluster + ingress + metrics
+ services), so the HPAs can scale.

```bash
make soak-up           # operator + script/manifest ConfigMaps + RBAC + CronJobs + first run
make soak-status       # TestRun, runner pods, CronJobs, HPAs, live DB row counts
make loadtest-logs     # tail the k6 runner pods (per-segment summary)
make soak-prune-now    # trigger one delete wave immediately (don't wait for :30)
make soak-down         # stop everything
```

`mise run soak-up`, `mise run soak-status`, `mise run soak-down` wrap the same
targets.

## Tuning

- **Rates / peak window** — edit the `VISITORS_PEAK`, `VISITORS_OFF`,
  `PEAK_START_HOUR`, `PEAK_END_HOUR` env in `k8s/testrun.yaml`.
- **Local time** — set `TZ_OFFSET_HOURS` (e.g. `2` for CEST) so "08:00" means
  your wall clock, not UTC.
- **Parallelism** — `spec.parallelism` in `k8s/testrun.yaml` (more runner pods =
  same total rate split into more segments).
- **Order/delete intensity** — `ORDER_RATE` (fraction of visitors who order and
  then delete).
- **Returning vs new** — `RETURNING_RATE` (default 0.7) and `RETURNING_POOL`
  (number of reusable accounts, default 1000).
- **Retention window** — the delete wave purges everything terminal older than
  *now*; narrow it (keep a trailing window) by changing `CUTOFF` in
  `k8s/soak/cronjob-prune.yaml`.
- **Target** — `K6_BASE_URL` defaults to the in-cluster gateway; point it at a
  service (e.g. `http://auth:8001`) to bypass the gateway.
