# LINE platform notes (verified 9 November 2026)

Facts the design depends on, with their sources. Anything we could not confirm is marked
**verify in console**. Re-check before implementing: LINE changes APIs, limits and prices often.

## 1. MINI App opening timeline and Taiwan / Thailand rules

- Since **28 Nov 2024**, anyone who can create a MINI App channel can publish an **unverified**
  MINI App without review. Verified status still requires LINE's review.
  Source: https://developers.line.biz/en/news/2024
- At that time, channel creation for services in Taiwan or Thailand required approval from
  LINE's local subsidiary. The getting-started page may still say so.
  Source: https://developers.line.biz/en/docs/line-mini-app/develop/develop-overview/
- **28 Oct 2025**: LINE Taiwan announced that the MINI App platform moves from an
  application-based to a fully open model in Q4 2025, with in-app payment and in-app advertising
  revenue sharing to follow. Source: https://www.linepluscorp.com/pr/news/global/20251028/
- **11 Mar 2026**: anyone who is a permitted customer under the LINE MINI App Policy can create a
  MINI App channel for services in Taiwan or Thailand. Source: LINE Developers news,
  https://developers.line.biz/en/news/
- **Verification review in Taiwan and Thailand** is open only to MINI App channels under a
  **certified provider**. Source: https://developers.line.biz/en/docs/line-mini-app/submit/submission-guide/
- Unverified MINI Apps cannot use service messages, custom paths or skipping the channel consent
  screen; the header shows the endpoint domain instead of the app name.
  Source: https://developers.line.biz/en/docs/line-mini-app/discover/custom-features
- Market size: "more than 100 brands" in Taiwan use MINI Apps (LY Corp CONVERGE 2025 report,
  Feb 2026). MINI HOME launched in LINE Wallet on 22 Oct 2025; LINE Touch (NFC) planned H1 2026.
  Source: https://www.lycorp.co.jp/en/story/20260218/taiwan_converge2025.html

## 2. Three internal channels and LIFF IDs

- Creating one MINI App channel creates three internal channels: **Developing**, **Review**,
  **Published**. Each has its **own LIFF ID** and its own endpoint URL. `liff.init()` must use
  the LIFF ID of the channel the app runs in.
- When review starts, Developing settings are copied to Review; when published, to Published.
  For unverified apps, Developing is reflected into Published.
- LIFF URL format: `https://miniapp.line.me/{liffId}`; older `https://liff.line.me/{liffId}`
  still opens the app.
- **Verify in console:** whether the three internal channels share one channel ID (and so one
  `aud` in ID tokens) or each has its own. Our `app_channels` table stores a channel ID per
  environment and the verifier accepts the set, so either answer works.
  Source: https://developers.line.biz/en/docs/line-mini-app/discover/console-guide/

## 3. Same-provider user ID rule

- A LINE user has a **different user ID per provider**. User IDs match across channels only when
  the channels belong to the same provider.
- Therefore the MINI App channel (login) and the OA's Messaging API channel (webhooks, push)
  **must be under the same provider**, or the login `sub` never matches webhook and push IDs.
- `line_user_id` is unique per app, never globally: unique index `(app_id, line_user_id)`.
  Source: https://developers.line.biz/en/docs/line-login/verify-id-token/ (sub semantics) and
  https://developers.line.biz/en/faq

### Setup step: a separate LINE Login channel for the staff app

