# Changelog

## SDK

### Unreleased

- Coupons: `coupons.list({ state, limit, cursor })`, `coupons.listAll()`, `coupons.offers()`, `coupons.claim(templateId, { idempotencyKey })`.
- Stamps: `stamps()` returns progress per card.
- New types: `CouponInstance`, `CouponInstancePage`, `CouponOffer`, `CouponTemplateSummary`, `CouponState`, `CouponFilter`, `CouponListOptions`, `StampProgress`, `CouponErrorCode`.
- No breaking changes.

### 0.3.0

- Member card: `card()` returns the rotating QR payload; `watchCard(onCard, onError)` keeps it current (refresh at `refresh_in_seconds`, paused while the page is hidden, backoff 1 to 30 s on errors) and returns `stop()`.
- Points: `points()` (balance, next expiries, earn rule), `pointsHistory({ limit, cursor })` and the async iterator `pointsHistoryAll({ limit })`.
- New types: `MemberCard`, `PointsSummary`, `PointsTransaction`, `PointsHistoryPage`, `PointsKind`, `PageOptions`, `StopWatching`.
- No breaking changes.

### 0.2.0

- `MiniLoyal.init()` is now asynchronous: it runs `liff.init()`, starts LINE login when needed (`state === 'redirecting'`) and exchanges the LIFF ID token for a member session.
- Member API: `me()`, `updateMe()`, `consent()`, `exportMe()`, `deleteMe()`, `friendship()`, `addFriendUrl()`, `login()`, `logout()`, `session`, `state`.
- Sessions renew before expiry and once after a `401`; mutations carry an `Idempotency-Key` reused on the retry.
- `MiniLoyalError` with `code`, `status`, `requestId`, `details` and `field()`.
- Breaking: `init()` returns a promise; the `openapi-fetch` dependency was dropped.

### 0.1.0

- Package skeleton with `health()`.
