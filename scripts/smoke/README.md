# Smoke tests

End-to-end checks against a running API and the development database. They need
`curl`, `jq`-free Python 3, `openssl`, `uuidgen` and `psql`.

```sh
make db-up migrate
LINE_SKIP_CREDENTIAL_VERIFICATION=true API_ADDR=:18080 go run ./cmd/api &
scripts/smoke/points.sh            # BASE and DB override the defaults
```

`points.sh` covers earn rules, POS earn and redeem through an API key, idempotent
retries, dashboard adjustments, history paging, scopes, and that the cached balance,
the ledger sum and the open lots agree in the database. Flows that need a real LINE
ID token (member sign-in, staff scan) are covered by the Go integration tests.
