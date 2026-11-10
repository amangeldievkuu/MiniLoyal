package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

func TestReceiveLineWebhookOverHTTP(t *testing.T) {
	d := testutil.NewDB(t)
	keyring := crypto.Static(bytes.Repeat([]byte{3}, 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenants := tenant.New(d.App, keyring, clock.Real{}, log)
	tenantID := d.NewTenant(t, "Alpha")
	appID := testutil.NewID()
	const secret = "oa-secret"
	enc, err := keyring.Encrypt(context.Background(), []byte(secret), "apps.oa_channel_secret_enc:"+appID.String())
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug, oa_channel_id, oa_channel_secret_enc) VALUES ($1, $2, 'Cafe', 'cafe', '2001', $3)", appID, tenantID, enc)

	s := New(Deps{
		Config: &config.Config{Env: config.Development, PublicBaseURL: "https://api.example"}, Log: log, DB: d.App, Version: "test",
		Tenants: tenants, Webhooks: webhook.New(d.App, tenants, nil, log),
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post := func(app uuid.UUID, body []byte, sig string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/line/"+app.String(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if sig != "" {
			req.Header.Set("X-Line-Signature", sig)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	body := []byte(fmt.Sprintf(`{"destination":"U","events":[{"type":"follow","webhookEventId":"http-1","deliveryContext":{"isRedelivery":false},"timestamp":%d,"mode":"active","source":{"type":"user","userId":"U77"}}]}`, time.Now().UnixMilli()))

	if res := post(appID, body, line.Sign(secret, body)); res.StatusCode != http.StatusOK {
		t.Fatalf("valid delivery: %d", res.StatusCode)
	}
	var friend bool
	if err := d.Admin.QueryRow(context.Background(), "SELECT is_friend FROM members WHERE app_id = $1 AND line_user_id = 'U77'", appID).Scan(&friend); err != nil || !friend {
		t.Errorf("member not created as friend: %v %v", err, friend)
	}
	if res := post(appID, body, line.Sign(secret, body)); res.StatusCode != http.StatusOK {
		t.Errorf("duplicate delivery must still answer 200: %d", res.StatusCode)
	}
	if res := post(appID, body, "nope"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad signature: %d", res.StatusCode)
	}
	if res := post(appID, body, ""); res.StatusCode != http.StatusBadRequest {
		t.Errorf("missing signature header: %d", res.StatusCode)
	}
	if res := post(testutil.NewID(), body, line.Sign(secret, body)); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown app: %d", res.StatusCode)
	}
	huge := append(append([]byte(`{"destination":"`), bytes.Repeat([]byte("a"), webhookMaxBody+1)...), []byte(`","events":[]}`)...)
	if res := post(appID, huge, line.Sign(secret, huge)); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", res.StatusCode)
	}
	// A webhook larger than the general API limit but under its own limit is fine.
	padded := []byte(`{"destination":"` + strings.Repeat("b", 100<<10) + `","events":[]}`)
	if res := post(appID, padded, line.Sign(secret, padded)); res.StatusCode != http.StatusOK {
		t.Errorf("100 KiB webhook: %d", res.StatusCode)
	}
}

// Invariant: the webhook body limit is exact. 1 MiB is accepted and applied,
// one more byte is 413 before the signature is even checked, and a body
// that is not JSON is 400 even when its signature is valid.
func TestWebhookBodyLimitBoundary(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	e.linkOA(ownerTok, app, "2001000002")

	// Pad the destination so the whole body is exactly webhookMaxBody bytes.
	frame := `{"destination":"","events":[{"type":"follow","webhookEventId":"limit-1","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":"Ulimit"}}]}`
	pad := strings.Repeat("d", webhookMaxBody-len(frame))
	exact := []byte(strings.Replace(frame, `"destination":""`, `"destination":"`+pad+`"`, 1))
	if len(exact) != webhookMaxBody {
		t.Fatalf("body is %d bytes, want %d", len(exact), webhookMaxBody)
	}
	if r := e.webhookBody(app, exact); r.status != http.StatusOK {
		t.Errorf("exactly 1 MiB: %d %s", r.status, r.body)
	}
	if _, friend, _ := e.memberRow(memberIDByLineUser(t, e, app, "Ulimit")); !friend {
		t.Error("the 1 MiB delivery was not applied")
	}
	over := append(bytes.Clone(exact), ' ')
	r := e.webhookBody(app, over)
	if r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MiB + 1: %d %s", r.status, r.body)
	}
	var ae api.Error
	r.into(t, &ae)
	if ae.Code != "payload_too_large" {
		t.Errorf("code = %s", ae.Code)
	}
	// An oversized body with a bad signature is also 413: the size check
	// comes first and reveals nothing about the secret.
	if r := e.raw(http.MethodPost, "/webhooks/line/"+app.Id.String(), over, map[string]string{line.SignatureHeader: "bad"}); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized with a bad signature: %d", r.status)
	}
	if r := e.webhookBody(app, []byte("not json")); r.status != http.StatusBadRequest {
		t.Errorf("non-JSON with a valid signature: %d %s", r.status, r.body)
	}
	// Headers alone, no body: 400, not a panic.
	if r := e.raw(http.MethodPost, "/webhooks/line/"+app.Id.String(), nil, map[string]string{line.SignatureHeader: line.Sign(erasureOASecret, nil)}); r.status != http.StatusBadRequest {
		t.Errorf("empty body: %d %s", r.status, r.body)
	}
}

func memberIDByLineUser(t *testing.T, e *memberEnv, app api.App, lineUserID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT id FROM members WHERE app_id = $1 AND line_user_id = $2", uuid.UUID(app.Id), lineUserID).Scan(&id); err != nil {
		t.Fatalf("member %s: %v", lineUserID, err)
	}
	return id
}
