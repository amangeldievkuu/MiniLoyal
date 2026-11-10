package webhook_test

import (
	"context"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/line"
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
