#!/usr/bin/env bash
# Phase 4 end-to-end smoke test: coupon templates and stamp cards from the dashboard,
# a member claiming a coupon, a staff scan, an atomic redeem (the second one is 409),
# stamps to completion with the reward coupon, and the outbound events in the database.
# Member and staff sign in with HS256 ID tokens this script signs with the channel secrets
# it configured, so it needs no LINE account. Run the API with
# LINE_SKIP_CREDENTIAL_VERIFICATION=true (development only).
set -euo pipefail
BASE="${BASE:-http://127.0.0.1:18080}"
DB="${DB:-postgres://miniloyal:miniloyal@127.0.0.1:5434/miniloyal}"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT
RUN=$(date +%s)
EMAIL="coupons-$RUN@example.com"
PASS="correct-horse-battery-staple"
MINI_CHANNEL="16$RUN"; MINI_SECRET="mini-secret-$RUN"
STAFF_CHANNEL="17$RUN"; STAFF_SECRET="staff-secret-$RUN"
MEMBER_SUB="U$(printf '%032d' "$RUN")"
STAFF_SUB="U$(printf '%031d' "$RUN")9"
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
expect() { [ "$STATUS" = "$1" ] || fail "$2: expected $1 got $STATUS: $BODY"; echo "ok   $2 [$STATUS]"; }
# idtoken CHANNEL_ID SECRET SUB prints an HS256 LINE ID token for the channel.
idtoken() {
  python3 - "$1" "$2" "$3" <<'PY'
import base64, hashlib, hmac, json, sys, time
aud, secret, sub = sys.argv[1:4]
enc = lambda d: base64.urlsafe_b64encode(json.dumps(d, separators=(',', ':')).encode()).rstrip(b'=')
now = int(time.time())
head = enc({'alg': 'HS256', 'typ': 'JWT'})
body = enc({'iss': 'https://access.line.me', 'sub': sub, 'aud': aud, 'iat': now, 'exp': now + 600, 'amr': ['linesso']})
sig = base64.urlsafe_b64encode(hmac.new(secret.encode(), head + b'.' + body, hashlib.sha256).digest()).rstrip(b'=')
print((head + b'.' + body + b'.' + sig).decode())
PY
}

# ---- dashboard: owner, app, channels, offers and a stamp card
req POST /v1/dashboard/auth/register "" "" "{\"email\":\"$EMAIL\",\"password\":\"$PASS\",\"tenant_name\":\"Coupon Cafe\",\"tenant_type\":\"brand\"}"
expect 201 "register owner"
TOKEN=$(printf '%s' "$BODY" | json '["access_token"]')
req GET /v1/dashboard/me "$TOKEN" ""; TID=$(printf '%s' "$BODY" | json '["memberships"][0]["tenant"]["id"]')
req POST "/v1/dashboard/tenants/$TID/apps" "$TOKEN" "$(uuidgen)" '{"name":"Coupon Cafe App"}'
expect 201 "create app"; AID=$(printf '%s' "$BODY" | json '["id"]')
req PUT "/v1/dashboard/apps/$AID/channels/developing" "$TOKEN" "$(uuidgen)" "{\"channel_id\":\"$MINI_CHANNEL\",\"liff_id\":\"$MINI_CHANNEL-dev\",\"channel_secret\":\"$MINI_SECRET\"}"
expect 200 "MINI App channel"
req PUT "/v1/dashboard/apps/$AID/channels/staff" "$TOKEN" "$(uuidgen)" "{\"channel_id\":\"$STAFF_CHANNEL\",\"liff_id\":\"$STAFF_CHANNEL-staff\",\"channel_secret\":\"$STAFF_SECRET\"}"
expect 200 "staff channel"
req PATCH "/v1/dashboard/apps/$AID" "$TOKEN" "$(uuidgen)" '{"allow_hs256_id_tokens":true}'
expect 200 "accept HS256 ID tokens (smoke only)"

req POST "/v1/dashboard/apps/$AID/coupon-templates" "$TOKEN" "$(uuidgen)" '{"name":"Free coffee","type":"free_item","claimable":true,"per_member_limit":1,"total_limit":100}'
expect 201 "coupon template"; OFFER=$(printf '%s' "$BODY" | json '["id"]')
req POST "/v1/dashboard/apps/$AID/coupon-templates" "$TOKEN" "$(uuidgen)" '{"name":"Free cake","type":"free_item"}'
expect 201 "reward template"; REWARD=$(printf '%s' "$BODY" | json '["id"]')
req POST "/v1/dashboard/apps/$AID/stamp-cards" "$TOKEN" "$(uuidgen)" "{\"name\":\"Coffee card\",\"stamps_required\":3,\"max_stamps_per_visit\":3,\"reward_coupon_template_id\":\"$REWARD\"}"
expect 201 "stamp card"; CARD=$(printf '%s' "$BODY" | json '["id"]')
req POST "/v1/dashboard/apps/$AID/staff" "$TOKEN" "$(uuidgen)" '{"display_name":"Cashier","role":"cashier"}'
expect 201 "staff member"; INVITE=$(printf '%s' "$BODY" | json '["invite_code"]')

# ---- sign-in: staff with the invite code, member on first login
req POST /v1/staff/auth/line "" "" "{\"app_id\":\"$AID\",\"id_token\":\"$(idtoken "$STAFF_CHANNEL" "$STAFF_SECRET" "$STAFF_SUB")\",\"invite_code\":\"$INVITE\"}"
expect 200 "staff sign-in"; STAFF=$(printf '%s' "$BODY" | json '["session_token"]')
req POST /v1/auth/line "" "" "{\"app_id\":\"$AID\",\"id_token\":\"$(idtoken "$STAFF_CHANNEL" "$STAFF_SECRET" "$MEMBER_SUB")\"}"
expect 401 "member sign-in refuses the staff channel"
req POST /v1/auth/line "" "" "{\"app_id\":\"$AID\",\"id_token\":\"$(idtoken "$MINI_CHANNEL" "$MINI_SECRET" "$MEMBER_SUB")\"}"
expect 200 "member sign-in"; MEMBER=$(printf '%s' "$BODY" | json '["session_token"]'); MID=$(printf '%s' "$BODY" | json '["member"]["id"]')

