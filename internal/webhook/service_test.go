package webhook_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

const oaSecret = "oa-channel-secret"

type fixture struct {
	d        *testutil.DB
	svc      *webhook.Service
	tenant   uuid.UUID
	app      uuid.UUID
	noSecret uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := testutil.NewDB(t)
	keyring := crypto.Static(bytes.Repeat([]byte{9}, 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenants := tenant.New(d.App, keyring, clock.Real{}, log)
	f := &fixture{d: d, svc: webhook.New(d.App, tenants, log), tenant: d.NewTenant(t, "Alpha"), app: testutil.NewID(), noSecret: testutil.NewID()}
	enc, err := keyring.Encrypt(context.Background(), []byte(oaSecret), "apps.oa_channel_secret_enc:"+f.app.String())
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug, oa_channel_id, oa_channel_secret_enc) VALUES ($1, $2, 'Cafe', 'cafe', '2001', $3)", f.app, f.tenant, enc)
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Bare', 'bare')", f.noSecret, f.tenant)
	return f
}

func event(typ, id, userID string, redelivery bool) []byte {
	return []byte(fmt.Sprintf(`{"destination":"Ubot","events":[{"type":%q,"webhookEventId":%q,"deliveryContext":{"isRedelivery":%v},"timestamp":%d,"mode":"active","source":{"type":"user","userId":%q}}]}`, typ, id, redelivery, time.Now().UnixMilli(), userID))
}

func (f *fixture) friend(t *testing.T, userID string) (exists, isFriend bool) {
	t.Helper()
	err := f.d.Admin.QueryRow(context.Background(), "SELECT is_friend FROM members WHERE app_id = $1 AND line_user_id = $2", f.app, userID).Scan(&isFriend)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return false, false
		}
		t.Fatal(err)
	}
	return true, isFriend
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

func TestFollowAndUnfollowUpdateFriendStatusOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	body := event("follow", "evt-1", "U1", false)
	res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil || res.Processed != 1 || res.Duplicates != 0 {
		t.Fatalf("follow: %v %+v", err, res)
	}
	if exists, friend := f.friend(t, "U1"); !exists || !friend {
		t.Fatalf("member after follow: exists=%v friend=%v", exists, friend)
	}

	// LINE redelivers the same event: recorded as a duplicate, nothing changes.
	redelivered := event("follow", "evt-1", "U1", true)
	res, err = f.svc.Receive(ctx, f.app, line.Sign(oaSecret, redelivered), redelivered)
	if err != nil || res.Processed != 0 || res.Duplicates != 1 {
		t.Fatalf("redelivery: %v %+v", err, res)
	}

	body = event("unfollow", "evt-2", "U1", false)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
		t.Fatal(err)
	}
	if _, friend := f.friend(t, "U1"); friend {
		t.Error("unfollow did not clear is_friend")
	}
	// Unfollow for a user we never saw is harmless.
	body = event("unfollow", "evt-3", "U-unknown", false)
	if res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil || res.Processed != 1 {
		t.Errorf("unfollow unknown: %v %+v", err, res)
	}
	if exists, _ := f.friend(t, "U-unknown"); exists {
		t.Error("unfollow must not create members")
	}
	// Other event types are recorded and ignored.
	body = event("message", "evt-4", "U1", false)
	if res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil || res.Ignored != 1 {
		t.Errorf("message event: %v %+v", err, res)
	}
}

func TestReceiveRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := event("follow", "evt-9", "U9", false)

	if _, err := f.svc.Receive(ctx, f.app, "bad", body); status(t, err) != http.StatusUnauthorized {
		t.Errorf("bad signature: %v", err)
	}
	if _, err := f.svc.Receive(ctx, f.app, line.Sign("other-secret", body), body); status(t, err) != http.StatusUnauthorized {
		t.Errorf("other secret: %v", err)
	}
	if exists, _ := f.friend(t, "U9"); exists {
		t.Error("a rejected delivery changed state")
	}
	if _, err := f.svc.Receive(ctx, testutil.NewID(), line.Sign(oaSecret, body), body); status(t, err) != http.StatusNotFound {
		t.Errorf("unknown app: %v", err)
	}
	if _, err := f.svc.Receive(ctx, f.noSecret, line.Sign(oaSecret, body), body); status(t, err) != http.StatusUnauthorized {
		t.Errorf("app without a secret: %v", err)
	}
	garbage := []byte(`{"events":"nope"}`)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, garbage), garbage); status(t, err) != http.StatusBadRequest {
		t.Errorf("garbage body: %v", err)
	}
	f.d.Exec(t, "UPDATE apps SET status = 'disabled' WHERE id = $1", f.app)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); status(t, err) != http.StatusNotFound {
		t.Errorf("disabled app: %v", err)
	}
}

func TestDeleteOldInboundEvents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := event("follow", "evt-old", "U1", false)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
		t.Fatal(err)
	}
	f.d.Exec(t, "UPDATE webhook_inbound_events SET received_at = now() - interval '8 days' WHERE app_id = $1", f.app)
	n, err := webhook.DeleteOldInboundEvents(ctx, f.d.App)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
}