- The staff LIFF app runs on its **own LINE Login channel**, created under the **same provider**
  as the MINI App (so a cashier's `sub` stays stable) but with a **different channel ID**.
- Add its LIFF app in the LINE Developers Console with the **Scan QR** toggle on, then enter
  the channel ID, secret and LIFF ID in the dashboard under Channels, row **Staff app**
  (`environment: staff`). The staff app's `VITE_LIFF_ID` is this LIFF ID.
- Why: staff sign-in accepts only the `staff` channel as `aud`, and member sign-in never accepts
  it. Without the split, any member ID token from the MINI App could be presented to staff
  sign-in. The API refuses a staff channel ID equal to a MINI App channel ID, and staff sign-in
  answers `409 staff_channel_required` until the row exists.

## 4. ID token algorithms and verification

- ID tokens from **native apps, the LINE SDK and LIFF apps are ES256** (ECDSA P-256) and carry a
  `kid` header. Tokens from **web login are HS256**, signed with the channel secret, no `kid`.
- ES256 keys: JWK set at `https://api.line.me/oauth2/v2.1/certs`, matched by `kid`.
- Always check `iss` = `https://access.line.me`, `aud` = your channel ID, `exp`, and `nonce` when
  you set one.
- Remote check: `POST https://api.line.me/oauth2/v2.1/verify` with form fields `id_token` and
  `client_id` (channel ID). We use it only as a fallback.
- Scopes: `openid` for the ID token, `profile` for name and picture, `email` needs separate
  permission.
  Source: https://developers.line.biz/en/docs/line-login/verify-id-token/ and
  https://developers.line.biz/en/reference/line-login/

## 5. Channel access tokens

- MINI App channels: use **stateless channel access tokens**. Long-lived tokens and v2.1 tokens
  **cannot be used for LINE MINI App channels**.
- Stateless tokens: `POST https://api.line.me/oauth2/v3/token` with `grant_type=client_credentials`,
  `client_id`, `client_secret`; valid 15 minutes; unlimited issuance; cannot be revoked.
- For the OA Messaging API channel we also mint stateless tokens from channel ID + secret, so
  no access token is stored at rest. Cache per channel, renew 60 s before expiry, one in-flight
  refresh.
  Source: https://developers.line.biz/en/docs/line-mini-app/discover/console-guide/ and
  https://developers.line.biz/en/reference/messaging-api/#issue-stateless-channel-access-token

## 6. Service messages (Phase 2, verified MINI Apps only)

- Available to **verified** MINI Apps only.
- Templates: up to **20 per MINI App channel**; each must pass review by LY Corporation; using a
  template outside its declared use case can get it blocked.
- Token flow: the client obtains a LIFF access token; the server calls the **issue service
  notification token** endpoint with it. One service notification token per LIFF access token;
  the token is bound to **one user**; it **expires 1 year** after issue; **every send renews the
  token value** (store the renewed token); the number of messages per token is set at review and
  is "subject to change depending on the use scenario" (third-party sources say 5; unconfirmed).
- **Never use the Developing channel's access token** to send service messages for Review or
  Published. The template tab is not copied to Published until the app passes verification.
  Source: https://developers.line.biz/en/docs/line-mini-app/develop/service-messages/ and
  https://developers.line.biz/en/reference/line-mini-app/

## 7. Messaging API webhooks

- Signature: header `X-Line-Signature` = Base64(HMAC-SHA256(channel secret, raw request body)).
  Compare in constant time. Reject with 401 on mismatch.
- LINE may **redeliver** events. Each event carries `webhookEventId`, and
  `deliveryContext.isRedelivery` is `true` on a redelivery. Dedupe on `(app_id, webhookEventId)`.
- Respond 200 quickly (our target: under 1 s) and do the work in the worker.
- `follow` / `unfollow` events come from the OA Messaging API channel; the webhook URL is set on
  that channel. Push messages count against the OA's monthly quota.
  Source: https://developers.line.biz/en/reference/messaging-api/#webhooks

## 8. `liff.scanCodeV2()`

- Not listed as unavailable in MINI Apps. Requires the **Scan QR** toggle on the channel's LIFF
  settings and LIFF SDK **2.15.0 or later**. Works in the LIFF browser and, since it uses an
  internal JS library, in external browsers too.
- A third-party source says the screen size must be **Full**. Treat as **verify on device**.
- The old `liff.scanCode()` is deprecated and does not work in external browsers.
- Our staff app uses `scanCodeV2` with a browser fallback (`@zxing/browser`).
  Source: https://developers.line.biz/en/docs/liff/ and
  https://developers.line.biz/en/docs/line-mini-app/discover/specifications/

## 9. `liff.iap` in-app purchase (Japan-only today)

- Flow: `liff.isApiAvailable("iap")` → `liff.iap.getPlatformProducts()` →
  `liff.iap.requestConsentAgreement()` → reserve → `liff.iap.createPayment()`; webhooks report
  completion and refunds; dedupe grants by `orderId`.
- Requirements: LIFF SDK 2.26.0+, LIFF browser only, LINE app 15.6.0+, the user needs a
  **registered Japanese phone number**; LY Corp's business page says the service is for
  providers based in Japan; unverified apps can use it only in Developing and Review.
- Taiwan in-app payments were announced on 28 Oct 2025 but we found no availability date.
  **Verify** before planning revenue on it.
  Source: https://developers.line.biz/en/docs/line-mini-app/in-app-purchase/overview/

## 10. Taiwan LINE Official Account pricing (from 1 Nov 2026)

- Mid plan (中用量): NT$800 → **NT$1,000** per month; high plan (高用量): NT$1,200 →
  **NT$1,400** per month (before tax). Light plan (輕用量) stays free with 200 messages.
- Free message quotas unchanged: 200 / 3,000 / 6,000 per month.
- High-plan add-on messages move to two tiers: about NT$0.2 per message up to 50,000, then
  about NT$0.15 (third-party summary; confirm on the official page).
- Back-office maintenance windows announced for 28 Oct–2 Nov and 24–25 Nov 2026.
- Pricing belongs in a config table with an effective date; the dashboard shows an estimate
  before each push.
  Source: https://tw.linebiz.com/column/LINEOA-2026-Price-Plan/

## 11. Taiwan PDPA status

- Amendments passed the Legislative Yuan on **17 Oct 2025** and were promulgated on
  **11 Nov 2025**. The **effective date is set by the Executive Yuan** and was still pending as of
  mid-2026. The Personal Data Protection Commission is being established, with a six-year
  transition during which sector regulators keep oversight.
- New duties: mandatory breach notification to affected individuals and the regulator (even when
  no PDPA violation caused the breach), higher fines, data protection officer requirements for
  some organisations.
- The **72-hour** deadline currently comes from MODA rules for digital-economy sectors; the
  Commission may set the final deadline. We design for 72 hours.
  Sources: https://www.klgates.com/New-Developments-in-the-Taiwan-Personal-Data-Protection-Act-1-13-2026
  and https://www.bakermckenzie.com/en/insight/publications/alerts/2025/10/taiwan-amendment-to-personal-data-protection-act

## 12. Competitive context (for positioning, not for code)

- LINE OA Manager includes a **free stamp card (集點卡)** and coupons on every plan, including
  unverified accounts. Customers must be OA friends to collect stamps (2024 change).
- Ocard: Taiwanese loyalty platform with thousands of brands, connects to the OA in about 30
  minutes. Omnichat: LINE member-card module. Crescendo Lab MAAC: LINE marketing automation from
  about NT$3,000 per month.
- Japan: many MINI App membership vendors (Yappli MembersCard from ¥12,000/month, みせめぐMini
  at ¥0, Lメンバーズカード, toypo, みせプリ, P+KACHI).
