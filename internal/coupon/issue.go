package coupon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/events"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// codeAlphabet is Crockford's base 32: no I, L, O or U, so a code read out
// at the counter cannot be confused.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeLen gives 32^8 (about 10^12) codes per app.
const codeLen = 8

// codeAttempts bounds retries on a code collision.
const codeAttempts = 5

// newCode returns a random coupon code matching ^[0-9A-Z]{6,12}$.
func newCode() (string, error) {
	var b [codeLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("coupon: code: %w", err)
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b[:]), nil
}

// issuer is who issues a coupon and why.
type issuer struct {
	source    string
	actorType string
	actorID   uuid.UUID
	// txnID groups the coupon with the rows of the same operation (a stamp
	// completion); uuid.Nil starts a new one.
	txnID uuid.UUID
}

// issuable checks the template under its lock. Claims also need claimable;
// every source needs an active template inside its window with stock left.
func issuable(t sqlc.CouponTemplate, source string, now time.Time) error {
	switch {
	case t.Status != "active":
		return errNotClaimable
	case source == SourceClaim && !t.Claimable:
		return errNotClaimable
	case t.ValidFrom.Valid && now.Before(t.ValidFrom.Time):
		return errNotClaimable
	case t.ValidTo.Valid && !now.Before(t.ValidTo.Time):
		return errNotClaimable
	case t.TotalLimit != nil && t.ClaimedCount >= *t.TotalLimit:
		return errSoldOut
	}
	return nil
}

// expiryOf is the earlier of valid_to and issuedAt plus validity_days, or
// nil when the template sets neither.
func expiryOf(t sqlc.CouponTemplate, issuedAt time.Time) *time.Time {
	var out *time.Time
	if t.ValidityDays != nil {
		e := issuedAt.Add(time.Duration(*t.ValidityDays) * 24 * time.Hour)
		out = &e
	}
	if t.ValidTo.Valid && (out == nil || t.ValidTo.Time.Before(*out)) {
		e := t.ValidTo.Time
		out = &e
	}
	return out
}

// lockTemplate locks the member's app's template for an issue.
func lockTemplate(ctx context.Context, q *sqlc.Queries, appID, templateID uuid.UUID) (sqlc.CouponTemplate, error) {
	t, err := q.LockCouponTemplate(ctx, sqlc.LockCouponTemplateParams{AppID: appID, ID: templateID})
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.CouponTemplate{}, errTemplateNotFound
		}
		return sqlc.CouponTemplate{}, fmt.Errorf("coupon: lock template: %w", err)
	}
	return t, nil
}

// issue gives m one coupon from templateID inside tx, which must be scoped
// to m's tenant. It locks the template first, so concurrent issues of one
// template serialize and every limit is checked against committed rows.
func issue(ctx context.Context, tx pgx.Tx, m sqlc.Member, templateID uuid.UUID, by issuer, now time.Time) (Instance, error) {
	q := sqlc.New(tx)
	t, err := lockTemplate(ctx, q, m.AppID, templateID)
	if err != nil {
		return Instance{}, err
	}
	if err := issuable(t, by.source, now); err != nil {
		return Instance{}, err
	}
	if by.source == SourceClaim {
		src := SourceClaim
		held, err := q.CountMemberCouponInstances(ctx, sqlc.CountMemberCouponInstancesParams{
			AppID: m.AppID, TemplateID: t.ID, MemberID: m.ID, Source: &src,
		})
		if err != nil {
			return Instance{}, fmt.Errorf("coupon: count held: %w", err)
		}
		if held >= t.PerMemberLimit {
			return Instance{}, errLimitReached
		}
	}
	return insert(ctx, tx, m, t, by, now)
}

