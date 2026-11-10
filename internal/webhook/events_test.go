package webhook_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

// Invariant: a follow that creates the member records member.created once,
// in the same transaction, with the member ID and no LINE user ID; a
// re-follow of a known member records nothing.
func TestFollowOfANewMemberEmitsMemberCreated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	steps := []struct {
		name, typ, id, user string
		want                int
	}{
		{"first follow creates the member", "follow", "evt-a", "Unew0000000000000000000000000001", 1},
		{"unfollow", "unfollow", "evt-b", "Unew0000000000000000000000000001", 1},
		{"re-follow of a known member", "follow", "evt-c", "Unew0000000000000000000000000001", 1},
		{"another new member", "follow", "evt-d", "Unew0000000000000000000000000002", 2},
	}
	for _, s := range steps {
		body := event(s.typ, s.id, s.user, false)
		if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		var n int
		if err := f.d.Admin.QueryRow(ctx, "SELECT count(*) FROM outbound_events WHERE type = 'member.created' AND app_id = $1", f.app).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != s.want {
			t.Fatalf("%s: %d member.created events, want %d", s.name, n, s.want)
		}
	}
	var payload string
	if err := f.d.Admin.QueryRow(ctx, `SELECT o.payload::text FROM outbound_events o JOIN members m ON m.id = o.member_id
		WHERE o.type = 'member.created' AND m.line_user_id = $1`, "Unew0000000000000000000000000001").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "Unew") || !strings.Contains(payload, `"source": "follow"`) {
		t.Errorf("payload = %s", payload)
	}
}

// The service ages deliveries and dates member.created by its injected
// clock, not the machine's: an event a week older than the clock is
// ignored, and a new member's event without a LINE timestamp occurs at the
// clock's time.
func TestWebhookTimeComesFromTheClock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	clk := clock.At(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := webhook.New(f.d.App, tenant.New(f.d.App, f.keyring, clock.Real{}, log), clk, log)

	// Sent "now" by the machine's clock, which is years before the
	// service's clock: older than the dedupe window, so it is ignored.
	body := event("follow", "evt-stale", "Ustale", false)
	res, err := svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil || res.Ignored != 1 || res.Processed != 0 {
		t.Fatalf("stale delivery: %v %+v", err, res)
	}

	// No timestamp: the event is dated by the clock.
	body = []byte(`{"destination":"Ubot","events":[{"type":"follow","webhookEventId":"evt-clock","deliveryContext":{"isRedelivery":false},"mode":"active","source":{"type":"user","userId":"Uclock"}}]}`)
	if res, err := svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil || res.Processed != 1 {
		t.Fatalf("follow: %v %+v", err, res)
	}
	var at time.Time
	if err := f.d.Admin.QueryRow(ctx, `SELECT o.occurred_at FROM outbound_events o JOIN members m ON m.id = o.member_id
		WHERE o.type = 'member.created' AND m.line_user_id = 'Uclock'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(clk.Now()) {
		t.Errorf("member.created occurred_at = %s, want the clock's %s", at, clk.Now())
	}
}
