# @miniloyal/sdk

Client for LINE MINI Apps talking to a MiniLoyal backend. It wraps `@line/liff` login, exchanges the LIFF ID token for a MiniLoyal member session, keeps that session fresh and exposes the member API with typed results. Framework-agnostic, ESM and CJS, under 15 kB gzipped.

## Install

```sh
npm install @miniloyal/sdk @line/liff
```

`@line/liff` is a peer dependency: your MINI App already has it.

## Initialise

```ts
import { MiniLoyal } from '@miniloyal/sdk';

const ml = await MiniLoyal.init({
  appId: import.meta.env.VITE_MINILOYAL_APP_ID, // from the MiniLoyal dashboard
  liffId: import.meta.env.VITE_LIFF_ID, // the LIFF ID of this internal channel
  baseUrl: import.meta.env.VITE_MINILOYAL_API, // defaults to the page origin
});

if (ml.state === 'redirecting') {
  // LINE login is opening. Render nothing; the page reloads after login.
}
```

`init()` runs `liff.init()`. When the member is not logged in to LINE it calls `liff.login()` and resolves in the `redirecting` state; it never throws for that. Otherwise it sends `liff.getIDToken()` to `POST /v1/auth/line` and keeps the one-hour session in memory. Pass `autoLogin: false` to decide yourself when to call `ml.login()`.

The session token never touches `localStorage`. Before it expires, and once after any `401`, the SDK exchanges a fresh ID token (LIFF refreshes it) and retries the call once.

## Profile

```ts
const me = await ml.me();
console.log(me.display_name, me.picture_url, me.is_friend, me.custom_fields);

await ml.updateMe({
  birthday: '1990-05-01',
  gender: 'female',
  custom_fields: { tier_hint: 'gold' },
});
```

Every mutation sends an `Idempotency-Key`. Pass your own with `{ idempotencyKey }` when you retry a user action yourself.

## Consent

Record which version of your personal-data notice the member accepted. Until `me.consent` is set, show the notice.

```ts
if (!me.consent) {
  const updated = await ml.consent('2026-11');
  console.log(updated.consent?.accepted_at);
}
```

## Friendship with the Official Account

```ts
const isFriend = await ml.friendship(); // liff.getFriendship(), false when unknown
if (!isFriend) {
  const url = ml.addFriendUrl(); // https://line.me/R/ti/p/@basicid, or null
  if (url) window.open(url, '_blank', 'noopener');
}
```

`addFriendUrl()` needs the app's Official Account basic ID, which the dashboard stores with the app; it returns `null` until then.

## Data rights

```ts
const data = await ml.exportMe(); // everything the brand stores about the member
await ml.deleteMe(); // erases personal data and ends the session
```

## Errors

Every failure is a `MiniLoyalError`:

```ts
import { MiniLoyalError } from '@miniloyal/sdk';

try {
  await ml.updateMe({ phone: 'nope' });
} catch (err) {
  if (err instanceof MiniLoyalError) {
    err.code; // 'validation_failed', 'unauthorized', 'rate_limited', 'network', ...
    err.status; // HTTP status, 0 for network failures
    err.requestId; // quote it when you contact support
    err.field('phone'); // a field message when the API sent one
  }
}
```

SDK-specific codes: `network` (the API was unreachable), `redirecting` (LINE login is opening), `not_signed_in` (`autoLogin: false` and no session yet), `invalid_response`.

## Session events

```ts
const ml = await MiniLoyal.init({
  appId,
  liffId,
  onSessionChange: (session) => setSignedIn(session !== null),
});
ml.session?.member; // the member as of the last sign-in or update
ml.logout(); // ends the MiniLoyal session; ml.logout({ liff: true }) also signs out of LIFF
```

## Types

Request and response shapes are generated from the MiniLoyal OpenAPI document and re-exported: `Member`, `MemberUpdate`, `MemberExport`, `MemberSession`, `AppPublic`, plus the raw `paths` and `components` under `@miniloyal/sdk/schema`.
