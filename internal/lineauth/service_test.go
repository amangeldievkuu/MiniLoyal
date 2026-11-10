package lineauth_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth/lineauthtest"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

var (
	signingKey = []byte("integration-test-signing-key-32b!")
	keyring    = crypto.Static(bytes.Repeat([]byte{9}, 32))
	client     = lineauth.Client{IP: "203.0.113.7", UserAgent: "tests/1.0"}
)

// world seeds a tenant with one app and one developing channel.
type world struct {
	d        *testutil.DB
	tenant   uuid.UUID
	app      uuid.UUID
	verifier *lineauthtest.FakeVerifier
	svc      *lineauth.Service
	clk      *clock.Fixed
}

const channelID = "2007001234"

func newWorld(t *testing.T, cfg lineauth.Config) *world {
	t.Helper()
	d := testutil.NewDB(t)
	w := &world{d: d, verifier: lineauthtest.NewFakeVerifier(), clk: clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))}
	w.tenant = d.NewTenant(t, "Alpha")
	w.app = testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug, oa_basic_id) VALUES ($1, $2, 'Cafe', 'cafe', '@cafe')", w.app, w.tenant)
	secret, err := keyring.Encrypt(context.Background(), []byte("channel-secret"), "app_channels.channel_secret_enc:"+w.app.String()+":developing")
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(t, "INSERT INTO app_channels (id, tenant_id, app_id, environment, channel_id, liff_id, channel_secret_enc) VALUES ($1, $2, $3, 'developing', $4, $5, $6)",
		testutil.NewID(), w.tenant, w.app, channelID, channelID+"-abcdefgh", secret)
	cfg.SigningKey = signingKey
	if cfg.IPPerMinute == 0 {
		cfg.IPPerMinute = 10000
	}
	w.svc, err = lineauth.New(lineauth.Deps{Pool: d.App, Keyring: keyring, Verifier: w.verifier, Clock: w.clk, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) token(t *testing.T, raw, sub, name string) string {
	t.Helper()
	w.verifier.Add(raw, lineauth.Claims{Sub: sub, Name: name, Picture: "https://profile.line-scdn.net/" + sub, Aud: channelID})
	return raw
}

func status(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 200
	}
	ae, ok := apierr.As(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	return ae.Status
}

func TestLoginCreatesThenRefreshesTheMember(t *testing.T) {
	w := newWorld(t, lineauth.Config{})
	ctx := context.Background()
	lang := "zh-TW"

	first, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: w.token(t, "id-token-one-aaaaaaaaaaaa", "Uabc", "Mei"), Language: &lang}, client)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if !first.IsNew || first.Member.LineUserID != "Uabc" || *first.Member.DisplayName != "Mei" || *first.Member.Language != "zh-TW" {
		t.Errorf("first session: %+v", first.Member)
	}
	if first.App.ID != w.app || *first.App.OaBasicID != "@cafe" || first.ExpiresIn != 3600 {
		t.Errorf("app in session: %+v, expires %d", first.App, first.ExpiresIn)
	}

	second, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: w.token(t, "id-token-two-bbbbbbbbbbbb", "Uabc", "Mei Lin")}, client)
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if second.IsNew || second.Member.ID != first.Member.ID || *second.Member.DisplayName != "Mei Lin" {
		t.Errorf("second session: new=%v id=%s name=%v", second.IsNew, second.Member.ID, second.Member.DisplayName)
	}
	if *second.Member.Language != "zh-TW" {
		t.Error("a login without language must keep the stored one")
	}

	// The verifier saw every configured channel and, since HS256 is opt-in
	// per app and off by default, no channel secret.
	call := w.verifier.Calls[len(w.verifier.Calls)-1]
	if len(call.ChannelIDs) != 1 || call.ChannelIDs[0] != channelID || len(call.ChannelSecrets) != 0 {
		t.Errorf("verifier input: %+v", call)
	}

	p, err := w.svc.AuthenticateMember(ctx, second.Token)
	if err != nil || !p.IsMember() || p.MemberID != first.Member.ID || p.AppID != w.app || p.TenantID != w.tenant {
		t.Errorf("authenticate: %v %+v", err, p)
	}
}

