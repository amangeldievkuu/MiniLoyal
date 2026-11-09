# MiniLoyal — Implementation Plan

> **Plan v2 — 9 November 2026** (supersedes the v1 draft of November 2026; see "Changes from v1" at the end)
> **Working name:** MiniLoyal (change later)
> **What it is:** A membership & loyalty backend for **LINE MINI Apps**: auth, member cards, points, coupons, stamp cards, push messages, admin dashboard, and SDK. Built in Go, sold to agencies and brands.
> **First market:** Taiwan → **then** Japan (partner-led) → Thailand
> **How it is built:** by one or two humans driving a Claude Code multi-agent harness (see §7)

---

## 1. Product summary

### Problem
LINE Taiwan announced on 28 Oct 2025 that the MINI App platform moves from an application-based to a fully open model, and since **11 Mar 2026** anyone who meets the LINE MINI App Policy can create a MINI App channel for services in Taiwan or Thailand (globally, unverified MINI Apps have been publishable without review since 28 Nov 2024). Brands and agencies in Taiwan can now build MINI Apps freely. But every project rebuilds the same backend from scratch:

- LINE Login / LIFF ID token verification
- Linking the user to the brand's LINE Official Account (OA)
- Member card with QR code
- Points, coupons, stamp cards
- Push messages and notifications
- An admin panel for the brand's staff

Agencies lose weeks per project on this. Generic tools (Firebase, Supabase) know nothing about LINE. LINE's own free OA features (stamp card, coupons) are not programmable and do not live inside a MINI App.

### Solution
A **multi-tenant backend with ready APIs + dashboard + JS SDK** that does all of the above. An agency can launch a loyalty MINI App in **days instead of weeks**. Positioning in one line: **"the Firebase for LINE MINI Apps"**, not a coupon tool.

### Target customers (in order)
1. **Agencies** that build LINE apps for brands (main channel, white-label)
2. **Brands with in-house developers** (retail, F&B, beauty, gyms, clinics)
3. **SaaS vendors** (POS, booking) that want to add LINE membership features

### Positioning
| | Generic BaaS (Firebase) | LINE OA native (free 集點卡 + coupons) | Taiwan loyalty SaaS (Ocard, Omnichat) | LINE CRM tools (e.g. MAAC) | **MiniLoyal** |
|---|---|---|---|---|---|
| Focus | Any app | Simple stamp card and coupons inside the OA | Marketer dashboard, member system on the OA | OA marketing messages | **MINI App backend** |
| LINE-native auth & OA linking | ❌ DIY | n/a (no custom app) | Partial | Partial | ✅ |
| Points / coupons / stamps APIs | ❌ DIY | ❌ no API | ❌ limited / none | Limited | ✅ |
| Custom MINI App UI | ✅ DIY | ❌ | Templates only | ❌ | ✅ via SDK |
| Developer-first (API + SDK + webhooks) | ✅ | ❌ | ❌ | ❌ (marketer UI) | ✅ |
| White-label for agencies | ❌ | ❌ | ❌ | ❌ | ✅ (Phase 2) |
| Data hosted in Taiwan | Optional | LINE-controlled | Varies | Varies | ✅ default |

> The free LINE OA stamp card and coupons set the floor: low-end brands will not pay for a stamp card alone. MiniLoyal sells programmability, custom UI, POS and webhook integration, and data ownership.

---

## 2. Feature list

### 2.1 MVP (Phases 0–7, about 6–8 weeks) — "one brand can run a loyalty program"

**Platform**
- [ ] Tenants (agency / brand) and apps (one MINI App = one app)
- [ ] App channels: the MINI App's three internal channels (Developing / Review / Published) with their channel IDs and LIFF IDs, plus the OA Messaging API channel
- [ ] Store each brand's LINE channel secrets (encrypted, envelope encryption); access tokens are minted on demand, never stored long-lived
- [ ] Team members & roles: Owner, Admin, Staff (`tenant_users`)
- [ ] Stores / branches (for staff redemption)

**LINE authentication**
- [ ] Verify LIFF ID token locally (ES256 via LINE JWKS) → create or find member → issue platform session
- [ ] Sync LINE profile (name, picture)
- [ ] Friend status with the brand's OA (`liff.getFriendship()` on the client + follow/unfollow webhooks on the server)
- [ ] Prompt to add OA as friend on first visit

**Membership**
- [ ] Member profile (+ custom fields: birthday, phone, gender)
- [ ] Digital member card with **rotating QR code** (30-second windows, screenshot dies within 60–90 s)
- [ ] Member registration form with versioned consent (`member_consents`)
- [ ] PDPA endpoints: export my data, delete my data (anonymize + hard-delete secrets)

**Points**
- [ ] Earn / redeem / manual adjust / expire
- [ ] Append-only points ledger + **points lots** (FIFO expiry) + idempotency keys with stored responses
- [ ] Simple earn rules (e.g. NT$100 spent = 1 point) in `points_rules`

**Coupons**
- [ ] Coupon templates (discount, free item, amount off)
- [ ] Issue to member / member claims / staff redeems
- [ ] Limits: total quantity, per-member limit, validity dates; `expires_at` stamped on each instance

**Stamp cards**
- [ ] N stamps → reward coupon
- [ ] Staff adds stamp by scanning member QR; every stamp is a `stamp_events` row

**Staff app (LIFF)**
- [ ] Cashier logs in → scans member QR (`liff.scanCodeV2()`, browser fallback) → add points / stamp / redeem coupon