// insert counts the coupon against the locked template and writes it with
// a fresh code, then emits coupon.issued. The count comes first, so a
// refused increment leaves nothing behind.
func insert(ctx context.Context, tx pgx.Tx, m sqlc.Member, t sqlc.CouponTemplate, by issuer, now time.Time) (Instance, error) {
	q := sqlc.New(tx)
	if _, err := q.IncrementCouponTemplateClaimed(ctx, sqlc.IncrementCouponTemplateClaimedParams{AppID: t.AppID, ID: t.ID}); err != nil {
		if db.IsNotFound(err) {
			// The guard behind the check made under the lock.
			return Instance{}, errSoldOut
		}
		return Instance{}, fmt.Errorf("coupon: count issue: %w", err)
	}
	txnID := by.txnID
	if txnID == uuid.Nil {
		txnID = uuid.Must(uuid.NewV7())
	}
	params := sqlc.InsertCouponInstanceParams{
		ID: uuid.Must(uuid.NewV7()), TenantID: m.TenantID, AppID: m.AppID, TemplateID: t.ID, MemberID: m.ID,
		Source: by.source, TxnID: txnID, IssuedAt: ts(now), ExpiresAt: tsOf(expiryOf(t, now)),
		IssuedByType: by.actorType, IssuedByID: access.NullUUID(by.actorID),
	}
	var row sqlc.CouponInstance
	for attempt := 1; ; attempt++ {
		code, err := newCode()
		if err != nil {
			return Instance{}, err
		}
		params.Code = code
		// A unique violation aborts the transaction, so each attempt runs
		// in a savepoint that a collision rolls back.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return Instance{}, fmt.Errorf("coupon: savepoint: %w", err)
		}
		row, err = sqlc.New(sp).InsertCouponInstance(ctx, params)
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return Instance{}, fmt.Errorf("coupon: release savepoint: %w", err)
			}
			break
		}
		_ = sp.Rollback(ctx)
		if !db.IsUniqueViolation(err, "coupon_instances_app_id_code_key") || attempt == codeAttempts {
			return Instance{}, fmt.Errorf("coupon: insert: %w", err)
		}
	}
	inst := instanceOf(row, summaryOf(t), nil, now)
	payload := map[string]any{
		"coupon_id": row.ID, "template_id": t.ID, "member_id": m.ID, "source": by.source,
	}
	if inst.ExpiresAt != nil {
		payload["expires_at"] = *inst.ExpiresAt
	}
	if err := events.Emit(ctx, q, events.Event{
		TenantID: m.TenantID, AppID: m.AppID, Type: events.CouponIssued, MemberID: m.ID, OccurredAt: now, Payload: payload,
	}); err != nil {
		return Instance{}, err
	}
	return inst, nil
}

// IssueReward issues a stamp card reward to m inside the caller's
// transaction (tx, scoped to m's tenant), as part of the stamp operation
// txnID. Rewards ignore claimable and per_member_limit but respect the
// template's status, window and total_limit: when those refuse, it returns
// ok false and writes nothing, and the caller records the completion
// without a coupon.
func IssueReward(ctx context.Context, tx pgx.Tx, m sqlc.Member, templateID uuid.UUID, actorType string, actorID, txnID uuid.UUID, now time.Time) (inst Instance, ok bool, err error) {
	q := sqlc.New(tx)
	t, err := lockTemplate(ctx, q, m.AppID, templateID)
	if err != nil {
		return Instance{}, false, err
	}
	if err := issuable(t, SourceReward, now); err != nil {
		if errors.Is(err, errNotClaimable) || errors.Is(err, errSoldOut) {
			return Instance{}, false, nil
		}
		return Instance{}, false, err
	}
	inst, err = insert(ctx, tx, m, t, issuer{source: SourceReward, actorType: actorType, actorID: actorID, txnID: txnID}, now)
	if err != nil {
		return Instance{}, false, err
	}
	return inst, true, nil
}

// Claim gives the signed-in member one coupon from an offer.
func (s *Service) Claim(ctx context.Context, p principal.Principal, templateID uuid.UUID, idem Idem) (Instance, error) {
	if !p.IsMember() {
		return Instance{}, access.ErrMemberNotFound
	}
	if err := allow(s.claimLimit, "member:"+p.MemberID.String()); err != nil {
		return Instance{}, err
	}
	acc, err := access.Reach(ctx, s.pool, s.tenants, p, p.MemberID, principal.RoleStaff)
	if err != nil {
		return Instance{}, err
	}
	var m sqlc.Member
	var out Instance
	err = s.writer.Write(ctx, acc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.Check(ctx, tx); return err },
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			actorType, actorID := access.ActorOf(p)
			return issue(ctx, tx, m, templateID, issuer{source: SourceClaim, actorType: actorType, actorID: actorID}, s.clock.Now())
		}, &out)
	return out, err
}

// Issue gives a member a coupon on behalf of a dashboard owner or admin or
// an API key of the member's app; both are audited, and API keys are rate
// limited per key. claimable and per_member_limit do not apply;
// total_limit and the window do.
func (s *Service) Issue(ctx context.Context, p principal.Principal, memberID, templateID uuid.UUID, idem Idem) (Instance, error) {
	if !p.IsUser() && !p.IsAPIKey() {
		return Instance{}, access.ErrMemberNotFound
	}
	if p.IsAPIKey() {
		if err := access.AllowAPIKey(s.issueLimit, p); err != nil {
			return Instance{}, err
		}
	}
	acc, err := access.Reach(ctx, s.pool, s.tenants, p, memberID, principal.RoleAdmin)
	if err != nil {
		return Instance{}, err
	}
	var m sqlc.Member
	var out Instance
	err = s.writer.Write(ctx, acc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.Check(ctx, tx); return err },
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			actorType, actorID := access.ActorOf(p)
			inst, err := issue(ctx, tx, m, templateID, issuer{source: SourceIssue, actorType: actorType, actorID: actorID}, s.clock.Now())
			if err != nil {
				return nil, err
			}
			appID := m.AppID
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: m.TenantID, AppID: &appID, Actor: p, Action: ActionIssue,
				TargetType: "member", TargetID: m.ID.String(),
				Metadata: map[string]any{"coupon_id": inst.ID.String(), "template_id": templateID.String()},
			}); err != nil {
				return nil, err
			}
			return inst, nil
		}, &out)
	return out, err
}
