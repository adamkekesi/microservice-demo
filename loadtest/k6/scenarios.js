// k6 soak simulator for the logistics stack, driven by the Grafana k6 operator.
//
// Shape of the load (requirements):
//   * Diurnal, wall-clock driven: ~40 visitors/s during the 08:00-12:00 peak,
//     ~10 visitors/s the rest of the day. The rate is chosen at script-init
//     from the current hour, so each hourly TestRun launch runs at the level
//     appropriate for *now* — which is also what makes the soak resumable: a
//     crashed/restarted run simply resumes at the correct level for the clock.
//   * Each "visitor" is a self-contained session: register -> login (a fresh
//     token every time, so the 15-min access-token TTL is never a problem) ->
//     browse -> often place an order -> then DELETE its own shipment and
//     reservation. So the request mix deletes data continuously (the hourly
//     "giant delete wave" is a separate retention sweep; see loadtest/k8s/soak).
//
// Metrics: none are exported from k6 — the services are already instrumented
// with Datadog, which is where soak results are read. k6 still prints its
// end-of-test summary to the runner pod logs.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomItem } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

const BASE = __ENV.K6_BASE_URL || 'http://ingress-nginx-controller.ingress-nginx.svc';
const AUTH = `${BASE}/auth`;
const INV = `${BASE}/inventory`;
const SHIP = `${BASE}/shipment`;

const ADMIN_EMAIL = __ENV.ADMIN_EMAIL || 'admin@example.com';
const ADMIN_PASSWORD = __ENV.ADMIN_PASSWORD || 'changeme123';

// Diurnal configuration (all overridable via the TestRun env).
const PEAK_START = parseFloat(__ENV.PEAK_START_HOUR || '8');
const PEAK_END = parseFloat(__ENV.PEAK_END_HOUR || '12');
const PEAK_RATE = parseFloat(__ENV.VISITORS_PEAK || '40'); // visitors/sec 08:00-12:00
const OFF_RATE = parseFloat(__ENV.VISITORS_OFF || '10'); // visitors/sec otherwise
const TZ_OFFSET = parseFloat(__ENV.TZ_OFFSET_HOURS || '0'); // hours to add to UTC
const ORDER_RATE = parseFloat(__ENV.ORDER_RATE || '0.3'); // fraction of visitors who order
const DURATION = __ENV.RUN_DURATION || '58m'; // a touch under an hour so the launcher can recycle cleanly

// Returning vs new visitors. 70% of visits reuse a pooled account (just log in);
// 30% are first-time signups (register + log in). This both models a realistic
// traffic mix and eases the auth bcrypt load, since most visits skip the extra
// registration hash. The pool accounts are role=customer, so the hourly delete
// wave eventually purges them — the returning path is self-healing: if a pooled
// account is gone it is re-registered on demand.
const RETURNING_RATE = parseFloat(__ENV.RETURNING_RATE || '0.7');
const RETURNING_POOL = parseInt(__ENV.RETURNING_POOL || '1000', 10); // distinct reusable accounts
const VISITOR_PASSWORD = 'loadtest123';

const JSON_HEADERS = { 'Content-Type': 'application/json' };

function currentHour() {
  const now = new Date();
  let h = now.getUTCHours() + now.getUTCMinutes() / 60 + TZ_OFFSET;
  h %= 24;
  if (h < 0) h += 24;
  return h;
}

const inPeak = currentHour() >= PEAK_START && currentHour() < PEAK_END;
const rate = inPeak ? PEAK_RATE : OFF_RATE;

export const options = {
  discardResponseBodies: true,
  scenarios: {
    visitors: {
      executor: 'constant-arrival-rate',
      exec: 'visitor',
      // The operator splits this rate across the runner pods via execution
      // segments, so `rate` is the cluster-wide visitors/sec.
      rate,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.max(50, Math.ceil(rate * 2)),
      maxVUs: Math.max(200, Math.ceil(rate * 10)),
    },
  },
  // Soak thresholds: looser than a spike test — we care that the system stays
  // healthy for hours, not about single-request tail latency.
  thresholds: {
    http_req_failed: ['rate<0.05'],
    checks: ['rate>0.95'],
  },
};

// --- helpers ---------------------------------------------------------------

function login(email, password) {
  const res = http.post(`${AUTH}/login`, JSON.stringify({ email, password }), {
    headers: JSON_HEADERS,
    responseType: 'text',
  });
  return res.status === 200 ? res.json('access_token') : null;
}

