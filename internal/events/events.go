// Package events writes the outbound event outbox. Emit inserts one row in
// the caller's transaction, so an event exists exactly when the change it
// describes committed. Delivery (endpoints, signatures, retries) is Phase 5.
//
// Rows are append-only and survive a PDPA erasure, so payloads carry IDs,
// counts and amounts only: never names, phone numbers, LINE user IDs, notes
// or any other text a person typed.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
)

// Event types. They match the CHECK on outbound_events.type.
const (
	MemberCreated  = "member.created"
	MemberUpdated  = "member.updated"
	FriendAdded    = "friend.added"
	FriendRemoved  = "friend.removed"
	PointsChanged  = "points.changed"
	CouponIssued   = "coupon.issued"
	CouponRedeemed = "coupon.redeemed"
	StampCompleted = "stamp.completed"
)

var known = map[string]bool{
	MemberCreated: true, MemberUpdated: true, FriendAdded: true, FriendRemoved: true,
	PointsChanged: true, CouponIssued: true, CouponRedeemed: true, StampCompleted: true,
}

// Event is one outbound event.
type Event struct {
	TenantID uuid.UUID
	AppID    uuid.UUID
	Type     string
	// MemberID is the member the event is about; uuid.Nil for none.
	MemberID uuid.UUID
	// Payload holds IDs, counts, amounts, enums and times only.
	Payload map[string]any
	// OccurredAt comes from the service clock.
	OccurredAt time.Time
}

// personal are payload keys that would carry personal data or free text.
// Emit refuses them so a careless caller fails loudly in tests instead of
// writing a row erasure cannot blank.
var personal = []string{"name", "display_name", "picture", "phone", "email", "line_user", "birthday", "gender", "note", "reason", "address", "token"}

// Emit writes e with q, which must belong to the transaction making the
// change, scoped to e.TenantID.
func Emit(ctx context.Context, q *sqlc.Queries, e Event) error {
	raw, err := encode(e)
	if err != nil {
		return err
	}
	if _, err := q.InsertOutboundEvent(ctx, sqlc.InsertOutboundEventParams{
		TenantID:   e.TenantID,
		AppID:      e.AppID,
		Type:       e.Type,
		MemberID:   uuid.NullUUID{UUID: e.MemberID, Valid: e.MemberID != uuid.Nil},
		Payload:    raw,
		OccurredAt: pgtype.Timestamptz{Time: e.OccurredAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("events: insert %s: %w", e.Type, err)
	}
	return nil
}

// EmitAll writes many member events of one app in one statement, for
// changes that touch many members at once (a webhook delivery). Every event
// needs the same tenant and app and a member.
func EmitAll(ctx context.Context, q *sqlc.Queries, list []Event) error {
	if len(list) == 0 {
		return nil
	}
	params := sqlc.InsertOutboundEventsParams{TenantID: list[0].TenantID, AppID: list[0].AppID}
	for _, e := range list {
		if e.TenantID != params.TenantID || e.AppID != params.AppID || e.MemberID == uuid.Nil {
			return fmt.Errorf("events: a batch needs one tenant and app and a member on every event")
		}
		raw, err := encode(e)
		if err != nil {
			return err
		}
		params.Types = append(params.Types, e.Type)
		params.MemberIds = append(params.MemberIds, e.MemberID)
		params.Payloads = append(params.Payloads, raw)
		params.OccurredAts = append(params.OccurredAts, pgtype.Timestamptz{Time: e.OccurredAt, Valid: true})
	}
	if err := q.InsertOutboundEvents(ctx, params); err != nil {
		return fmt.Errorf("events: insert %d events: %w", len(list), err)
	}
	return nil
}

// encode checks e and returns its payload as JSON.
func encode(e Event) ([]byte, error) {
	if !known[e.Type] {
		return nil, fmt.Errorf("events: unknown type %q", e.Type)
	}
	if e.TenantID == uuid.Nil || e.AppID == uuid.Nil || e.OccurredAt.IsZero() {
		return nil, fmt.Errorf("events: %s needs tenant, app and occurred_at", e.Type)
	}
	payload := e.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	for k := range payload {
		lk := strings.ToLower(k)
		for _, p := range personal {
			if strings.Contains(lk, p) {
				return nil, fmt.Errorf("events: %s payload key %q may carry personal data", e.Type, k)
			}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("events: encode %s: %w", e.Type, err)
	}
	return raw, nil
}
