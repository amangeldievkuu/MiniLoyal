package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
)

// Security event names. They are shown to the user on the profile page, so
// they describe what happened to the account, never how.
const (
	EventLoginSuccess    = "login.success"
	EventLoginFailed     = "login.failed"
	EventMFAFailed       = "login.mfa_failed"
	EventMFAExhausted    = "login.mfa_exhausted"
	EventPasswordChanged = "password.changed"
	EventTOTPEnabled     = "totp.enabled"
	EventTOTPDisabled    = "totp.disabled"
	EventSessionsEnded   = "sessions.ended"
	EventRefreshReplayed = "refresh.replayed"
	EventInviteAccepted  = "invite.accepted"
)

// SecurityEvent is one entry of the account's security history.
type SecurityEvent struct {
	ID        int64
	Event     string
	IP        *string
	UserAgent *string
	Metadata  map[string]any
	CreatedAt time.Time
}

// SecurityEventPage is one page of events, newest first.
type SecurityEventPage struct {
	Items      []SecurityEvent
	NextCursor string
}

type clientCtxKey struct{}

// WithClient stores the calling device in ctx for methods that do not take
// a Client parameter, so their security events still carry IP and agent.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientCtxKey{}, c)
}

// ClientFrom returns the device stored by WithClient, or a zero Client.
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientCtxKey{}).(Client)
	return c
}

// recordEvent inserts a security event through q, which may be a
// transaction so the event commits with the change it describes.
func (s *Service) recordEvent(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, event string, c Client, meta map[string]any) error {
	if c.IP == "" && c.UserAgent == "" {
		c = ClientFrom(ctx)
	}
	var ip *netip.Addr
	if a, err := netip.ParseAddr(c.IP); err == nil {
		ip = &a
	}
	var ua *string
	if c.UserAgent != "" {
		v := c.UserAgent
		ua = &v
	}
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("auth: encode event metadata: %w", err)
	}
	if err := q.InsertSecurityEvent(ctx, sqlc.InsertSecurityEventParams{
		UserID: userID, Event: event, Ip: ip, UserAgent: ua, Metadata: raw,
	}); err != nil {
		return fmt.Errorf("auth: record security event: %w", err)
	}
	return nil
}

// noteEvent records an event outside any transaction and only logs when it
// fails: a failed attempt must still be answered.
func (s *Service) noteEvent(ctx context.Context, userID uuid.UUID, event string, c Client, meta map[string]any) {
	if err := s.recordEvent(ctx, s.q, userID, event, c, meta); err != nil {
		s.log.WarnContext(ctx, "security event not recorded", "event", event, "user_id", userID, "err", err)
	}
}

// ListSecurityEvents returns a page of the user's own security history.
func (s *Service) ListSecurityEvents(ctx context.Context, userID uuid.UUID, limit int, cursor string) (SecurityEventPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var before *int64
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return SecurityEventPage{}, apierr.BadRequest("invalid cursor")
		}
		id, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return SecurityEventPage{}, apierr.BadRequest("invalid cursor")
		}
		before = &id
	}
	rows, err := s.q.ListSecurityEvents(ctx, sqlc.ListSecurityEventsParams{UserID: userID, BeforeID: before, PageLimit: int32(limit)})
	if err != nil {
		return SecurityEventPage{}, fmt.Errorf("auth: list security events: %w", err)
	}
	page := SecurityEventPage{Items: make([]SecurityEvent, 0, len(rows))}
	for _, r := range rows {
		e := SecurityEvent{ID: r.ID, Event: r.Event, UserAgent: r.UserAgent, CreatedAt: r.CreatedAt.Time, Metadata: map[string]any{}}
		if r.Ip != nil {
			v := r.Ip.String()
			e.IP = &v
		}
		if len(r.Metadata) > 0 {
			_ = json.Unmarshal(r.Metadata, &e.Metadata)
		}
		page.Items = append(page.Items, e)
	}
	if len(rows) == limit {
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(rows[len(rows)-1].ID, 10)))
	}
	return page, nil
}
