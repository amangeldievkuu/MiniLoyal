#!/usr/bin/env bash
# Phase 3 end-to-end smoke test: points rules, POS earn/redeem through an API key,
# dashboard adjustments, history, summary, scopes, and ledger integrity in the database.
# The staff scan flow needs a real LINE ID token; it is covered by Go integration tests.
set -euo pipefail
BASE="${BASE:-http://127.0.0.1:18080}"
DB="${DB:-postgres://miniloyal:miniloyal@127.0.0.1:5434/miniloyal}"
JAR="$(mktemp)"
RUN=$(date +%s)
EMAIL="points-$RUN@example.com"
PASS="correct-horse-battery-staple"
OA_SECRET="oa-secret-$RUN"
STATUS=""; BODY=""
fail() { echo "FAIL: $*" >&2; exit 1; }
json() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval("d"+sys.argv[1]))' "$1"; }
req() { # METHOD PATH TOKEN IDEM [BODY]
  local m=$1 p=$2 tok=$3 idem=$4 body=${5:-}
  local args=(-s -b "$JAR" -c "$JAR" -X "$m" -H 'Content-Type: application/json' -w '\n%{http_code}')
  [ -n "$tok" ] && args+=(-H "Authorization: Bearer $tok")
  [ -n "$idem" ] && args+=(-H "Idempotency-Key: $idem")
  [ -n "$body" ] && args+=(-d "$body")
  local out; out=$(curl "${args[@]}" "$BASE$p")
  STATUS=${out##*$'\n'}; BODY=${out%$'\n'*}
}
key() { # METHOD PATH APIKEY IDEM [BODY]
  local args=(-s -X "$1" -H 'Content-Type: application/json' -H "X-API-Key: $3" -w '\n%{http_code}')
  [ -n "$4" ] && args+=(-H "Idempotency-Key: $4")
  [ -n "${5:-}" ] && args+=(-d "$5")
  local out; out=$(curl "${args[@]}" "$BASE$2")
  STATUS=${out##*$'\n'}; BODY=${out%$'\n'*}
}
expect() { [ "$STATUS" = "$1" ] || fail "$2: expected $1 got $STATUS: $BODY"; echo "ok   $2 [$STATUS]"; }
webhook() {
  local sig; sig=$(printf '%s' "$3" | openssl dgst -sha256 -hmac "$2" -binary | base64)
  local out; out=$(curl -s -X POST -H 'Content-Type: application/json' -H "X-Line-Signature: $sig" -d "$3" -w '\n%{http_code}' "$BASE/webhooks/line/$1")
  STATUS=${out##*$'\n'}; BODY=${out%$'\n'*}
}

req POST /v1/dashboard/auth/register "" "" "{\"email\":\"$EMAIL\",\"password\":\"$PASS\",\"tenant_name\":\"Points Cafe\",\"tenant_type\":\"brand\"}"
expect 201 "register owner"
TOKEN=$(printf '%s' "$BODY" | json '["access_token"]')
req GET /v1/dashboard/me "$TOKEN" ""; TID=$(printf '%s' "$BODY" | json '["memberships"][0]["tenant"]["id"]')
req POST "/v1/dashboard/tenants/$TID/apps" "$TOKEN" "$(uuidgen)" '{"name":"Points Cafe App"}'
expect 201 "create app"; AID=$(printf '%s' "$BODY" | json '["id"]')
req PATCH "/v1/dashboard/apps/$AID" "$TOKEN" "$(uuidgen)" "{\"oa_channel_id\":\"30$RUN\",\"oa_channel_secret\":\"$OA_SECRET\"}"
expect 200 "set OA credentials"

# A member appears through a follow webhook.
UID_="U$(printf '%032d' "$RUN")"
webhook "$AID" "$OA_SECRET" "{\"destination\":\"Ubot\",\"events\":[{\"type\":\"follow\",\"webhookEventId\":\"p3-$RUN\",\"deliveryContext\":{\"isRedelivery\":false},\"timestamp\":$(date +%s)000,\"source\":{\"type\":\"user\",\"userId\":\"$UID_\"},\"mode\":\"active\"}]}"
expect 200 "follow creates member"
req GET "/v1/dashboard/apps/$AID/members" "$TOKEN" ""; MID=$(printf '%s' "$BODY" | json '["items"][0]["id"]')

# Rules: defaults, then NT$100 = 2 points, minimum NT$50, expiry 30 days.
req GET "/v1/dashboard/apps/$AID/points-rules" "$TOKEN" ""
expect 200 "default rules"
[ "$(printf '%s' "$BODY" | json '["spend_per_point"]')" = 100 ] || fail "default spend_per_point"
req PUT "/v1/dashboard/apps/$AID/points-rules" "$TOKEN" "$(uuidgen)" '{"currency":"TWD","spend_per_point":100,"points_per_unit":2,"min_spend":50,"expiry_days":30,"enabled":true}'
expect 200 "save rules"

req POST "/v1/dashboard/apps/$AID/api-keys" "$TOKEN" "$(uuidgen)" '{"name":"POS","scopes":["members:read","points:write"]}'
KEY=$(printf '%s' "$BODY" | json '["key"]')
req POST "/v1/dashboard/apps/$AID/api-keys" "$TOKEN" "$(uuidgen)" '{"name":"Reader","scopes":["members:read"]}'
READER=$(printf '%s' "$BODY" | json '["key"]')

# POS earn by spend: floor(1250 / 100) * 2 = 24 points.
IDEM=$(uuidgen)
key POST "/v1/members/$MID/points" "$KEY" "$IDEM" '{"type":"earn","spend_amount":1250,"ref_type":"receipt","ref_id":"R-1001"}'
expect 200 "POS earn by spend"
[ "$(printf '%s' "$BODY" | json '["points"]')" = 24 ] || fail "earned $(printf '%s' "$BODY" | json '["points"]'), want 24"
TXN=$(printf '%s' "$BODY" | json '["id"]')
key POST "/v1/members/$MID/points" "$KEY" "$IDEM" '{"type":"earn","spend_amount":1250,"ref_type":"receipt","ref_id":"R-1001"}'
expect 200 "POS retry replays"
[ "$(printf '%s' "$BODY" | json '["id"]')" = "$TXN" ] || fail "replay created a new transaction"
key POST "/v1/members/$MID/points" "$KEY" "$IDEM" '{"type":"earn","spend_amount":9999}'
expect 422 "same key, different body"
key POST "/v1/members/$MID/points" "$KEY" "$(uuidgen)" '{"type":"earn","spend_amount":30}'
expect 400 "below minimum spend"
key POST "/v1/members/$MID/points" "$READER" "$(uuidgen)" '{"type":"earn","spend_amount":1000}'
expect 403 "members:read key cannot write points"

# Redeem and insufficient points.
key POST "/v1/members/$MID/points" "$KEY" "$(uuidgen)" '{"type":"redeem","points":10,"ref_type":"order","ref_id":"O-7"}'
expect 200 "POS redeem 10"
[ "$(printf '%s' "$BODY" | json '["balance_after"]')" = 14 ] || fail "balance after redeem"
key POST "/v1/members/$MID/points" "$KEY" "$(uuidgen)" '{"type":"redeem","points":100}'
expect 409 "redeem beyond balance"
printf '%s' "$BODY" | command grep -q insufficient_points || fail "409 code"

# Dashboard adjustments.
req POST "/v1/dashboard/members/$MID/points/adjustments" "$TOKEN" "$(uuidgen)" '{"delta":5,"note":"birthday bonus"}'
expect 200 "adjust +5"
req POST "/v1/dashboard/members/$MID/points/adjustments" "$TOKEN" "$(uuidgen)" '{"delta":-30,"note":"too much"}'
expect 409 "adjust below zero refused"

# Summary and history.
req GET "/v1/dashboard/members/$MID/points" "$TOKEN" ""
expect 200 "summary"
[ "$(printf '%s' "$BODY" | json '["balance"]')" = 19 ] || fail "balance $(printf '%s' "$BODY" | json '["balance"]'), want 19"
[ "$(printf '%s' "$BODY" | json '["lifetime_earned"]')" = 24 ] || fail "lifetime earned"
[ "$(printf '%s' "$BODY" | json '["expiring"].__len__()')" -ge 1 ] || fail "expiring lots missing"
req GET "/v1/dashboard/members/$MID/points/history?limit=2" "$TOKEN" ""
expect 200 "history page 1"
CUR=$(printf '%s' "$BODY" | json '["next_cursor"]')
[ "$(printf '%s' "$BODY" | json '["items"][0]["kind"]')" = adjust ] || fail "newest first"
req GET "/v1/dashboard/members/$MID/points/history?limit=2&cursor=$CUR" "$TOKEN" ""
expect 200 "history page 2"
[ "$(printf '%s' "$BODY" | json '["items"][0]["kind"]')" = earn ] || fail "oldest on page 2"
key GET "/v1/members/$MID/points" "$READER" ""
expect 200 "server summary with members:read"

# Ledger integrity straight from the database.
read -r BAL LEDGER LOTS < <(psql "$DB" -Atc "select a.balance, (select sum(delta) from points_ledger where member_id=a.member_id), (select sum(remaining) from points_lots where member_id=a.member_id) from points_accounts a where member_id='$MID'" | tr '|' ' ')
[ "$BAL" = 19 ] && [ "$LEDGER" = 19 ] && [ "$LOTS" = 19 ] || fail "balance $BAL, ledger $LEDGER, lots $LOTS"
echo "ok   balance = ledger sum = open lots = $BAL"
ROWS=$(psql "$DB" -Atc "select count(*) from points_ledger where member_id='$MID'")
echo "ok   ledger rows: $ROWS (one per lot touched)"
echo "ALL PHASE 3 SMOKE CHECKS PASSED"
