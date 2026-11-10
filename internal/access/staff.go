package access

import (
	"context"
	"fmt"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Scan errors shared by every staff write.
var (
	ErrScanInvalid  = apierr.New(403, "scan_invalid", "scan the member card again")
	ErrScanMismatch = apierr.New(403, "scan_mismatch", "this scan belongs to another staff member or app")
)

// DefaultStaffWritesPerMinute limits staff writes (points, coupons and
// stamps together) per staff member.
const DefaultStaffWritesPerMinute = 30

// NewStaffLimiter returns the per-staff write limiter. Zero means the
// default; a negative value turns the limit off and returns nil (tests).
// One limiter is shared by every service that takes staff writes, so the
// limit counts them together.
func NewStaffLimiter(clk clock.Clock, perMinute int) *auth.Limiter {
	switch {
	case perMinute == 0:
		return auth.NewLimiter(clk, DefaultStaffWritesPerMinute, DefaultStaffWritesPerMinute)
	case perMinute > 0:
		return auth.NewLimiter(clk, perMinute, perMinute)
	default:
		return nil
	}
}

// AllowStaff spends one write of p's allowance; nil l allows everything.
func AllowStaff(l *auth.Limiter, p principal.Principal) error {
	if l == nil {
		return nil
	}
	if wait, ok := l.Allow("staff:" + p.StaffID.String()); !ok {
		return apierr.RateLimited(wait)
	}
	return nil
}

// VerifyScan checks the scan token's MAC and that it was issued to this
// staff member of this app. Its lifetime is checked only when a write is
// made for the first time (see ScanFresh), so an idempotent retry of a
// write that already happened replays even after the token ran out.
func VerifyScan(codec *card.Codec, p principal.Principal, token string) (card.ScanClaims, error) {
	if !p.IsStaff() {
		return card.ScanClaims{}, apierr.Unauthorized("sign in to the staff app")
	}
	if codec == nil {
		return card.ScanClaims{}, apierr.Unavailable("member cards are not configured")
	}
	cl, err := codec.VerifyScanTokenIgnoringExpiry(token)
	if err != nil {
		return card.ScanClaims{}, ErrScanInvalid
	}
	if cl.StaffID != p.StaffID || cl.AppID != p.AppID || cl.TenantID != p.TenantID {
		return card.ScanClaims{}, ErrScanMismatch
	}
	return cl, nil
}

// StaffMember loads the scanned member inside the staff member's tenant
// transaction and refuses the staff member's own card with selfService.
// Staff and members of one app share a LINE provider, so the same LINE
// account means the staff member's own card. (The session already proved
// the staff row exists; a missing row cannot be the member.)
func StaffMember(ctx context.Context, q *sqlc.Queries, p principal.Principal, cl card.ScanClaims, selfService error) (sqlc.Member, error) {
	m, err := LoadMember(ctx, q, cl.MemberID, p.AppID)
	if err != nil {
		return sqlc.Member{}, err
	}
	st, err := q.GetStaff(ctx, p.StaffID)
	switch {
	case err == nil:
		if st.LineUserID != nil && *st.LineUserID != "" && *st.LineUserID == m.LineUserID {
			return sqlc.Member{}, selfService
		}
	case !db.IsNotFound(err):
		return sqlc.Member{}, fmt.Errorf("access: load staff: %w", err)
	}
	return m, nil
}

// ScanFresh checks, before a first write, that the scan has not expired and
// was made under the member's current card key.
func ScanFresh(cl card.ScanClaims, m sqlc.Member, now time.Time) error {
	if cl.Expired(now) || cl.KeyVersion != int(m.QrKeyVersion) {
		return ErrScanInvalid
	}
	return nil
}
