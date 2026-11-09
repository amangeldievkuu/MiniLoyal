# LINE Platform invariants

These hold everywhere in the repository. The `line-api` skill has the endpoints and URLs.

- A LINE user ID is scoped to a **provider**. The MINI App channel and the brand's Messaging API channel must share a provider. Members are unique per `(app_id, line_user_id)`, never globally.
- LIFF ID tokens are ES256 and verified locally against LINE's JWKS; HS256 tokens use the channel secret. Always check `iss`, `aud`, `exp` and `nonce`.
- A MINI App has three internal channels (Developing, Review, Published) with different LIFF IDs. Store them per app and accept each configured channel ID as a valid `aud`.
- Channel access tokens are stateless, 15-minute tokens minted on demand and cached in memory. They are never stored or logged.
- Webhooks are verified on the raw body with `X-Line-Signature`, deduplicated by `webhookEventId`, acknowledged within one second after enqueueing.
- Service messages exist only for verified MINI Apps, need reviewed templates, and use a per-user notification token that is renewed on every send. They are not an MVP feature.
- Push messages cost the brand money under its OA plan. Show an estimate before sending and keep prices in configuration.
- In Taiwan and Thailand, verification review requires a certified provider. Pilot apps are unverified; design flows that work without verification.
- `liff.scanCodeV2()` needs the Scan QR toggle and LIFF 2.15 or later; always keep a browser fallback.
- Never ship a LIFF ID, channel ID or API URL hard-coded. They differ per environment.
