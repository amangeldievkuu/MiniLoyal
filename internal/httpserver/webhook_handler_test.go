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
		Tenants: tenants, Webhooks: webhook.New(d.App, tenants, log),
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