# ---- member claims the offer
req GET /v1/coupons/offers "$MEMBER" ""
expect 200 "offers"
printf '%s' "$BODY" | command grep -q "$OFFER" || fail "offer not listed: $BODY"
IDEM=$(uuidgen)
req POST "/v1/coupons/$OFFER/claim" "$MEMBER" "$IDEM" '{}'
expect 201 "claim"; COUPON=$(printf '%s' "$BODY" | json '["id"]')
req POST "/v1/coupons/$OFFER/claim" "$MEMBER" "$IDEM" '{}'
expect 201 "claim retry replays"
[ "$(printf '%s' "$BODY" | json '["id"]')" = "$COUPON" ] || fail "replay issued another coupon"
req POST "/v1/coupons/$OFFER/claim" "$MEMBER" "$(uuidgen)" '{}'
expect 409 "second claim over per_member_limit"

# ---- staff scans the member card and redeems
req GET /v1/me/card "$MEMBER" ""
expect 200 "member card"; QR=$(printf '%s' "$BODY" | json '["qr"]')
req POST /v1/staff/scan "$STAFF" "" "{\"qr\":\"$QR\"}"
expect 200 "staff scan"; SCAN=$(printf '%s' "$BODY" | json '["scan_token"]')
printf '%s' "$BODY" | command grep -q "$COUPON" || fail "claimed coupon missing from the scan: $BODY"
IDEM=$(uuidgen)
req POST "/v1/staff/coupons/$COUPON/redeem" "$STAFF" "$IDEM" "{\"scan_token\":\"$SCAN\"}"
expect 200 "redeem"
[ "$(printf '%s' "$BODY" | json '["status"]')" = redeemed ] || fail "redeemed coupon state: $BODY"
req POST "/v1/staff/coupons/$COUPON/redeem" "$STAFF" "$IDEM" "{\"scan_token\":\"$SCAN\"}"
expect 200 "redeem retry replays"
req POST "/v1/staff/coupons/$COUPON/redeem" "$STAFF" "$(uuidgen)" "{\"scan_token\":\"$SCAN\"}"
expect 409 "double redeem"
printf '%s' "$BODY" | command grep -q coupon_unavailable || fail "double redeem code: $BODY"

# ---- stamps to completion, the reward coupon, the visit cap
req POST /v1/staff/stamps "$STAFF" "$(uuidgen)" "{\"scan_token\":\"$SCAN\",\"stamp_card_id\":\"$CARD\",\"count\":3}"
expect 200 "three stamps complete the card"
[ "$(printf '%s' "$BODY" | json '["completed"]')" = 1 ] || fail "completed: $BODY"
[ "$(printf '%s' "$BODY" | json '["rewards"].__len__()')" = 1 ] || fail "reward coupon missing: $BODY"
REWARD_COUPON=$(printf '%s' "$BODY" | json '["rewards"][0]["id"]')
req POST /v1/staff/stamps "$STAFF" "$(uuidgen)" "{\"scan_token\":\"$SCAN\",\"stamp_card_id\":\"$CARD\",\"count\":1}"
expect 409 "fourth stamp in one visit"
printf '%s' "$BODY" | command grep -q stamp_visit_limit || fail "visit cap code: $BODY"
req GET /v1/me/coupons "$MEMBER" ""
expect 200 "member coupons"
printf '%s' "$BODY" | command grep -q "$REWARD_COUPON" || fail "reward coupon not in the member's list: $BODY"
req GET /v1/me/stamps "$MEMBER" ""
expect 200 "member stamps"

# ---- outbound events and the database view
read -r ISSUED REDEEMED COMPLETED < <(psql "$DB" -Atc "select count(*) filter (where type = 'coupon.issued'), count(*) filter (where type = 'coupon.redeemed'), count(*) filter (where type = 'stamp.completed') from outbound_events where app_id = '$AID'" | tr '|' ' ')
[ "$ISSUED" = 2 ] && [ "$REDEEMED" = 1 ] && [ "$COMPLETED" = 1 ] || fail "outbound events: issued $ISSUED, redeemed $REDEEMED, completed $COMPLETED"
echo "ok   outbound events: coupon.issued $ISSUED, coupon.redeemed $REDEEMED, stamp.completed $COMPLETED"
read -r COUNT COMPLETIONS EVENTS < <(psql "$DB" -Atc "select p.count, p.completions, (select coalesce(sum(delta), 0) from stamp_events e where e.app_id = p.app_id and e.member_id = p.member_id and e.stamp_card_id = p.stamp_card_id) from stamp_progress p where p.app_id = '$AID' and p.member_id = '$MID'" | tr '|' ' ')
[ "$COUNT" = 0 ] && [ "$COMPLETIONS" = 1 ] && [ "$EVENTS" = 0 ] || fail "stamp progress $COUNT, completions $COMPLETIONS, event sum $EVENTS"
echo "ok   stamp progress = event sum = $COUNT, completions $COMPLETIONS"
CLAIMED=$(psql "$DB" -Atc "select claimed_count from coupon_templates where id = '$OFFER'")
[ "$CLAIMED" = 1 ] || fail "claimed_count $CLAIMED"
echo "ok   claimed_count = 1"
echo "ALL PHASE 4 SMOKE CHECKS PASSED"
