# Real LINE checkpoint

The other smoke tests sign their own HS256 ID tokens and store channels unverified. This one runs
against real LINE channels and two phones. It closes the Phase 2 exit criterion (a real
Developing-channel login) and builds the channels Phase 5 needs for its test push.

## What you need

- LINE Developers Console access and one provider for everything below.
- Two LINE accounts on two devices. Staff cannot serve their own member card (403
  `self_service`), so the member (phone A) and the cashier (phone B) must be different accounts.
- `cloudflared` (`brew install cloudflared`). Quick tunnels need no Cloudflare account.
- `make dev` working with your `.env`.

## 1. Create the channels

All three under the same provider. A different provider gives the member a different user ID
in webhooks than at login, and check 2 below catches that.

1. **LINE MINI App channel**, service region Taiwan. On the Developing tab, note the channel
   ID, channel secret and LIFF ID. Also note the channel ID on the Review and Published tabs.
   That answers the open question in `docs/line/platform-notes.md` §2.
2. **Messaging API channel** for the brand's Official Account. Note the channel ID and channel
   secret from Basic settings.
3. **LINE Login channel** for the staff app. Add a LIFF app: size Full, scopes `openid` and
   `profile`, Scan QR on. Note the channel ID, channel secret and LIFF ID.

Endpoint and webhook URLs come in step 4, once the tunnels exist.

## 2. Start the API with real credential checks

`.env` may set `LINE_SKIP_CREDENTIAL_VERIFICATION=true`. Environment variables win over `.env`,
so turn it off for this run only:

```sh
LINE_SKIP_CREDENTIAL_VERIFICATION=false make dev   # API on :8080
npm run dev:dashboard                              # http://localhost:5173
```

The API must not log `LINE credential verification is disabled` at start.

## 3. Register the app in the dashboard

1. Register an owner and create an app. Note the app ID.
2. **App settings**: OA channel ID and secret. Saving mints a real stateless token, so an error
   here means LINE refused the credentials. Once verified, the page shows the OA basic ID.
3. **Channels**: the Developing row gets the MINI App channel ID, LIFF ID and secret. The Staff
   app row gets the staff Login channel's values. Each save is checked against LINE too.
4. Create a claimable coupon template, a stamp card and a staff member. Note the invite code.

## 4. Tunnels, front ends and console URLs

Three quick tunnels, each in its own terminal. Each prints an `https://….trycloudflare.com` URL.

```sh
cloudflared tunnel --url http://localhost:8080   # API, for the webhook
cloudflared tunnel --url http://localhost:5174   # demo café, phone A
cloudflared tunnel --url http://localhost:5175   # staff app, phone B
```

Start the front ends. Leave `VITE_MINILOYAL_API` unset: both apps call `/v1` on their own
origin and Vite proxies it to :8080. The last variable lets Vite answer on tunnel hosts.

```sh
VITE_MINILOYAL_APP_ID=<app id> VITE_LIFF_ID=<Developing LIFF ID> \
  __VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.trycloudflare.com \
  npm run dev -w @miniloyal/demo-cafe -- --port 5174 --strictPort

VITE_MINILOYAL_APP_ID=<app id> VITE_LIFF_ID=<staff LIFF ID> \
  __VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.trycloudflare.com \
  npm run dev -w @miniloyal/staff -- --port 5175 --strictPort
```

In the console:

- MINI App channel, Developing tab: endpoint URL = the demo café tunnel URL.
- Staff LIFF app: endpoint URL = the staff tunnel URL.
- Messaging API channel: webhook URL = `<API tunnel URL>/webhooks/line/<app id>`, Use webhook
  on, then press **Verify**. It must succeed. That is the first real signature check.

Quick tunnel URLs change whenever `cloudflared` restarts. Update the console when they do.

## 5. Checks

Write down the date, pass or fail, and what you saw for each one.

1. **Member login.** On phone A, open `https://miniapp.line.me/<Developing LIFF ID>` in LINE.
   Accept the consent. Pass: the card shows, the API logged 200 for `POST /v1/auth/line`, and
   the dashboard lists exactly one member. The token was ES256 and verified against LINE's JWKS.
   Only the Developing channel ID is registered, so a pass proves that `aud` is that ID.
2. **Friend status and same provider.** On phone A, add the OA as a friend. Pass: the member
   shows as a friend in the dashboard and the member count is still one. Two members mean the
   webhook user ID did not match the login `sub`, so the channels sit under different providers.
   Then block the OA. Pass: friend status goes back to false.
   ```sh
   psql "$MIGRATE_DATABASE_URL" -c "select count(*), bool_or(is_friend) from members where app_id = '<app id>'"
   ```
3. **Staff counter.** Phone A claims the coupon in the demo café. On phone B, open
   `https://liff.line.me/<staff LIFF ID>` and sign in with the invite code. Scan phone A's card
   with the in-app scanner (`liff.scanCodeV2()`). Earn points, add a stamp and redeem the coupon.
   Pass: each succeeds and phone A's history shows it.
4. **Screenshot.** Take a screenshot of phone A's card, wait 90 seconds, and scan the screenshot
   from phone B. Pass: 422 `card_expired`.

## 6. Record the result

- Add a dated "Verified on a real channel" section to `docs/line/platform-notes.md` with the
  four results and the §2 answer about channel IDs.
- Mark the Phase 2 exit criterion as met in the README status line.
- Treat any failure as a bug: write a failing test first, then fix it, before Phase 5 starts.
