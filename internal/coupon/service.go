// Package coupon owns coupon templates and the coupons members hold.
//
// Every issue (a member's claim, a dashboard or API issue, a stamp card
// reward) runs under SELECT ... FOR UPDATE on the template row, so
// total_limit and per_member_limit hold under any number of concurrent
// requests. A redeem is one conditional UPDATE, so of many concurrent
// attempts on one coupon exactly one succeeds. Expiry is computed from
// expires_at against the service clock; no job flips a status.
package coupon

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
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Audit actions.
const (
	ActionTemplateCreate = "coupon_template.create"
	ActionTemplateUpdate = "coupon_template.update"
	ActionIssue          = "coupon.issue"
	ActionVoid           = "coupon.void"
	ActionRedeem         = "coupon.redeem"
)

// Sources of an issued coupon.
const (
	SourceClaim  = "claim"
	SourceIssue  = "issue"
	SourceReward = "stamp_reward"
)

// Template types.
const (
	TypeAmountOff  = "amount_off"
	TypePercentOff = "percent_off"
	TypeFreeItem   = "free_item"
)

// Stored statuses and effective states of a coupon.
const (
	StatusIssued   = "issued"
	StatusRedeemed = "redeemed"
	StatusVoided   = "voided"

	StateAvailable = "available"
	StateExpired   = "expired"
)

// Paging limits.
const (
	DefaultLimit = 50
	MaxLimit     = 100
	// MaxRedeemable bounds the coupons a scan shows.
	MaxRedeemable = 50
	// maxTemplates bounds the dashboard template list.
	maxTemplates = 500
	exportPage   = 500
)

var (
	// errUnavailable is the one answer for every coupon a redeem or void
	// cannot touch, so the code reveals nothing about other members or apps.
	errUnavailable      = apierr.New(409, "coupon_unavailable", "this coupon cannot be used: it is redeemed, voided, expired or not this member's")
	errNotClaimable     = apierr.New(409, "coupon_not_claimable", "this coupon cannot be claimed or issued now")
	errSoldOut          = apierr.New(409, "coupon_sold_out", "no coupons of this kind are left")
	errLimitReached     = apierr.New(409, "coupon_limit_reached", "you already hold as many of this coupon as allowed")
	errInUse            = apierr.New(409, "coupon_template_in_use", "type and value are fixed once a coupon was issued from the template")
	errSelfService      = apierr.New(403, "self_service", "staff cannot redeem coupons on their own member card")
	errTemplateNotFound = apierr.NotFound("coupon template")
	errCouponNotFound   = apierr.NotFound("coupon")
)

// Deps are the service's collaborators.
type Deps struct {
	Pool    *pgxpool.Pool
	Tenants *tenant.Service
	// Keyring encrypts stored idempotent outcomes. Nil stores plain JSON
	// (tests).
	Keyring crypto.Keyring
	// Card verifies the scan tokens staff redeem with.
	Card  *card.Codec
	Clock clock.Clock
	Log   *slog.Logger
	// StaffLimiter is the per-staff write limiter shared with points and
	// stamps. Nil builds one from StaffWritesPerMinute.
	StaffLimiter *auth.Limiter
	// StaffWritesPerMinute: zero means the default (30), negative turns
	// the limit off (tests). Ignored when StaffLimiter is set.
	StaffWritesPerMinute int
}

// Service is the coupon domain.
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

// Summary is what a coupon is worth, as members and staff see it.
type Summary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	Type        string    `json:"type"`
	Value       *int      `json:"value"`
}

// StoreRef names the store a coupon was redeemed at.
type StoreRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Instance is one coupon a member holds or held.
type Instance struct {
	ID         uuid.UUID  `json:"id"`
	Code       string     `json:"code"`
	Template   Summary    `json:"template"`
	MemberID   uuid.UUID  `json:"member_id"`
	Status     string     `json:"status"`
	State      string     `json:"state"`
	Source     string     `json:"source"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
	Store      *StoreRef  `json:"store,omitempty"`
}

// Template is a coupon template as the dashboard sees it.
type Template struct {
	ID             uuid.UUID  `json:"id"`
	AppID          uuid.UUID  `json:"app_id"`
	Name           string     `json:"name"`
	Description    *string    `json:"description,omitempty"`
	Type           string     `json:"type"`
	Value          *int       `json:"value"`
	TotalLimit     *int       `json:"total_limit,omitempty"`
	PerMemberLimit int        `json:"per_member_limit"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	ValidTo        *time.Time `json:"valid_to,omitempty"`
	ValidityDays   *int       `json:"validity_days,omitempty"`
	Claimable      bool       `json:"claimable"`
	ClaimedCount   int        `json:"claimed_count"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Offer is a template the member can claim now.
type Offer struct {
	Template       Summary
	ValidTo        *time.Time
	ValidityDays   *int
	PerMemberLimit int
	Held           int
	CanClaim       bool
}

// Page is one page of coupons, newest first.
type Page struct {
	Items      []Instance
	NextCursor string
}

// stateOf is the effective state of a coupon at now.
func stateOf(status string, expiresAt pgtype.Timestamptz, now time.Time) string {
	if status != StatusIssued {
		return status
	}
	if expiresAt.Valid && !now.Before(expiresAt.Time) {
		return StateExpired
	}
	return StateAvailable
}

func toTemplate(t sqlc.CouponTemplate) Template {
	return Template{
		ID: t.ID, AppID: t.AppID, Name: t.Name, Description: t.Description, Type: t.Type,
		Value: intOf(t.Value), TotalLimit: intOf(t.TotalLimit), PerMemberLimit: int(t.PerMemberLimit),
		ValidFrom: timeOf(t.ValidFrom), ValidTo: timeOf(t.ValidTo), ValidityDays: intOf(t.ValidityDays),
		Claimable: t.Claimable, ClaimedCount: int(t.ClaimedCount), Status: t.Status,
		CreatedAt: t.CreatedAt.Time, UpdatedAt: t.UpdatedAt.Time,
	}
}

func summaryOf(t sqlc.CouponTemplate) Summary {
	return Summary{ID: t.ID, Name: t.Name, Description: t.Description, Type: t.Type, Value: intOf(t.Value)}
}

// instanceOf maps a coupon row; storeName is the redeeming store's name
// when it still exists.
func instanceOf(r sqlc.CouponInstance, t Summary, storeName *string, now time.Time) Instance {
	out := Instance{
		ID: r.ID, Code: r.Code, Template: t, MemberID: r.MemberID, Status: r.Status,
		State: stateOf(r.Status, r.ExpiresAt, now), Source: r.Source, IssuedAt: r.IssuedAt.Time,
		ExpiresAt: timeOf(r.ExpiresAt), RedeemedAt: timeOf(r.RedeemedAt),
	}
	if r.RedeemedAtStoreID.Valid && storeName != nil {
		out.Store = &StoreRef{ID: r.RedeemedAtStoreID.UUID, Name: *storeName}
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

func int32Of(v *int) *int32 {
	if v == nil {
		return nil
	}
	x := int32(*v)
	return &x
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func tsOf(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return ts(*t)
}