**Messaging (minimal)**
- [ ] Push message to one member / a list (via brand's OA Messaging API, stateless channel access tokens)
- [ ] Message log and cost estimate (configurable Taiwan OA pricing)

**Developer experience**
- [ ] REST API + OpenAPI spec (authored as 3.0.3 for `oapi-codegen` compatibility) + docs site
- [ ] JS/TS SDK that wraps LIFF + our API
- [ ] Server API keys (for brand backends / POS), `ml_` prefix, hash only
- [ ] Outbound webhooks (member.created, points.changed, coupon.redeemed …), signed

**Admin dashboard**
- [ ] Members list, search, detail page
- [ ] Coupons / stamp cards CRUD
- [ ] Points adjustments
- [ ] Basic stats: members, new members/day, redemptions
- [ ] Languages: 繁體中文, English (i18n framework ready for ja/th; no JP/TH strings in MVP)

**Deferred from the v1 MVP:** service messages (need a verified MINI App), booking, segments, Japanese and Thai strings, a second "web" staff app.

### 2.2 Phase 2 of the business (Months 4–6) — "agencies can sell it"
- [ ] **Agency mode:** manage many brand clients, white-label dashboard (logo, domain)
- [ ] **Service messages** for verified MINI Apps (notifier token flow, reviewed templates)
- [ ] Member tiers (Silver / Gold / VIP) with auto upgrade/downgrade
- [ ] Segments (e.g. "inactive 30 days", "birthday this month")
- [ ] Automated campaigns: birthday coupon, welcome coupon, win-back message
- [ ] Referral program (invite friends via `shareTargetPicker`)
- [ ] Booking / reservation module (time slots, reminders)
- [ ] CSV import of existing members (from old POS/CRM)
- [ ] POS integration API (earn points from receipt)
- [ ] Analytics: retention, redemption rate, campaign results
- [ ] Billing & subscription plans (Stripe or local gateway)
- [ ] Full Japanese & Thai UI

### 2.3 Phase 3 of the business (Months 7–12) — "scale & expand"
- [ ] Japan launch through **one partner agency** (Japanese docs, JP data region option); the Japanese MINI App membership market is crowded (Yappli MembersCard, みせめぐMini, Lメンバーズカード, toypo, みせプリ, P+KACHI and more), so compete on developer tooling, not on another member card
- [ ] LINE MINI App in-app payments (`liff.iap`) **when available in Taiwan**; today it is Japan-only (Japan-based providers, users with a Japanese phone number, LIFF 2.26+)
- [ ] Template gallery (ready MINI App front-ends: café, salon, gym, retail)
- [ ] Connectors: Shopify, WooCommerce, popular Taiwan POS systems
- [ ] AI helpers: suggest segments, write campaign messages
- [ ] Apply for **LINE Technology Partner** program
- [ ] Self-hosted / private deployment option for enterprise; managed-cloud migration (Cloud Run + Cloud SQL)

---

## 3. Architecture

### 3.1 High-level

```
           ┌──────────────────────────────────────┐
           │              LINE App                 │
           │  ┌──────────────┐   ┌──────────────┐  │
           │  │ Brand MINI   │   │ Staff app    │  │
           │  │ App (LIFF)   │   │ (LIFF)       │  │
           │  └──────┬───────┘   └──────┬───────┘  │
           └─────────┼──────────────────┼──────────┘
                     │ JS SDK           │
                     ▼                  ▼
┌──────────────────────────────────────────────────────────┐
│                 MiniLoyal API (Go, net/http)              │
│  auth │ members │ points │ coupons │ stamps │ messaging   │
│  tenants │ api keys │ webhooks │ audit │ billing          │
└───────┬──────────────────────────────────┬───────────────┘
        │                                  │
        ▼                                  ▼
  ┌─────────────────────┐        ┌────────────────────┐
  │ PostgreSQL 17       │◄──────►│ Worker (Go, River) │
  │ main DB, RLS,       │  jobs  │ messages, expiry,  │
  │ job queue (River),  │        │ reconciliation,    │
  │ rate-limit counters │        │ outbound webhooks  │
  └─────────────────────┘        └─────────┬──────────┘
                                           │
                                           ▼
                            ┌──────────────────────────┐
                            │ LINE Platform APIs        │
                            │ JWKS │ Messaging │ LIFF   │
                            │ Service messages (P2)     │
                            └──────────────────────────┘

  Admin Dashboard (React SPA) ──► MiniLoyal API
  Brand backend / POS         ──► MiniLoyal API (API key)
  MiniLoyal                   ──► customer webhooks (signed)
  Everything runs in Docker Compose on one VM in GCP asia-east1 (Taiwan) behind nginx.
```

No Redis in the MVP: Postgres carries sessions, rate-limit counters and the River job queue.

### 3.2 Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | **Go 1.27** (`CGO_ENABLED=0 -trimpath` builds) | Team skill, fast, simple deploys |
| HTTP | `net/http` `ServeMux` with method patterns + `http.NewCrossOriginProtection`, graceful shutdown | Standard, no magic; matches the team's existing Go style |
| API contract | **OpenAPI, authored as 3.0.3** (for `oapi-codegen`), `oapi-codegen` strict server (`std-http`), `openapi-typescript` + `openapi-fetch` for TS clients | Spec is the shared contract between parallel agents |
| DB | **PostgreSQL 17** | Transactions for points ledger, RLS for tenants |
| DB access | `pgx` v5 + **`sqlc`** | Type-safe SQL, no ORM |
| Migrations | `goose` (embedded, run on container start) | Simple |
| Jobs | **River** (Postgres-backed job queue for Go) in `cmd/worker` | No extra infra, transactional jobs |
| Cache / rate limit | none in MVP (Postgres + in-process token buckets) | One fewer service; add Redis only on measured need |
| Dashboard auth | argon2id (dummy-hash timing defense), 15-min access JWT with `iatMs` vs `sessionsValidFrom`, 30-day rotating refresh tokens stored as SHA-256 hashes with family revocation, httpOnly cookies, throttling; TOTP via `pquerna/otp` | Ported from the team's taiwan-mandarin auth design |
| Member sessions | 1-hour JWT; the SDK re-exchanges the LIFF ID token on expiry | No refresh tokens for members |
| API keys | `ml_` prefix + SHA-256 hash + scopes | Team's personal-access-token pattern |
| JWT / JWKS | `lestrrat-go/jwx` v3 | LINE ES256 JWKS cache, our session tokens |
| LINE tokens | Stateless channel access tokens (v3, 15 min), cached with single in-flight refresh and renewal 60 s early | MINI App channels accept only stateless tokens |
| Secrets | Envelope encryption: AES-256-GCM DEKs wrapped by Cloud KMS; local KEK file in dev | Channel secrets never in plaintext at rest |
| LINE SDK | `github.com/line/line-bot-sdk-go` (v8) for Messaging API; small hand-written clients for JWKS, stateless tokens, service messages, LIFF server API | Official client where it exists |
| Dashboard / staff / demo | **React 19 + Vite + TypeScript**, TanStack Router + Query, Tailwind + shadcn/ui, i18next; staff app uses `@line/liff` | Big ecosystem; the demo doubles as an agency template |
| JS SDK | TypeScript, `tsup`, wraps `@line/liff`, types generated from OpenAPI, published as `@miniloyal/sdk` | Used inside MINI Apps |
| Docs | Scalar (API reference), Astro Starlight (guides, zh-TW + en) | Standard |
| Observability | `slog` JSON, OpenTelemetry traces and metrics, Grafana Cloud | Standard |
| Testing | `go test -race` + `testcontainers-go`, Vitest, Playwright, k6 | Correctness of the ledger is the product |
| Lint / security | gofmt, `go vet`, staticcheck, govulncheck, ESLint + Prettier, Smoke lockfile upload in CI | Team's CI conventions |
| Monorepo | Go module at root; npm workspaces (`web/*`, `sdk/*`, `apps/*`), Node 22; `Makefile` | npm and Makefile as in the team's repos |
| Hosting (MVP) | **One GCP Compute Engine VM in `asia-east1` (Taiwan)** running Docker Compose: nginx (TLS via certbot), `api`, `worker`, `postgres:17` on 127.0.0.1 with `synchronous_commit=on`, backup sidecar + **WAL archiving to a GCS bucket in asia-east1** for point-in-time recovery | Data stays in Taiwan (PDPA, customer trust); cheapest and familiar |
| Hosting (later) | Terraform for VM, GCS and KMS (Phase 7); Cloud Run + Cloud SQL when paying customers justify it | Staged |
| CI/CD | GitHub Actions: gofmt check, vet, staticcheck, govulncheck, `go test -race` with Postgres service, npm lint/test/build, Docker build, Smoke upload, Dependabot; deploy to staging VM over SSH on `main` | Simple |

### 3.3 Repository layout (monorepo)

```
miniloyal/
├── cmd/
│   ├── api/            # HTTP API server
│   ├── worker/         # background jobs (River)
│   └── migrate/        # DB migrations runner (goose)
├── internal/
│   ├── platform/       # config, db, logging, otel, crypto (envelope encryption)
│   ├── tenant/         # tenants, apps, app channels, team members, roles
│   ├── lineauth/       # LIFF ID token verify (JWKS), member sessions
│   ├── line/           # LINE API clients (stateless tokens, messaging, service msg, LIFF server API)
│   ├── member/         # members, profiles, consents, member card QR, PDPA export/delete
│   ├── points/         # ledger, lots, rules, expiry, reconciliation
│   ├── coupon/         # templates, instances, claim, redemption
│   ├── stamp/          # stamp cards, stamp events
│   ├── staff/          # staff auth, scan & redeem
│   ├── messaging/      # push, templates, logs, cost estimate
│   ├── webhook/        # inbound (LINE) + outbound (customers)
│   ├── apikey/         # server API keys & scopes
│   ├── idempotency/    # Idempotency-Key store (request hash + response)
│   ├── audit/          # audit log
│   └── billing/        # plans, usage (phase 2)
├── api/openapi.yaml    # OpenAPI 3.0.3 (source of truth for server stubs and TS types)
├── db/
│   ├── migrations/     # goose
│   └── queries/        # sqlc
├── web/
│   ├── dashboard/      # admin dashboard (React + Vite)
│   └── staff/          # staff scan LIFF app (React + Vite + @line/liff)
├── sdk/js/             # TypeScript SDK (@miniloyal/sdk)
├── apps/demo-cafe/     # demo café loyalty MINI App built on the SDK
├── deploy/             # Dockerfile, compose, nginx, backup/restore, Terraform (phase 7)
├── docs/               # plan, ADRs, LINE platform notes, developer docs site
└── .claude/            # agents, skills, rules, hooks, workflows (see §7)
```

---

## 4. Data model (core tables)

**Convention (stated once):** every row carries `tenant_id`; every member-, app- or channel-scoped row also carries `app_id`. Row-Level Security policies compare `tenant_id` with `current_setting('app.tenant_id')`, and application code still filters by `tenant_id`/`app_id` explicitly. Tests try cross-tenant access on every endpoint.

| Table | Key fields |
|---|---|
| `tenants` | id, name, type (agency/brand), plan, parent_tenant_id (agency → brand) |
| `apps` | id, tenant_id, name, oa_channel_id, **encrypted** oa_channel_secret, locale, provider_id (for the same-provider check) |
| `app_channels` | id, tenant_id, app_id, environment (developing/review/published), channel_id, liff_id, **encrypted** channel_secret |
| `users` (dashboard) | id, email, password_hash (argon2id), totp_secret (encrypted), sessions_valid_from |
| `tenant_users` | tenant_id, user_id, role (owner/admin/staff) |
| `refresh_tokens` | id, user_id, token_hash, family_id, parent_id, expires_at, revoked_at |
| `stores` | id, tenant_id, app_id, name, address |
| `staff` | id, tenant_id, app_id, store_id, line_user_id or login, role |
| `members` | id, tenant_id, app_id, line_user_id, display_name, picture_url, is_friend, tier_id, custom_fields (jsonb), anonymized_at |
| `member_consents` | id, member_id, consent_version, text_hash, consented_at |
| `member_qr_secrets` | member_id, secret (encrypted), rotated_at |
| `points_accounts` | member_id, balance (cached), version |
| `points_lots` | id, tenant_id, app_id, member_id, earned, remaining, expires_at, created_at |
| `points_ledger` | id, tenant_id, app_id, member_id, lot_id, delta, reason, ref_type, ref_id, idempotency_key, created_at |
| `points_rules` | id, app_id, type (spend_per_point/fixed), amount, points, active |
| `idempotency_keys` | app_id, key, request_hash, response_status, response_body, created_at |
| `coupon_templates` | id, tenant_id, app_id, type, value, total_limit, per_member_limit, valid_from, valid_to, validity_days, claimed_count |
| `coupon_instances` | id, tenant_id, app_id, template_id, member_id, status (issued/redeemed/expired), code, **expires_at**, redeemed_by_staff_id, redeemed_at_store_id, redeemed_at |
| `stamp_cards` | id, tenant_id, app_id, stamps_required, reward_coupon_template_id |
| `stamp_progress` | member_id, stamp_card_id, count, completed_at |
| `stamp_events` | id, app_id, member_id, stamp_card_id, delta, staff_id, store_id, created_at |
| `messages` | id, tenant_id, app_id, member_id, type (push/service), payload, status, estimated_cost, sent_at |
| `service_notifier_tokens` (phase 2) | member_id, token (encrypted), remaining_count, expires_at |
| `api_keys` | id, tenant_id, app_id, prefix, hash, scopes, last_used_at |
| `webhook_endpoints` | id, tenant_id, app_id, url, secret (encrypted), events[] |
| `webhook_deliveries` | id, endpoint_id, event, status, attempts, next_retry_at |
| `webhook_inbound_events` | app_id, webhook_event_id, received_at (dedupe of LINE redeliveries) |
| `audit_log` | id, tenant_id, actor, action, target, metadata, created_at |

> ⚠️ **Important LINE rule:** a LINE user gets a **different user ID per provider**. `line_user_id` is unique only **per app**: unique index `(app_id, line_user_id)`. The MINI App channel and the OA's Messaging API channel **must be under the same provider**, or the login `sub` will never match the webhook and push user IDs. Onboarding runs a same-provider check and the checklist says so.

> **Erasure:** a PDPA delete anonymizes PII in place (`anonymized_at`), keeps ledger rows for integrity, and hard-deletes QR secrets, consents and message payloads. The audit log records the erasure.

---

## 5. Key implementation details

### 5.1 Member login flow (LIFF → MiniLoyal session)

```
MINI App (LIFF)                  MiniLoyal API                         LINE
     │ liff.init()                    │                                 │
     │ idToken = liff.getIDToken()    │                                 │
     │── POST /v1/auth/line ─────────►│                                 │
     │   { app_id, id_token,          │ verify ES256 locally with cached│
     │     liff_access_token? }       │ JWKS (iss, aud, exp, nonce)     │
     │                                │── (fallback only) POST /oauth2/v2.1/verify ►
     │                                │ upsert member (app_id, sub)     │
     │◄─ { session_token (1h), member }│                                 │
     │ (SDK stores token; on 401 it   │                                 │
     │  re-exchanges a fresh ID token)│                                 │
```

LIFF-issued ID tokens are **ES256** with a `kid` header. Verify them locally against LINE's JWKS; keep LINE's verify endpoint as a fallback (web-login tokens are HS256 with the channel secret). Accept any of the app's configured channel IDs as `aud`, because the Developing, Review and Published channels each run their own LIFF ID.

Go sketch:

```go
// Verifier verifies LINE ID tokens issued to one app's channels.
type Verifier struct {
    jwks jwk.Cache // github.com/lestrrat-go/jwx/v3, refreshes https://api.line.me/oauth2/v2.1/certs
    http *http.Client
}

func (v *Verifier) Verify(ctx context.Context, idToken string, channelIDs []string, nonce string) (*LineClaims, error) {
    set, err := v.jwks.Lookup(ctx, "https://api.line.me/oauth2/v2.1/certs")
    if err != nil {
        return v.verifyRemote(ctx, idToken, channelIDs) // fallback: POST /oauth2/v2.1/verify
    }
    tok, err := jwt.Parse([]byte(idToken),
        jwt.WithKeySet(set),                         // ES256, matched by kid
        jwt.WithIssuer("https://access.line.me"),
        jwt.WithAcceptableSkew(30*time.Second),      // exp / iat checked by default
    )
    if err != nil {
        return nil, ErrInvalidIDToken
    }
    if !audienceMatches(tok, channelIDs) {
        return nil, ErrInvalidIDToken
    }
    if nonce != "" && claimString(tok, "nonce") != nonce {
        return nil, ErrInvalidIDToken
    }
    return claimsFrom(tok), nil // sub, name, picture, exp, aud
}
```

**Member session:** 1-hour JWT scoped to `{app_id, member_id}`, signing keys rotated with `kid` in the header. No refresh tokens for members; the SDK re-exchanges the LIFF ID token. **Dashboard users** use the ported design: 15-minute access JWT, 30-day rotating refresh tokens (hashed, family revocation, httpOnly cookie), `sessionsValidFrom` to log out everywhere, TOTP.

If the client sends `liff_access_token`, the API can mint a service notification token for later service messages (Phase 2, verified MINI Apps only).

### 5.2 Inbound LINE webhooks (follow / unfollow)

```go
func validLineSignature(channelSecret string, body []byte, signature string) bool {
    mac := hmac.New(sha256.New, []byte(channelSecret))
    mac.Write(body)
    expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    app := h.appByID(r.PathValue("app_id"))
    body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
    if !validLineSignature(app.OAChannelSecret, body, r.Header.Get("X-Line-Signature")) {
        http.Error(w, "bad signature", http.StatusUnauthorized)
        return
    }
    var req struct {
        Events []struct {
            Type            string `json:"type"`
            WebhookEventID  string `json:"webhookEventId"`
            DeliveryContext struct{ IsRedelivery bool `json:"isRedelivery"` } `json:"deliveryContext"`
            Source          struct{ UserID string `json:"userId"` } `json:"source"`
        } `json:"events"`
    }
    _ = json.Unmarshal(body, &req)
    for _, ev := range req.Events {
        // INSERT ... ON CONFLICT DO NOTHING on (app_id, webhook_event_id); skip when already seen.
        if !h.store.FirstSeen(r.Context(), app.ID, ev.WebhookEventID) {
            continue // redelivery of an event we already processed
        }
        h.jobs.Enqueue(r.Context(), ProcessLineEvent{AppID: app.ID, Event: ev, Redelivery: ev.DeliveryContext.IsRedelivery})
    }
    w.WriteHeader(http.StatusOK) // within 1 s; work happens in the worker
}
```

- Header: `X-Line-Signature`; the OA Messaging API channel's secret signs the body.
- Route per app: `POST /webhooks/line/{app_id}` → load that app's OA channel secret → verify → dedupe on `webhookEventId` → enqueue → return 200 fast. Bad signature → 401.
- `follow` → `is_friend = true`, maybe send welcome coupon. `unfollow` → `is_friend = false`.

### 5.3 Points ledger and lots (no double-spend, no lost points, FIFO expiry)

```sql
CREATE TABLE points_lots (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   UUID        NOT NULL,
  app_id      UUID        NOT NULL,
  member_id   UUID        NOT NULL,
  earned      INTEGER     NOT NULL CHECK (earned > 0),
  remaining   INTEGER     NOT NULL CHECK (remaining >= 0),
  expires_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE points_ledger (
  id               BIGSERIAL PRIMARY KEY,
  tenant_id        UUID        NOT NULL,
  app_id           UUID        NOT NULL,
  member_id        UUID        NOT NULL,
  lot_id           BIGINT      REFERENCES points_lots(id),
  delta            INTEGER     NOT NULL,        -- +earn / -redeem / -expire
  reason           TEXT        NOT NULL,        -- 'purchase','redeem','adjust','expire'
  ref_type         TEXT,
  ref_id           TEXT,
  idempotency_key  TEXT        NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (app_id, idempotency_key)
);

CREATE TABLE idempotency_keys (
  app_id           UUID        NOT NULL,
  key              TEXT        NOT NULL,
  request_hash     BYTEA       NOT NULL,        -- SHA-256 of method + path + body
  response_status  INTEGER     NOT NULL,
  response_body    JSONB       NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, key)
);
```

Rules:
1. Every change happens in **one DB transaction**: lock `points_accounts` row (`SELECT … FOR UPDATE`) → check balance → consume lots FIFO (`ORDER BY expires_at NULLS LAST, created_at`) or create a lot → insert ledger rows (one per lot touched) → update cached balance.
2. Every write API requires an `Idempotency-Key` header. The first call stores the request hash and the response; a retry with the same key and hash returns the stored response; the same key with a different hash returns 422.
3. Ledger is **append-only**. Corrections = new rows, never updates/deletes.
4. Nightly job: expire lots whose `expires_at` passed, writing `expire` rows for `remaining` and setting `remaining = 0`.
5. Daily reconciliation job: `SUM(delta)` per member must equal the cached balance and `SUM(remaining)` → alert if not.

### 5.4 Rotating member QR code (anti-screenshot)

- Each member has a rotatable secret. QR payload = `version | app_id | member_id | time_window | HMAC(secret, version|app_id|member_id|time_window)` with 30-second windows (like TOTP).
- Staff app scans → API verifies the HMAC, accepts current ±1 window, and checks the member belongs to the staff's app.
- A screenshot stops working after 60–90 seconds.

### 5.5 Coupon redemption (race-safe)

```sql
UPDATE coupon_instances
SET status = 'redeemed',
    redeemed_at = now(),
    redeemed_by_staff_id = $3,
    redeemed_at_store_id = $4
WHERE id = $1
  AND app_id = $2
  AND status = 'issued'
  AND (expires_at IS NULL OR expires_at > now())
RETURNING id;
```

`expires_at` is stamped at issue time from the template's `valid_to` or `issued_at + validity_days`. If 0 rows → already redeemed, expired or wrong app. One atomic statement = no double redemption.

Claims respect limits under the template row lock: `SELECT … FOR UPDATE` on the template, check `claimed_count < total_limit` and the member's count `< per_member_limit`, insert the instance, increment `claimed_count`, all in one transaction with an `Idempotency-Key`.

### 5.6 Messaging

- **Push** through the brand's OA Messaging API with **stateless channel access tokens** (minted from channel ID + secret, 15-minute lifetime, cached with a single in-flight refresh). The brand pays LINE message fees; the dashboard shows the estimated cost before sending. LINE revised Taiwan OA pricing on **1 Nov 2026** (mid plan NT$1,000/month, high plan NT$1,400/month, free quotas 200 / 3,000 / 6,000 unchanged, add-on messages tiered at about NT$0.2 then NT$0.15), so pricing lives in a config table with an effective date.
- **Service messages** (Phase 2, **verified MINI Apps only**): templates (max 20 per channel) must pass LY Corporation review; a service notification token is minted from the user's LIFF access token (one per LIFF access token, bound to one user, valid 1 year, renewed on every send, send count set at review). Never use the Developing channel's token for Review or Published. In Taiwan and Thailand, verification review is open only to channels under a certified provider.
- All sends go through the **worker queue** with rate limiting per channel, retries with backoff, and a message log.

### 5.7 Outbound webhooks to customers

- Events: `member.created`, `member.updated`, `friend.added`, `friend.removed`, `points.changed`, `coupon.issued`, `coupon.redeemed`, `stamp.completed`.
- Signed with HMAC-SHA256 (`MiniLoyal-Signature: t=timestamp,v1=signature`).
- Retries: exponential backoff up to 24h, dashboard shows delivery history + "resend".

### 5.8 Security & compliance

- **Taiwan PDPA:** amendments passed on 17 Oct 2025 and were promulgated on 11 Nov 2025; the effective date is set by the Executive Yuan and the new Personal Data Protection Commission is being established with a six-year transition. Breach notification to subjects and regulator becomes mandatory; the 72-hour deadline currently comes from MODA rules for digital-economy sectors and the Commission can set the final limit. We design for 72 hours: consent recorded at registration (versioned), member data export & delete endpoints, data stored in Taiwan, breach-response runbook.
- LINE channel secrets: encrypted at rest (envelope encryption, AES-256-GCM data keys wrapped by Cloud KMS). Access tokens are minted on demand, never stored long-lived.
- API keys: show once, store only hash, prefix for lookup, scopes per key.
- Tenant isolation: Postgres RLS + `tenant_id`/`app_id` in every query + tests that try cross-tenant access.
- Rate limits per API key, per member, per IP (Postgres counters, in-process token buckets).
- Audit log for every admin/staff action (points adjust, coupon redeem, data export, erasure).
- Dashboard login: email + password (argon2id) with 2FA (TOTP), rotating refresh tokens, SSO later.
- Supply chain: govulncheck, Dependabot, Smoke lockfile inventory on every push to `main`.

### 5.9 Non-functional targets

| Metric | Target |
|---|---|
| API p95 latency | < 150 ms (excluding LINE API calls) |
| Uptime | 99.9% |
| Points / coupon correctness | 0 lost or double-counted transactions (reconciliation alerts) |
| Webhook processing | LINE webhook acked < 1s |
| Data residency | Taiwan region by default |
| Recovery | daily dumps + WAL archiving to GCS, restore drill before pilot |

---

## 6. API overview (v1)

Every write endpoint requires an `Idempotency-Key` header.

**Member-facing (session token from LIFF login)**

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/auth/line` | Exchange LIFF ID token for a 1-hour session (`liff_access_token` optional) |
| GET | `/v1/me` | Member profile + tier + friend status |
| PATCH | `/v1/me` | Update profile / custom fields |
| POST | `/v1/me/consent` | Record consent to a consent version |
| GET | `/v1/me/card` | Rotating QR payload |
| GET | `/v1/me/points` | Balance + history |
| GET | `/v1/me/coupons` | My coupons |
| POST | `/v1/coupons/{template_id}/claim` | Claim a coupon (Idempotency-Key) |
| GET | `/v1/me/stamps` | Stamp card progress |
| GET | `/v1/me/export` | PDPA: export my data |
| DELETE | `/v1/me` | PDPA: erase my data |

**Staff (staff session)**

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/staff/scan` | Decode member QR → member summary |
| POST | `/v1/staff/points` | Add points for a purchase (Idempotency-Key) |
| POST | `/v1/staff/stamps` | Add stamp (Idempotency-Key) |
| POST | `/v1/staff/coupons/{instance_id}/redeem` | Redeem coupon (Idempotency-Key) |

**Server (API key)**

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/members` | List / search members |
| GET | `/v1/members/{id}/export` | PDPA export for a member |
| DELETE | `/v1/members/{id}` | PDPA erasure for a member |
| POST | `/v1/members/{id}/points` | Earn / adjust (Idempotency-Key) |
| POST | `/v1/members/{id}/coupons` | Issue coupon (Idempotency-Key) |
| POST | `/v1/messages/push` | Push message to members (Idempotency-Key) |
| CRUD | `/v1/coupon-templates`, `/v1/stamp-cards`, `/v1/webhook-endpoints`, `/v1/stores`, `/v1/staff` | Configuration |

**Inbound**

| Method | Path | Purpose |
|---|---|---|
| POST | `/webhooks/line/{app_id}` | LINE Messaging API webhook (signature, dedupe, enqueue) |

**JS SDK example (inside the MINI App)**

```ts
import { MiniLoyal } from "@miniloyal/sdk";

const ml = await MiniLoyal.init({ appId: "app_123", liffId: "1234567890-AbCdEfGh" });
// handles liff.init(), login, token exchange, and re-exchange when the session expires

const me = await ml.me();
const card = await ml.card();        // { qr: "...", refreshInSec: 30 }
const coupons = await ml.coupons.list();
await ml.coupons.claim("tmpl_welcome");
```

---

## 7. Team & timeline

### How it is built
One or two humans (product, LINE accounts, pilots, review) drive a **Claude Code multi-agent harness** committed in the repo. The harness lives in `CLAUDE.md`, `.claude/rules/`, `.claude/agents/`, `.claude/skills/`, `.claude/hooks/`, `.claude/workflows/` and `.mcp.json`. The OpenAPI document is the contract that lets agents work in parallel in git worktrees; an integrator agent is the only one that merges to `main`.

### Agent roster
| Agent | Scope | Notes |
|---|---|---|
| `backend-go` | handlers, services, River jobs | worktree isolation for feature work |
| `db-migrations` | goose migrations, sqlc queries, RLS policies | regenerates code after every change |
| `line-integration` | ID token verify, webhooks, Messaging API, service messages, LIFF server API | cites developers.line.biz for every endpoint |
| `frontend-dashboard` | `web/dashboard` | consumes generated OpenAPI types only |
| `staff-app` | `web/staff` LIFF app | |
| `sdk-ts` | `sdk/js` + `apps/demo-cafe` | |
| `test-engineer` | integration, cross-tenant, idempotency, webhook fixtures | |
| `security-reviewer` | read-only review | tenant isolation, secrets, OWASP, PDPA checklist |
| `docs-writer` | OpenAPI descriptions, guides | |
| `integrator` | merges worktree branches, runs full CI, resolves conflicts | only agent that touches `main` |

Hooks run gofmt, `go vet`, `sqlc generate`, Prettier and ESLint after edits, block destructive shell commands, and run fast tests on stop. Skills package the recipes: add an endpoint spec-first, add a migration with RLS and a cross-tenant test, the LINE API reference card, verify, release.

### 6–8 week MVP plan (7 phases, checkpoint after each)

| Phase | Deliverables | Exit criteria |
|---|---|---|
| 0 | Plan v2, Claude harness, repo scaffold (Makefile, compose, Dockerfile, CI, OpenAPI skeleton, npm workspaces) | `make ci` green; agents and hooks load |
| 1 | Tenants, apps + channels, encrypted secrets, dashboard users + roles + TOTP + refresh tokens, stores, staff, API keys, audit log, RLS, dashboard login and app settings | cross-tenant tests pass; login with TOTP works end to end. **Checkpoint** |
| 2 | LIFF ID token verify (JWKS), member upsert, sessions, profile sync, inbound webhooks with dedupe, friend status, SDK `init()`/`me()` | real Developing-channel login succeeds |
| 3 | Points accounts, lots, ledger, idempotency store, rules, expiry and reconciliation jobs, rotating QR, staff scan API, staff LIFF app | 1,000 concurrent redeems never go negative; QR screenshot fails after 90 s |
| 4 | Coupons (templates, claim limits, atomic redeem), stamps + events, dashboard CRUD, outbound events | double redeem returns one success |
| 5 | Push via Messaging API, cost estimate, message log, signed outbound webhooks with retries, API key scopes and limits | webhook signature and retry tests pass; test push arrives |
| 6 | SDK complete, Scalar reference, guides (zh-TW, en), demo café app, dashboard i18n | a new developer gets a member card working in under an hour |
| 7 | Security review, k6 load test, Terraform for the asia-east1 VM + GCS + KMS, TLS, backup and PITR drill, staging deploy, PDPA runbooks, pilot onboarding checklist | staging live in Taiwan; first pilot app onboarded |

The bottleneck is not code: it is LINE account setup, real-device testing and pilot feedback. Agency interviews run during Phases 0–2 and gate the Phase 2 business scope.

### Months 4–6
Agency mode, service messages for verified apps, tiers, segments & campaigns, booking module, CSV import, billing, Japanese/Thai UI.

### Months 7–12
Japan launch through one partner agency, templates gallery, connectors, LINE Technology Partner application, AI helpers, managed-cloud migration.

---

## 8. Pricing (draft)

| Plan | Price / month | Limits |
|---|---|---|
| Free | NT$0 | 1 app, 300 members, MiniLoyal branding |
| Starter | NT$1,990 | 1 app, 5,000 members |
| Pro | NT$5,990 | 1 app, 50,000 members, webhooks, campaigns |
| Agency | from NT$14,900 | Many client apps, white-label, priority support |
| Enterprise | Custom | Private deployment, SLA |

LINE message fees are paid by the brand directly to LINE (not resold). Reference points: the free LINE OA stamp card sets the floor, Ocard and MAAC (from about NT$3,000/month) set the ceiling for SMB tools.

---

## 9. Go-to-market

1. **While building (Phases 0–2):** interview 5–10 Taiwanese agencies that build LINE apps. Ask: "What do you rebuild on every LINE project? How many days does it take?" Their answers gate the Phase 2 business scope, not the MVP build.
2. **Pilot program:** 3 agencies get free Pro/Agency plan for 6 months in exchange for feedback + a case study.
3. **Content:** Traditional Chinese tutorials ("Build a LINE MINI App membership card in 1 day"), open-source the JS SDK and the demo MINI App.
4. **Community:** LINE developer meetups in Taipei, LINE Developers community, Taiwan Go/JS meetups.
5. **Partnership:** apply to LINE's partner program once you have paying customers and case studies; a certified provider is also what unlocks verification review in Taiwan.
6. **Japan:** after 20+ paying customers in Taiwan, translate docs, find 1 Japanese agency partner. Do not enter Japan with "another member card".

---

## 10. Success metrics

| Stage | Metric | Target |
|---|---|---|
| Month 3 | Pilot agencies live | 3 |
| Month 6 | Paying apps | 15 |
| Month 6 | Members managed | 50,000 |
| Month 12 | Paying apps | 80+ |
| Month 12 | MRR | NT$400,000+ |
| Always | Monthly churn | < 3% |
| Always | Points/coupon incidents | 0 |

---

## 11. Risks & mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| **LINE builds the same features** (the OA already has a free stamp card and coupons) | High | Go deeper than LINE will (POS connectors, agency tools, analytics, custom UI); become a LINE partner instead of a competitor |
| Taiwan MINI App adoption grows slowly (about 100 brands in late 2025) | High | Sell through agencies, who multiply brands; track the number of Taiwanese MINI Apps quarterly; **if no clear growth by mid-2027, move focus to Japan** |
| Ocard, Omnichat or Crescendo Lab add MINI App backend features | Medium | Developer-first product + agency white-label, which marketer-focused tools do not do well |
| Japan vendor crowding (Yappli, みせめぐMini, Lメンバーズカード, toypo, others) | Medium | Enter Japan only through a partner agency, with developer tooling, after Taiwan traction |
| Verification gating: service messages and verification review need a certified provider in Taiwan | Medium | Keep service messages out of the MVP; apply for partner status early; design around push + LIFF |
| Developers choose Firebase + DIY | Medium | Make LINE-specific work 10× faster: one SDK call for login + OA link + member card |
| Points/coupon bugs lose money for brands | High | Lots + ledger design, idempotency with stored responses, reconciliation jobs, concurrency tests |
| PDPA / data breach | High | Encryption, Taiwan region, audit logs, 72-hour breach runbook, minimal data collection |
| Single-VM hosting outage | Medium | Daily dumps + WAL archiving to GCS, restore drill, documented move to Cloud Run + Cloud SQL |
| LINE API changes | Medium | Isolate LINE code in `internal/line`, contract tests, follow LINE developer news |

---

## 12. Open questions to validate early

- [ ] How many MINI Apps are launching in Taiwan each month since channel creation opened (11 Mar 2026)?
- [ ] Do agencies prefer white-label (their brand) or co-branded?
- [ ] Which use case is most demanded: membership card, coupons, booking, or mobile order?
- [ ] Which POS systems do Taiwanese SMB brands use most (for connector priority)?
- [ ] Do the three internal MINI App channels share one channel ID, or does each have its own? (verify in the LINE Developers Console; the design accepts a set of `aud` values either way)
- [ ] When do in-app payments (`liff.iap`) open in Taiwan, and on what terms?
- [ ] Requirements and timeline to become a certified provider / LINE Technology Partner in Taiwan
- [ ] Effective date of the PDPA amendments and the Commission's final breach-notification deadline

---

## 13. References

**LINE platform**
- LINE MINI App docs: https://developers.line.biz/en/docs/line-mini-app/
- LINE Developers news, 2026-03-11: MINI App channel creation open for Taiwan and Thailand services: https://developers.line.biz/en/news/
- LINE Developers news 2024 (unverified MINI Apps publishable without review from 2024-11-28): https://developers.line.biz/en/news/2024
- Console guide (three internal channels, LIFF IDs): https://developers.line.biz/en/docs/line-mini-app/discover/console-guide/
- Custom features (verified vs unverified): https://developers.line.biz/en/docs/line-mini-app/discover/custom-features
- Submission guide (certified provider rule for TW/TH): https://developers.line.biz/en/docs/line-mini-app/submit/submission-guide/
- Sending service messages: https://developers.line.biz/en/docs/line-mini-app/develop/service-messages/
- LINE MINI App API reference (service notification tokens): https://developers.line.biz/en/reference/line-mini-app/
- Verify ID token (ES256 / HS256, JWKS): https://developers.line.biz/en/docs/line-login/verify-id-token/
- LINE Login v2.1 API reference: https://developers.line.biz/en/reference/line-login/
- Stateless channel access tokens: https://developers.line.biz/en/reference/messaging-api/#issue-stateless-channel-access-token
- Messaging API webhooks (signature, redelivery): https://developers.line.biz/en/reference/messaging-api/#webhooks
- LIFF docs (`scanCodeV2`, `getFriendship`): https://developers.line.biz/en/docs/liff/
- In-app purchase overview (Japan-only today): https://developers.line.biz/en/docs/line-mini-app/in-app-purchase/overview/
- LINE Bot SDK for Go: https://github.com/line/line-bot-sdk-go

**Market and pricing**
- LINE Plus press release, 2025-10-28 (MINI App fully open model, in-app payment and ad revenue share): https://www.linepluscorp.com/pr/news/global/20251028/
- LY Corp, LINE CONVERGE 2025 report (100+ brands on MINI Apps in Taiwan, MINI HOME, LINE Touch): https://www.lycorp.co.jp/en/story/20260218/taiwan_converge2025.html
- LINE Biz-Solutions, 2026 OA price plan change (effective 2026-11-01): https://tw.linebiz.com/column/LINEOA-2026-Price-Plan/

**Law**
- K&L Gates, New Developments in the Taiwan Personal Data Protection Act (2026-01-13): https://www.klgates.com/New-Developments-in-the-Taiwan-Personal-Data-Protection-Act-1-13-2026
- Baker McKenzie, Taiwan amendment to the Personal Data Protection Act (2025-10): https://www.bakermckenzie.com/en/insight/publications/alerts/2025/10/taiwan-amendment-to-personal-data-protection-act

**Tooling**
- Go release history: https://go.dev/doc/devel/release
- oapi-codegen: https://github.com/oapi-codegen/oapi-codegen
- sqlc: https://sqlc.dev · goose: https://github.com/pressly/goose · River: https://riverqueue.com

> Always confirm exact endpoint names, limits, and fees on LINE Developers before implementing; LINE updates its APIs and pricing regularly. `docs/line/platform-notes.md` holds the verified facts with dates.

---

## Changes from v1

1. §1, §11: precise MINI App opening timeline (2024-11-28 global, 2025-10-28 Taiwan announcement, 2026-03-11 TW/TH channel creation); certified-provider rule for verification.
2. §5.6, §2.1: service messages get their token flow and gating; moved out of the MVP.
3. §5.1: ID tokens verified locally (ES256, JWKS) from day one; verify endpoint is the fallback.
4. §4: `app_channels` for the three internal channels; channel secrets stored, tokens minted on demand (stateless).
5. §5.6: Taiwan OA pricing figures confirmed and made configurable.
6. §5.8: PDPA status and 72-hour design target corrected.
7. §2.3: `liff.iap` marked Japan-only for now.
8. §1: positioning table gains LINE OA native and Taiwan loyalty SaaS columns.
9. §3.2: Go 1.27 pinned.
10. §2.1: staff app uses `liff.scanCodeV2()` with a browser fallback.
11. §5.5: coupon redeem uses `coupon_instances.expires_at`, scoped by `app_id`, records store and staff.
12. §5.3: `points_lots` for FIFO expiry; idempotency scoped to `app_id` with stored request hash and response.
13. §4: tenant_id/app_id convention stated once; `stamp_events`, `tenant_users`, `refresh_tokens`, `points_rules`, `member_consents`, `webhook_inbound_events`, `idempotency_keys` added.
14. §4: erasure = anonymize + hard-delete secrets, no soft delete.
15. §5.4: QR validity stated as 60–90 s; version byte and `app_id` in the HMAC; app-membership check.
16. §5.2: webhook redelivery handling and `webhookEventId` dedupe; 401 on bad signature.
17. §6: PDPA endpoints, consent endpoint, `Idempotency-Key` on staff and claim endpoints, optional `liff_access_token`.
18. §5.1: member sessions are 1-hour JWTs with ID-token re-exchange; dashboard users keep refresh tokens.
19. §3.2: Redis removed from the MVP.
20. §3.2: River worker runs as its own compose service.
21. §4: same-provider rule and onboarding check.
22. §2.1: MVP cut (no service messages, booking, segments, JP/TH strings, second staff app).
23. §7: 4-human team replaced by the Claude Code multi-agent harness and a 7-phase, 6–8 week schedule.
24. §9: agency interviews run during the build and gate Phase 2 scope.
25. §13: references refreshed with dated sources; OpenAPI noted as 3.0.3 for `oapi-codegen`.