// acquireToken returns a bearer token for this visit. 70% of the time it is a
// returning user (reuse a pooled account, just log in); otherwise a first-time
// signup (register a unique throwaway account, then log in). The returning path
// self-heals: if a pooled account has been purged by the delete wave, it is
// re-registered on demand.
function acquireToken() {
  if (Math.random() < RETURNING_RATE) {
    const email = `returning-${Math.floor(Math.random() * RETURNING_POOL)}@load.example`;
    let token = login(email, VISITOR_PASSWORD);
    if (!token) {
      http.post(`${AUTH}/register`, JSON.stringify({ email, password: VISITOR_PASSWORD }), { headers: JSON_HEADERS });
      token = login(email, VISITOR_PASSWORD);
    }
    return token;
  }
  const email = `v-${__VU}-${__ITER}-${Date.now()}@load.example`;
  http.post(`${AUTH}/register`, JSON.stringify({ email, password: VISITOR_PASSWORD }), { headers: JSON_HEADERS });
  return login(email, VISITOR_PASSWORD);
}

function bearer(token) {
  return { headers: { ...JSON_HEADERS, Authorization: `Bearer ${token}` } };
}

// --- setup: ensure fixtures exist (idempotent / race-safe per runner) -------

export function setup() {
  const adminToken = login(ADMIN_EMAIL, ADMIN_PASSWORD);
  if (!adminToken) throw new Error('admin login failed — is the stack up?');

  const whId = ensure(`${INV}/warehouses`, { code: 'WH-LOAD', name: 'Load Warehouse' }, 'code', 'WH-LOAD', adminToken);
  const itemId = ensure(`${INV}/items`, { sku: 'SKU-LOAD', name: 'Load Item' }, 'sku', 'SKU-LOAD', adminToken);

  // Huge on-hand so sustained reserve/confirm never exhausts stock over a soak.
  http.put(
    `${INV}/stock`,
    JSON.stringify({ warehouse_id: whId, item_id: itemId, quantity_on_hand: 1000000000 }),
    bearer(adminToken),
  );
  return { whId, itemId };
}

function ensure(listURL, body, field, value, token) {
  const res = http.post(listURL, JSON.stringify(body), { ...bearer(token), responseType: 'text' });
  if (res.status === 201) return res.json('id');
  // Conflict (another runner won the race) — find it in the list.
  const list = http.get(listURL, { ...bearer(token), responseType: 'text' });
  const found = (list.json() || []).find((o) => o[field] === value);
  if (!found) throw new Error(`setup: could not create or find ${value} (status ${res.status})`);
  return found.id;
}

// --- the visitor session ---------------------------------------------------

export function visitor(data) {
  const token = acquireToken();
  if (!token) return;
  const h = bearer(token);

  // Browse.
  check(http.get(`${INV}/warehouses`, h), { 'warehouses 200': (r) => r.status === 200 });
  check(http.get(`${INV}/items`, h), { 'items 200': (r) => r.status === 200 });
  check(http.get(`${INV}/stock?warehouse_id=${data.whId}&item_id=${data.itemId}`, h),
    { 'stock 200': (r) => r.status === 200 });

  // A fraction of visitors place an order, then tidy up after themselves.
  if (Math.random() < ORDER_RATE) {
    placeAndCleanOrder(data, token);
  }

  sleep(Math.random());
}

function placeAndCleanOrder(data, token) {
  const h = { ...bearer(token), responseType: 'text' };
  const createRes = http.post(
    `${SHIP}/shipments`,
    JSON.stringify({ item_id: data.itemId, warehouse_id: data.whId, quantity: 1, destination_address: '1 Load Ave' }),
    h,
  );
  if (!check(createRes, { 'shipment created': (r) => r.status === 201 })) return;
  const shipmentId = createRes.json('id');
  const reservationId = createRes.json('reservation_id');

  // ~75% confirm, ~25% cancel — both move the shipment + reservation to a
  // terminal state, which is the precondition for deleting them.
  const action = randomItem(['confirm', 'confirm', 'confirm', 'cancel']);
  const resolve = http.post(`${SHIP}/shipments/${shipmentId}/${action}`, null, bearer(token));
  if (!check(resolve, { 'shipment resolved': (r) => r.status === 200 })) return;

  // Self-clean: delete the now-terminal shipment, then its reservation.
  check(http.del(`${SHIP}/shipments/${shipmentId}`, null, bearer(token)),
    { 'shipment deleted': (r) => r.status === 204 });
  if (reservationId) {
    check(http.del(`${INV}/reservations/${reservationId}`, null, bearer(token)),
      { 'reservation deleted': (r) => r.status === 204 });
  }
}
