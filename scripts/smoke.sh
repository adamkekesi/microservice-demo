#!/usr/bin/env bash
# Happy-path smoke test against a RUNNING stack (e.g. after `mise run up`).
# Re-runnable: uses a unique suffix for the customer/warehouse/item each time.
#   scripts/smoke.sh         # or: mise run smoke
set -uo pipefail

AUTH=${AUTH_URL:-http://localhost:8001}
INV=${INV_URL:-http://localhost:8002}
SHIP=${SHIP_URL:-http://localhost:8003}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@example.com}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-changeme123}
SUFFIX="$(date +%s)$RANDOM"

fail() { echo "SMOKE FAIL: $1"; exit 1; }
command -v curl >/dev/null || fail "curl not found"
command -v jq   >/dev/null || fail "jq not found"

for p in 8001 8002 8003; do
  curl -s --retry 30 --retry-delay 1 --retry-connrefused -o /dev/null "http://localhost:$p/health" \
    || fail "service on :$p not healthy"
done

CUST_EMAIL="cust+$SUFFIX@example.com"
curl -s -XPOST "$AUTH/auth/register" -d "{\"email\":\"$CUST_EMAIL\",\"password\":\"supersecret\"}" >/dev/null
CUST=$(curl -s -XPOST "$AUTH/auth/login" -d "{\"email\":\"$CUST_EMAIL\",\"password\":\"supersecret\"}" | jq -r .access_token)
[ -n "$CUST" ] && [ "$CUST" != null ] || fail "customer login"

ADMIN=$(curl -s -XPOST "$AUTH/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | jq -r .access_token)
[ -n "$ADMIN" ] && [ "$ADMIN" != null ] || fail "admin login (check ADMIN_EMAIL/ADMIN_PASSWORD)"

WH=$(curl -s -XPOST "$INV/warehouses" -H "Authorization: Bearer $ADMIN" -d "{\"code\":\"WH-$SUFFIX\",\"name\":\"Smoke WH\"}" | jq -r .id)
IT=$(curl -s -XPOST "$INV/items"      -H "Authorization: Bearer $ADMIN" -d "{\"sku\":\"SKU-$SUFFIX\",\"name\":\"Smoke Item\"}" | jq -r .id)
[ -n "$WH" ] && [ "$WH" != null ] || fail "create warehouse"
[ -n "$IT" ] && [ "$IT" != null ] || fail "create item"
curl -s -XPUT "$INV/stock" -H "Authorization: Bearer $ADMIN" \
  -d "{\"warehouse_id\":\"$WH\",\"item_id\":\"$IT\",\"quantity_on_hand\":100}" >/dev/null

SID=$(curl -s -XPOST "$SHIP/shipments" -H "Authorization: Bearer $CUST" \
  -d "{\"item_id\":\"$IT\",\"warehouse_id\":\"$WH\",\"quantity\":30,\"destination_address\":\"1 Main St\"}" | jq -r .id)
[ -n "$SID" ] && [ "$SID" != null ] || fail "create shipment"

AVAIL=$(curl -s "$INV/stock?warehouse_id=$WH&item_id=$IT" -H "Authorization: Bearer $CUST" | jq -r .quantity_available)
[ "$AVAIL" = 70 ] || fail "available expected 70, got $AVAIL"

CONF=$(curl -s -XPOST "$SHIP/shipments/$SID/confirm" -H "Authorization: Bearer $CUST" | jq -r .status)
[ "$CONF" = CONFIRMED ] || fail "confirm status: $CONF"

ONHAND=$(curl -s "$INV/stock?warehouse_id=$WH&item_id=$IT" -H "Authorization: Bearer $CUST" | jq -r .quantity_on_hand)
[ "$ONHAND" = 70 ] || fail "on_hand expected 70, got $ONHAND"

echo "SMOKE OK: shipment=$SID available_after_reserve=$AVAIL on_hand_after_confirm=$ONHAND"
