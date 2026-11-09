package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/line"
)

// fakeLINE answers the token and bot-info endpoints for a fixed set of
// channel credentials.
type fakeLINE struct {
	srv   *httptest.Server
	creds map[string]string
	mints atomic.Int32
}

func newFakeLINE(t *testing.T, creds map[string]string) *fakeLINE {
	t.Helper()
	f := &fakeLINE{creds: creds}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		f.mints.Add(1)
		_ = r.ParseForm()
		id := r.PostForm.Get("client_id")
		if f.creds[id] == "" || f.creds[id] != r.PostForm.Get("client_secret") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token_type": "Bearer", "access_token": "tok-" + id, "expires_in": 900})
	})
	mux.HandleFunc("GET /v2/bot/info", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"userId": "Ubot", "basicId": "@cafe", "displayName": "Cafe", "chatMode": "bot", "markAsReadMode": "auto"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLINE) verifier() *line.ChannelVerifier {
	tokens := line.NewTokenSource(line.TokenSourceOptions{HTTPClient: f.srv.Client(), TokenURL: f.srv.URL + "/oauth2/v3/token"})
	messaging := line.NewMessaging(line.MessagingOptions{HTTPClient: f.srv.Client(), BaseURL: f.srv.URL, Tokens: tokens})
	return line.NewChannelVerifier(tokens, messaging)
}

func TestCredentialsAreVerifiedAgainstLINE(t *testing.T) {
	w := newWorld(t)
	fake := newFakeLINE(t, map[string]string{"2001234567": "oa-secret", "1661234567": "mini-secret"})
	w.svc.SetChannelVerifier(fake.verifier())
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	owner := w.as("owner-a")
	appPath := "/v1/dashboard/apps/" + app.Id.String()

	// Official Account: valid credentials are stamped and the basic ID is filled in.
	r := owner.patch(appPath, uuid.NewString(), map[string]any{"oa_channel_id": "2001234567", "oa_channel_secret": "oa-secret"})
	want(t, r, http.StatusOK)
	var got api.App
	r.into(t, &got)
	if got.OaVerifiedAt == nil || got.OaBasicId == nil || *got.OaBasicId != "@cafe" || !got.OaChannelSecretSet {
		t.Errorf("verified app = %+v", got)
	}
	if !strings.HasSuffix(got.WebhookUrl, "/webhooks/line/"+app.Id.String()) {
		t.Errorf("webhook_url = %s", got.WebhookUrl)
	}
	// An explicit basic ID is kept.
	r = owner.patch(appPath, uuid.NewString(), map[string]any{"oa_channel_id": "2001234567", "oa_basic_id": "@mine", "oa_channel_secret": "oa-secret"})
	want(t, r, http.StatusOK)
	r.into(t, &got)
	if got.OaBasicId == nil || *got.OaBasicId != "@mine" || got.OaVerifiedAt == nil {
		t.Errorf("explicit basic id = %+v", got)
	}
	// Wrong secret: 400 pointing at the field, nothing stored.
	r = owner.patch(appPath, uuid.NewString(), map[string]any{"oa_channel_id": "2001234567", "oa_channel_secret": "wrong"})
	want(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.body), "channel_secret") {
		t.Errorf("validation body = %s", r.body)
	}
	r = owner.get(appPath)
	r.into(t, &got)
	if got.OaVerifiedAt == nil {
		t.Error("a failed verification must not change the stored credentials")
	}
	// Changing only the channel ID: the stored secret is re-verified against it.
	r = owner.patch(appPath, uuid.NewString(), map[string]any{"oa_channel_id": "9999"})
	want(t, r, http.StatusBadRequest)
	// Clearing the secret leaves the account unverified.
	r = owner.patch(appPath, uuid.NewString(), map[string]any{"oa_channel_secret": ""})
	want(t, r, http.StatusOK)
	// A fresh struct: the key is omitted when null, and Unmarshal keeps old values.
	var cleared api.App
	r.into(t, &cleared)
	var dbVerified *string
	if err := w.db.Admin.QueryRow(context.Background(), "SELECT oa_verified_at::text FROM apps WHERE id = $1", uuid.UUID(app.Id)).Scan(&dbVerified); err != nil {
		t.Fatal(err)
	}
	if cleared.OaVerifiedAt != nil || cleared.OaChannelSecretSet || dbVerified != nil {
		t.Errorf("after clearing the secret = %+v (db oa_verified_at=%v)", cleared, dbVerified)
	}

	// MINI App channel: the secret is required and proven.
	base := appPath + "/channels"
	want(t, owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "1661234567", "liff_id": "1661234567-dev"}), http.StatusBadRequest)
	want(t, owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "1661234567", "liff_id": "1661234567-dev", "channel_secret": "nope"}), http.StatusBadRequest)
	r = owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "1661234567", "liff_id": "1661234567-dev", "channel_secret": "mini-secret"})
	want(t, r, http.StatusOK)
	var ch api.AppChannel
	r.into(t, &ch)
	if ch.VerifiedAt == nil || !ch.ChannelSecretSet {
		t.Errorf("verified channel = %+v", ch)
	}
	if fake.mints.Load() < 4 {
		t.Errorf("LINE token endpoint calls = %d, expected one per verification", fake.mints.Load())
	}
}
