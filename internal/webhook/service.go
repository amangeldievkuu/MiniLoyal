// Package webhook processes inbound LINE webhooks from each app's Official
// Account: signature check on the raw body, deduplication by webhookEventId,
// and friend status updates for follow and unfollow events. It never calls
// LINE in the request path, so the answer is fast.
package webhook

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/events"
	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Service handles webhook deliveries.
// maxEventAge matches the webhook_inbound_events retention (DeleteOldInboundEvents).
const maxEventAge = 7 * 24 * time.Hour

// Service processes inbound LINE webhooks.
type Service struct {
	pool    *pgxpool.Pool
	tenants *tenant.Service
	log     *slog.Logger
}

// New returns a Service. The tenant service decrypts channel secrets.
func New(pool *pgxpool.Pool, tenants *tenant.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, tenants: tenants, log: log}
}

// Result summarises one delivery.
type Result struct {
	Events     int
	Processed  int
	Duplicates int
	Ignored    int
}

// Receive verifies and applies one webhook delivery for appID. Errors are
// apierr values: 404 for an unknown or disabled app, 401 when the app has no
// channel secret or the signature does not match, 400 for an unreadable body.
func (s *Service) Receive(ctx context.Context, appID uuid.UUID, signature string, body []byte) (Result, error) {
	var app sqlc.App
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		app, err = sqlc.New(tx).GetApp(ctx, appID)
		return err
	})
	if err != nil {
		if db.IsNotFound(err) {
			return Result{}, apierr.NotFound("app")
		}
		return Result{}, fmt.Errorf("webhook: load app: %w", err)
	}
	if app.Status != "active" {
		return Result{}, apierr.NotFound("app")
	}
	secret, ok, err := s.tenants.OAChannelSecret(ctx, app)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, apierr.Unauthorized("this app has no LINE Official Account channel secret configured")
	}
	if !line.VerifySignature(secret, body, signature) {
		s.log.WarnContext(ctx, "webhook signature rejected", "app_id", appID)
		return Result{}, apierr.Unauthorized("bad signature")
	}
	req, err := line.ParseEvents(body)
	if err != nil {
		return Result{}, apierr.BadRequest("webhook body is not a LINE webhook payload").WithCause(err)
	}

	res := Result{Events: len(req.Events)}
	var created []events.Event
	err = db.WithTenant(ctx, s.pool, app.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		for _, ev := range req.Events {
			if ev.WebhookEventID == "" {
				res.Ignored++
				continue
			}
			// Dedupe rows are kept for a week; an older signed delivery
			// could otherwise be replayed once its row is gone.
			if ev.Timestamp > 0 && time.Since(time.UnixMilli(ev.Timestamp)) > maxEventAge {
				res.Ignored++
				continue
			}
			n, err := q.InsertInboundEvent(ctx, sqlc.InsertInboundEventParams{
				TenantID: app.TenantID, AppID: app.ID, LineWebhookEventID: ev.WebhookEventID,
				EventType: ev.Type, Redelivery: ev.DeliveryContext.IsRedelivery,
			})
			if err != nil {
				return fmt.Errorf("webhook: record event: %w", err)
			}
			if n == 0 {
				res.Duplicates++
				continue
			}
			if err := s.apply(ctx, q, app, ev, &res, &created); err != nil {
				return err
			}
		}
		return events.EmitAll(ctx, q, created)
	})
	if err != nil {
		return Result{}, err
	}
	s.log.InfoContext(ctx, "webhook processed", "app_id", appID, "events", res.Events,
		"processed", res.Processed, "duplicates", res.Duplicates, "ignored", res.Ignored)
	return res, nil
}

// apply updates state for one new event. Unknown types are recorded and
// otherwise ignored until a later phase handles them.
// New members are appended to created as member.created events.
func (s *Service) apply(ctx context.Context, q *sqlc.Queries, app sqlc.App, ev line.Event, res *Result, created *[]events.Event) error {
	userID := ""
	if ev.Source != nil && ev.Source.Type == "user" {
		userID = ev.Source.UserID
	}
	switch ev.Type {
	case line.EventFollow:
		if userID == "" {
			res.Ignored++
			return nil
		}
		row, err := q.UpsertMemberFromFollow(ctx, sqlc.UpsertMemberFromFollowParams{
			ID: uuid.Must(uuid.NewV7()), TenantID: app.TenantID, AppID: app.ID, LineUserID: userID,
		})
		if err != nil {
			return fmt.Errorf("webhook: follow: %w", err)
		}
		// A follow can be the first time the brand hears of the member.
		// The events are written together once the delivery is applied.
		if row.Created {
			at := time.Now()
			if ev.Timestamp > 0 {
				at = time.UnixMilli(ev.Timestamp)
			}
			*created = append(*created, events.Event{
				TenantID: app.TenantID, AppID: app.ID, Type: events.MemberCreated, MemberID: row.ID, OccurredAt: at,
				Payload: map[string]any{"member_id": row.ID, "source": "follow"},
			})
		}
		res.Processed++
	case line.EventUnfollow:
		if userID == "" {
			res.Ignored++
			return nil
		}
		if _, err := q.SetMemberFriendByLineUserID(ctx, sqlc.SetMemberFriendByLineUserIDParams{
			AppID: app.ID, LineUserID: userID, IsFriend: false,
		}); err != nil {
			return fmt.Errorf("webhook: unfollow: %w", err)
		}
		res.Processed++
	default:
		res.Ignored++
	}
	return nil
}
