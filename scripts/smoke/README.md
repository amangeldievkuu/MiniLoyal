# Smoke tests

End-to-end checks against a running API and the development database. They need
`curl`, `jq`-free Python 3, `openssl`, `uuidgen` and `psql`.

```sh
make db-up migrate
LINE_SKIP_CREDENTIAL_VERIFICATION=true API_ADDR=:18080 go run ./cmd/api &
scripts/smoke/points.sh            # BASE and DB override the defaults
scripts/smoke/coupons.sh
```

`make db-up` needs `POSTGRES_PASSWORD`, `APP_DB_PASSWORD` and `WORKER_DB_PASSWORD` in `.env`
or the environment (`.env.example` sets all three). To keep the scripts' data out of your
development database, create another database, run `go run ./cmd/migrate up` with
`MIGRATE_DATABASE_URL` pointing at it, start the API with `DATABASE_URL` pointing at it as
`miniloyal_api`, and pass the owner URL as `DB`.

`points.sh` covers earn rules, POS earn and redeem through an API key, idempotent
retries, dashboard adjustments, history paging, scopes, and that the cached balance,
the ledger sum and the open lots agree in the database.

`coupons.sh` covers Phase 4: a coupon template and a stamp card with a reward from the
dashboard, staff and member sign-in, a member claiming the offer (a retry replays, a second
claim is 409), a staff scan, a redeem (a retry replays, a second redeem is 409
`coupon_unavailable`), three stamps completing the card and issuing the reward coupon, the
30-minute visit cap (409 `stamp_visit_limit`), and in the database the `coupon.issued`,
`coupon.redeemed` and `stamp.completed` outbound events, stamp progress against its events and
the template's `claimed_count`.

Member and staff sign-in need a LINE ID token. `coupons.sh` configures the app's MINI App and
staff channels with secrets it chooses, turns on `allow_hs256_id_tokens` for that app and signs
HS256 ID tokens itself, so it needs no LINE account. That works only with
`LINE_SKIP_CREDENTIAL_VERIFICATION=true`, which the API refuses outside development. Real
MINI Apps get ES256 tokens from LIFF and keep `allow_hs256_id_tokens` off.
