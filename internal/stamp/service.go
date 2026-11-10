// Package stamp owns stamp cards, each member's progress on them and the
// append-only stamp events.
//
// Every stamp or adjustment runs in one tenant transaction that first locks
// the member's progress row (SELECT ... FOR UPDATE), so concurrent stamps on
// one card serialize per member: a completion is never skipped or doubled,
// and the stamps given in one visit (VisitWindow) never pass the card's
// per-visit cap.
// Progress changes only through new stamp_events rows; corrections are new
// rows too. A completion issues the reward coupon in the same transaction
// through internal/coupon; when the reward template is sold out, archived
// or outside its window the completion is still recorded, with no coupon
// and the note reward_unavailable, so the brand can issue it by hand.
package stamp

import (
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Audit actions.
const (
	ActionCardCreate = "stamp_card.create"
	ActionCardUpdate = "stamp_card.update"
	ActionAdjust     = "stamp.adjust"
	ActionAdd        = "stamp.add"
)

// Event reasons.
const (
	ReasonStamp      = "stamp"
	ReasonAdjust     = "adjust"
	ReasonCompletion = "completion"
)

// NoteRewardUnavailable marks a completion whose reward could not be
// issued; ListMemberStampProgress counts these as unrewarded_completions.
const NoteRewardUnavailable = "reward_unavailable"

// VisitWindow is how far back max_stamps_per_visit looks: a member gets at
// most that many stamps on one card in any 30 minutes, whatever the scan,
// so scanning the card again does not open a new visit.
const VisitWindow = 30 * time.Minute

// Bounds.
const (
	MaxStampsPerOperation = 100
	maxCards              = 500
	exportPage            = 500
)

var (
	errCardNotFound     = apierr.NotFound("stamp card")
	errTemplateNotFound = apierr.NotFound("coupon template")
	errInactive         = apierr.New(409, "stamp_card_inactive", "this stamp card is archived and takes no stamps")
	errVisitLimit       = apierr.New(409, "stamp_visit_limit", "the member already got the most stamps one visit allows on this card; try again in 30 minutes")
	errInsufficient     = apierr.New(409, "insufficient_stamps", "the member does not have that many stamps on this card")
	errInUse            = apierr.New(409, "stamp_card_in_use", "stamps_required is fixed once a member has a stamp on the card")
	errSelfService      = apierr.New(403, "self_service", "staff cannot stamp their own member card")
)

// Deps are the service's collaborators.
type Deps struct {
	Pool    *pgxpool.Pool
	Tenants *tenant.Service
	// Keyring encrypts stored idempotent outcomes. Nil stores plain JSON
	// (tests).
	Keyring crypto.Keyring
	// Card verifies the scan tokens staff stamp with.
	Card  *card.Codec
	Clock clock.Clock
	Log   *slog.Logger
	// StaffLimiter is the per-staff write limiter shared with points and
	// coupons. Nil builds one from StaffWritesPerMinute.
	StaffLimiter *auth.Limiter
	// StaffWritesPerMinute: zero means the default (30), negative turns
	// the limit off (tests). Ignored when StaffLimiter is set.
	StaffWritesPerMinute int
}

// Service is the stamp domain.
type Service struct {
	pool       *pgxpool.Pool
	tenants    *tenant.Service
	writer     *access.Writer
	card       *card.Codec
	clock      clock.Clock
	log        *slog.Logger
	staffLimit *auth.Limiter
}

// New returns a Service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	limit := d.StaffLimiter
	if limit == nil {
		limit = access.NewStaffLimiter(d.Clock, d.StaffWritesPerMinute)
	}
	return &Service{
		pool:       d.Pool,
		tenants:    d.Tenants,
		writer:     access.NewWriter(d.Pool, d.Keyring, d.Clock),
		card:       d.Card,
		clock:      d.Clock,
		log:        d.Log,
		staffLimit: limit,
	}
}

// Idem is the client's idempotency key and the request fingerprint.
type Idem = access.Idem

// Card is a stamp card as the dashboard sees it.
type Card struct {
	ID                     uuid.UUID  `json:"id"`
	AppID                  uuid.UUID  `json:"app_id"`
	Name                   string     `json:"name"`
	StampsRequired         int        `json:"stamps_required"`
	RewardCouponTemplateID *uuid.UUID `json:"reward_coupon_template_id,omitempty"`
	MaxStampsPerVisit      int        `json:"max_stamps_per_visit"`
	Status                 string     `json:"status"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// Progress is one member's state on one card.
type Progress struct {
	StampCardID           uuid.UUID       `json:"stamp_card_id"`
	Name                  string          `json:"name"`
	StampsRequired        int             `json:"stamps_required"`
	MaxStampsPerVisit     int             `json:"max_stamps_per_visit"`
	Status                string          `json:"status"`
	Count                 int             `json:"count"`
	Completions           int             `json:"completions"`
	Reward                *coupon.Summary `json:"reward,omitempty"`
	UnrewardedCompletions int             `json:"unrewarded_completions"`
	UpdatedAt             *time.Time      `json:"updated_at,omitempty"`
}

// Result is what one stamp or adjustment did.
type Result struct {
	Progress           Progress          `json:"progress"`
	Added              int               `json:"added"`
	Completed          int               `json:"completed"`
	Rewards            []coupon.Instance `json:"rewards"`
	RewardsUnavailable int               `json:"rewards_unavailable"`
}

// Event is one change to a member's stamps.
type Event struct {
	ID          int64      `json:"id"`
	StampCardID uuid.UUID  `json:"stamp_card_id"`
	Delta       int        `json:"delta"`
	Reason      string     `json:"reason"`
	CountAfter  int        `json:"count_after"`
	StoreID     *uuid.UUID `json:"store_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// MemberData is the stamp part of a member's PDPA export.
type MemberData struct {
	Progress []Progress
	Events   []Event
}

func toCard(c sqlc.StampCard) Card {
	out := Card{
		ID: c.ID, AppID: c.AppID, Name: c.Name, StampsRequired: int(c.StampsRequired),
		MaxStampsPerVisit: int(c.MaxStampsPerVisit), Status: c.Status,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
	}
	if c.RewardCouponTemplateID.Valid {
		id := c.RewardCouponTemplateID.UUID
		out.RewardCouponTemplateID = &id
	}
	return out
}

func toProgress(r sqlc.ListMemberStampProgressRow) Progress {
	out := Progress{
		StampCardID: r.StampCardID, Name: r.Name, StampsRequired: int(r.StampsRequired),
		MaxStampsPerVisit: int(r.MaxStampsPerVisit), Status: r.Status, Count: int(r.Count),
		Completions: int(r.Completions), UnrewardedCompletions: int(r.UnrewardedCompletions),
	}
	if r.RewardCouponTemplateID.Valid && r.RewardName != nil && r.RewardType != nil {
		out.Reward = &coupon.Summary{
			ID: r.RewardCouponTemplateID.UUID, Name: *r.RewardName, Description: r.RewardDescription,
			Type: *r.RewardType, Value: intOf(r.RewardValue),
		}
	}
	if r.ProgressUpdatedAt.Valid {
		t := r.ProgressUpdatedAt.Time
		out.UpdatedAt = &t
	}
	return out
}

func toEvent(e sqlc.StampEvent) Event {
	out := Event{
		ID: e.ID, StampCardID: e.StampCardID, Delta: int(e.Delta), Reason: e.Reason,
		CountAfter: int(e.CountAfter), CreatedAt: e.CreatedAt.Time,
	}
	if e.StoreID.Valid {
		id := e.StoreID.UUID
		out.StoreID = &id
	}
	return out
}

func intOf(v *int32) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
