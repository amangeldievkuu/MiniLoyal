package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Emit refuses an event before it reaches the database when its type is not
// in the outbound_events CHECK, it lacks its scope or time, or its payload
// has a key that could carry personal data.
func TestEmitRefusesBadEvents(t *testing.T) {
	ok := Event{TenantID: uuid.New(), AppID: uuid.New(), Type: CouponIssued, OccurredAt: time.Now(), Payload: map[string]any{"coupon_id": uuid.New()}}
	cases := []struct {
		name string
		edit func(e *Event)
		want string
	}{
		{"unknown type", func(e *Event) { e.Type = "coupon.voided" }, "unknown type"},
		{"no tenant", func(e *Event) { e.TenantID = uuid.Nil }, "needs tenant"},
		{"no app", func(e *Event) { e.AppID = uuid.Nil }, "needs tenant"},
		{"no time", func(e *Event) { e.OccurredAt = time.Time{} }, "needs tenant"},
		{"display name", func(e *Event) { e.Payload = map[string]any{"display_name": "Lin"} }, "personal data"},
		{"phone", func(e *Event) { e.Payload = map[string]any{"Phone": "0912"} }, "personal data"},
		{"line user id", func(e *Event) { e.Payload = map[string]any{"line_user_id": "U1"} }, "personal data"},
		{"note", func(e *Event) { e.Payload = map[string]any{"note": "birthday"} }, "personal data"},
		{"void reason", func(e *Event) { e.Payload = map[string]any{"void_reason": "x"} }, "personal data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ok
			tc.edit(&e)
			// A nil Queries proves the refusal happens before any query.
			err := Emit(context.Background(), nil, e)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Emit = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// Every type the migration allows is known, and nothing else.
func TestKnownTypesMatchTheMigration(t *testing.T) {
	migration := []string{
		"member.created", "member.updated", "friend.added", "friend.removed",
		"points.changed", "coupon.issued", "coupon.redeemed", "stamp.completed",
	}
	if len(known) != len(migration) {
		t.Fatalf("known has %d types, the migration %d", len(known), len(migration))
	}
	for _, typ := range migration {
		if !known[typ] {
			t.Errorf("%s is allowed by the migration but unknown to Emit", typ)
		}
	}
}

// EmitAll refuses a batch spanning apps or naming no member, and checks
// every event as Emit does, before any query.
func TestEmitAllRefusesMixedBatches(t *testing.T) {
	tenantID, appID := uuid.New(), uuid.New()
	ev := func() Event {
		return Event{TenantID: tenantID, AppID: appID, Type: MemberCreated, MemberID: uuid.New(), OccurredAt: time.Now(), Payload: map[string]any{"source": "follow"}}
	}
	otherApp, noMember, personal := ev(), ev(), ev()
	otherApp.AppID = uuid.New()
	noMember.MemberID = uuid.Nil
	personal.Payload = map[string]any{"display_name": "Lin"}
	for name, batch := range map[string][]Event{
		"two apps":      {ev(), otherApp},
		"no member":     {ev(), noMember},
		"personal data": {ev(), personal},
	} {
		t.Run(name, func(t *testing.T) {
			if err := EmitAll(context.Background(), nil, batch); err == nil {
				t.Fatal("EmitAll accepted the batch")
			}
		})
	}
	if err := EmitAll(context.Background(), nil, nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}