func TestLoginRejections(t *testing.T) {
	w := newWorld(t, lineauth.Config{})
	ctx := context.Background()
	w.verifier.Add("wrong-audience-token-xxxxxxxx", lineauth.Claims{Sub: "Uzzz", Aud: "9999999999"})

	disabled := testutil.NewID()
	w.d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug, status) VALUES ($1, $2, 'Old', 'old', 'disabled')", disabled, w.tenant)
	noChannels := testutil.NewID()
	w.d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Bare', 'bare')", noChannels, w.tenant)

	cases := []struct {
		name string
		in   lineauth.LoginInput
		want int
	}{
		{"unknown token", lineauth.LoginInput{AppID: w.app, IDToken: "never-registered-token-xxxxxx"}, 401},
		{"audience of another channel", lineauth.LoginInput{AppID: w.app, IDToken: "wrong-audience-token-xxxxxxxx"}, 401},
		{"unknown app", lineauth.LoginInput{AppID: uuid.New(), IDToken: "wrong-audience-token-xxxxxxxx"}, 404},
		{"disabled app", lineauth.LoginInput{AppID: disabled, IDToken: "wrong-audience-token-xxxxxxxx"}, 404},
		{"app without channels", lineauth.LoginInput{AppID: noChannels, IDToken: "wrong-audience-token-xxxxxxxx"}, 401},
		{"short token", lineauth.LoginInput{AppID: w.app, IDToken: "short"}, 400},
		{"missing app", lineauth.LoginInput{IDToken: "wrong-audience-token-xxxxxxxx"}, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.svc.LoginWithLine(ctx, tc.in, client)
			if got := status(t, err); got != tc.want {
				t.Errorf("got %d (%v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestLoginWithoutVerifierIs503(t *testing.T) {
	d := testutil.NewDB(t)
	svc, err := lineauth.New(lineauth.Deps{Pool: d.App, Keyring: keyring, Config: lineauth.Config{SigningKey: signingKey}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.LoginWithLine(context.Background(), lineauth.LoginInput{AppID: uuid.New(), IDToken: "some-long-enough-id-token-value"}, client)
	if status(t, err) != 503 {
		t.Errorf("got %v, want 503", err)
	}
}

func TestLoginIsRateLimitedPerIP(t *testing.T) {
	w := newWorld(t, lineauth.Config{IPPerMinute: 2})
	ctx := context.Background()
	tok := w.token(t, "rate-limit-token-aaaaaaaaaaaa", "Urate", "R")
	for i := 0; i < 2; i++ {
		if _, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: tok}, client); err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
	_, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: tok}, client)
	if status(t, err) != 429 {
		t.Fatalf("third login: got %v, want 429", err)
	}
	// Another address is unaffected.
	if _, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: tok}, lineauth.Client{IP: "198.51.100.3"}); err != nil {
		t.Errorf("other ip: %v", err)
	}
}

func TestAuthenticateMemberRejections(t *testing.T) {
	w := newWorld(t, lineauth.Config{})
	ctx := context.Background()
	sess, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: w.token(t, "auth-token-aaaaaaaaaaaaaaaa", "Uauth", "A")}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AuthenticateMember(ctx, "garbage"); status(t, err) != 401 {
		t.Errorf("garbage token: %v", err)
	}
	w.clk.Advance(time.Hour + time.Minute)
	if _, err := w.svc.AuthenticateMember(ctx, sess.Token); status(t, err) != 401 {
		t.Errorf("expired token: %v", err)
	}
	w.clk.Advance(-(time.Hour + time.Minute))

	// Erased members cannot use an old session.
	w.d.Exec(t, "UPDATE members SET anonymized_at = now(), line_user_id = 'erased:' || id::text WHERE id = $1", sess.Member.ID)
	if _, err := w.svc.AuthenticateMember(ctx, sess.Token); status(t, err) != 401 {
		t.Errorf("erased member: %v", err)
	}
	// And the same LINE user signs in again as a new member.
	again, err := w.svc.LoginWithLine(ctx, lineauth.LoginInput{AppID: w.app, IDToken: "auth-token-aaaaaaaaaaaaaaaa"}, client)
	if err != nil || !again.IsNew || again.Member.ID == sess.Member.ID {
		t.Errorf("re-login after erasure: %v new=%v same=%v", err, again != nil && again.IsNew, again != nil && again.Member.ID == sess.Member.ID)
	}
}
