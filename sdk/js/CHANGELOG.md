# Changelog

## SDK

### 0.2.0

- `MiniLoyal.init()` is now asynchronous: it runs `liff.init()`, starts LINE login when needed (`state === 'redirecting'`) and exchanges the LIFF ID token for a member session.
- Member API: `me()`, `updateMe()`, `consent()`, `exportMe()`, `deleteMe()`, `friendship()`, `addFriendUrl()`, `login()`, `logout()`, `session`, `state`.
- Sessions renew before expiry and once after a `401`; mutations carry an `Idempotency-Key` reused on the retry.
- `MiniLoyalError` with `code`, `status`, `requestId`, `details` and `field()`.
- Breaking: `init()` returns a promise; the `openapi-fetch` dependency was dropped.

### 0.1.0

- Package skeleton with `health()`.
