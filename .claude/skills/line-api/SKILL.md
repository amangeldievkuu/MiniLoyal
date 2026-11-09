---
name: line-api
description: Reference card of verified LINE Platform facts for MiniLoyal: ID token verification, channel tokens, Messaging API, webhooks, service messages, MINI App channels, LIFF. Read before touching any LINE integration.
---

# LINE Platform reference card

Verified against developers.line.biz on 2026-11-09. When a live page disagrees with this card, the page wins. Update this file in the same commit.

## User identity

- A LINE user has a different user ID per **provider**. Channels under the same provider see the same ID. The MINI App channel and the brand's Messaging API channel must sit under one provider or login `sub` will not match webhook and push user IDs. Store members as `UNIQUE (app_id, line_user_id)`. https://developers.line.biz/en/docs/line-developers-console/overview/#provider

## ID tokens (LIFF login)

- Get: `liff.getIDToken()` (needs the `openid` scope; `profile` for name and picture, `email` needs separate permission).
- LIFF and native tokens are **ES256** with a `kid` header. Verify against the JWKS at `https://api.line.me/oauth2/v2.1/certs`, cache keys, refresh on unknown `kid`. Web login tokens are **HS256** signed with the channel secret. Check `iss` = `https://access.line.me`, `aud` = channel ID, `exp`, and `nonce` when you sent one. https://developers.line.biz/en/docs/line-login/verify-id-token/
- Fallback or cross-check: `POST https://api.line.me/oauth2/v2.1/verify` with form fields `id_token` and `client_id`. https://developers.line.biz/en/reference/line-login/#verify-id-token

## Channel access tokens

- Use **stateless** tokens: `POST https://api.line.me/oauth2/v3/token` with `grant_type=client_credentials`, `client_id`, `client_secret`. Valid 15 minutes, unlimited issuance, cannot be revoked. Cache in memory, single in-flight refresh, renew 60 seconds early. Never persist. https://developers.line.biz/en/reference/messaging-api/#issue-stateless-channel-access-token
- LINE MINI App channels do not accept long-lived or v2.1 tokens. Use stateless tokens for service messages. https://developers.line.biz/en/docs/line-mini-app/discover/console-guide/

## Messaging API (brand OA)

- Push: `POST https://api.line.me/v2/bot/message/push` (one user). Multicast: `POST https://api.line.me/v2/bot/message/multicast` (up to 500 user IDs). Narrowcast and broadcast exist but are out of MVP scope. https://developers.line.biz/en/reference/messaging-api/#send-push-message
- Idempotent retries: send `X-Line-Retry-Key: <UUID>`; the same key within 24 hours is not resent. https://developers.line.biz/en/reference/messaging-api/#retry-api-request
- Rate limits: push and multicast 2,000 requests per second per channel; broadcast 60 requests per hour. Confirm at https://developers.line.biz/en/reference/messaging-api/#rate-limits
- Messages count toward the OA plan quota. Taiwan prices from 2026-11-01: mid plan NT$1,000/month, high plan NT$1,400/month, free quotas 200 / 3,000 / 6,000, add-on messages tiered around NT$0.2 then NT$0.15. Keep these in a config table. https://tw.linebiz.com/column/LINEOA-2026-Price-Plan/
- Profile: `GET https://api.line.me/v2/bot/profile/{userId}` works only while the user is a friend.

## Webhooks (brand OA)

- One webhook URL per Messaging API channel, set in the console. Route `POST /webhooks/line/{app_id}` and load that app's channel secret.
- Verify `X-Line-Signature`: base64 of HMAC-SHA256(channel secret, raw request body). Compare in constant time. Return 401 on failure. https://developers.line.biz/en/reference/messaging-api/#signature-validation
- Each event carries `webhookEventId` and `deliveryContext.isRedelivery`. Deduplicate on the ID. Enqueue and return 200 within one second. https://developers.line.biz/en/reference/messaging-api/#webhook-event-objects
- MVP events: `follow` (sets `is_friend = true`, may trigger a welcome coupon), `unfollow` (sets `is_friend = false`). `source.userId` is the provider-scoped user ID.

## Service messages (verified MINI Apps only)

- Unverified MINI Apps cannot send them. Templates (max 20 per channel) must pass LY Corporation review and be used as described in their use case. https://developers.line.biz/en/docs/line-mini-app/develop/service-messages/
- Token: `POST https://api.line.me/message/v3/notifier/token` with body `{"liffAccessToken": "..."}` and the MINI App channel's stateless token as Bearer. Returns `notificationToken`, `expiresIn` (one year), `remainingCount`, `sessionId`. One notification token per LIFF access token, bound to one user. https://developers.line.biz/en/reference/line-mini-app/#issue-notification-token
- Send: `POST https://api.line.me/message/v3/notifier/send?target=service` with `templateName`, `params`, `notificationToken`. The response carries a renewed token; store it. The count is limited and set at review. https://developers.line.biz/en/reference/line-mini-app/#send-service-message
- Never use the Developing channel's token for the Review or Published channel.

## MINI App channels and verification

- One MINI App channel creates three internal channels: Developing, Review, Published. Each has its own LIFF ID and endpoint URL. LIFF URL format: `https://miniapp.line.me/{liffId}`. Configure all three per app. https://developers.line.biz/en/docs/line-mini-app/discover/console-guide/
- Unverified MINI Apps can be published without review but lack service messages, custom path and consent-screen skipping. https://developers.line.biz/en/docs/line-mini-app/discover/custom-features/
- Taiwan and Thailand: channel creation is open to anyone meeting the LINE MINI App Policy since 2026-03-11 (LINE Developers news). Verification review still requires a certified provider. https://developers.line.biz/en/news/ and https://developers.line.biz/en/docs/line-mini-app/submit/submission-guide/
- In-app purchase (`liff.iap`) is Japan-only today. Taiwan payments were announced but have not shipped. https://developers.line.biz/en/docs/line-mini-app/in-app-purchase/overview/

## LIFF client

- `liff.init({ liffId })`, `liff.isLoggedIn()`, `liff.login()`, `liff.getIDToken()`, `liff.getAccessToken()`, `liff.getProfile()`. https://developers.line.biz/en/reference/liff/
- `liff.getFriendship()` returns `{ friendFlag }` and needs the OA linked to the channel with the add-friend option set.
- `liff.scanCodeV2()` needs the Scan QR toggle on the channel and LIFF SDK 2.15 or later. Check `liff.isApiAvailable('scanCodeV2')` and fall back to a browser QR library.
- `liff.shareTargetPicker()` for referrals (Phase 2).

## Go SDK

- `github.com/line/line-bot-sdk-go/v8` provides `linebot/messaging_api` and webhook parsing. Use it for push and multicast. Write small clients for login verify, stateless tokens and service messages. https://github.com/line/line-bot-sdk-go
