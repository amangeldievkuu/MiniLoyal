package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/staffauth"
)

// cardOf returns the member's current QR payload.
func (e *memberEnv) cardOf(memberToken string) api.MemberCard {
	e.t.Helper()
	var c api.MemberCard
	e.must(e.do(http.MethodGet, "/v1/me/card", nil, bearer(memberToken)), http.StatusOK, "card").into(e.t, &c)
	return c
}

func (e *memberEnv) scanQR(staffToken, qr string) resp {
	e.t.Helper()
	return e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": qr}, bearer(staffToken))
}

// scanToken scans the member's card as the staff session.
func (e *memberEnv) scanToken(staffToken, memberToken string) string {
	e.t.Helper()
	var res api.ScanResult
	e.must(e.scanQR(staffToken, e.cardOf(memberToken).Qr), http.StatusOK, "scan").into(e.t, &res)
	return res.ScanToken
}

func (e *memberEnv) staffEarn(staffToken, scanToken, key string, spend int) resp {
	e.t.Helper()
	h := bearer(staffToken)
	h["Idempotency-Key"] = key
	return e.do(http.MethodPost, "/v1/staff/points/earn", map[string]any{"scan_token": scanToken, "spend_amount": spend}, h)
}

func wantCode(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d; body %s", r.status, status, r.body)
	}
	if code != "" {
		if got := errCode(t, r).Code; got != code {
			t.Fatalf("code = %s, want %s", got, code)
		}
	}
}

// Invariant: a scan token authorizes one staff member to act on one member
// for five minutes, and no longer than the member and the staff session
// exist. The cases document what happens to a token issued before each
// change.
func TestScanTokenLifecycle(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	mei := e.staffInvite(owner, app, "Mei")
	meiSess := e.staffSession(app, "Umei", mei.InviteCode)
	kenji := e.staffInvite(owner, app, "Kenji")
	kenjiSess := e.staffSession(app, "Ukenji", kenji.InviteCode)

	t.Run("another staff member of the same app", func(t *testing.T) {
		m := e.lineLogin(app, "Ushared", "Shared")
		tok := e.scanToken(meiSess.SessionToken, m.SessionToken)
		wantCode(t, e.staffEarn(kenjiSess.SessionToken, tok, uuid.NewString(), 500), http.StatusForbidden, "scan_mismatch")
		e.must(e.staffEarn(meiSess.SessionToken, tok, uuid.NewString(), 500), http.StatusOK, "the scanner itself")
	})

	t.Run("member erased after the scan", func(t *testing.T) {
		m := e.lineLogin(app, "Uerased", "Erased")
		tok := e.scanToken(meiSess.SessionToken, m.SessionToken)
		e.must(e.do(http.MethodDelete, "/v1/dashboard/members/"+m.Member.Id.String(), nil, bearer(owner)), http.StatusNoContent, "erase")
		r := e.staffEarn(meiSess.SessionToken, tok, uuid.NewString(), 500)
		wantCode(t, r, http.StatusNotFound, "not_found")
		if bytes.Contains(r.body, []byte(m.Member.Id.String())) {
			t.Errorf("refusal names the erased member: %s", r.body)
		}
	})

	t.Run("member erased themselves after the scan", func(t *testing.T) {
		m := e.lineLogin(app, "Uselferased", "Self")
		tok := e.scanToken(meiSess.SessionToken, m.SessionToken)
		e.must(e.do(http.MethodDelete, "/v1/me", nil, bearer(m.SessionToken)), http.StatusNoContent, "erase")
		wantCode(t, e.staffEarn(meiSess.SessionToken, tok, uuid.NewString(), 500), http.StatusNotFound, "not_found")
	})

	// Rotation invalidates every QR code the card showed, so nobody can scan
	// it again; a scan token issued before the rotation stays valid for the
	// rest of its five minutes, because it is a staff-side credential bound
	// to the staff member who scanned, not to the QR key version.
	t.Run("card rotated after the scan", func(t *testing.T) {
		m := e.lineLogin(app, "Urotated", "Rotated")
		oldQR := e.cardOf(m.SessionToken).Qr
		tok := e.scanToken(meiSess.SessionToken, m.SessionToken)
		e.must(e.do(http.MethodPost, "/v1/dashboard/members/"+m.Member.Id.String()+"/card/rotate", nil, idemHeaders(owner)), http.StatusOK, "rotate")
		wantCode(t, e.scanQR(meiSess.SessionToken, oldQR), http.StatusUnprocessableEntity, "invalid_card")
		e.must(e.staffEarn(meiSess.SessionToken, tok, uuid.NewString(), 500), http.StatusOK, "scan token issued before the rotation")
	})

	t.Run("staff member disabled after the scan", func(t *testing.T) {
		st := e.staffInvite(owner, app, "Disabled")
		sess := e.staffSession(app, "Udisabled", st.InviteCode)
		m := e.lineLogin(app, "Udis", "Dis")
		tok := e.scanToken(sess.SessionToken, m.SessionToken)
		e.must(e.do(http.MethodPatch, "/v1/dashboard/staff/"+st.Id.String(), map[string]any{"status": "disabled"}, idemHeaders(owner)), http.StatusOK, "disable")
		wantCode(t, e.staffEarn(sess.SessionToken, tok, uuid.NewString(), 500), http.StatusUnauthorized, "unauthorized")
	})

	// A reset ends the old phone's session, so the token on that phone is
	// useless. The token itself names only the staff record, so the same
	// staff record signed in on a new phone could still use it within its
	// five minutes; the new phone never received it.
	t.Run("staff invite reset after the scan", func(t *testing.T) {
		st := e.staffInvite(owner, app, "Reset")
		old := e.staffSession(app, "Uoldphone", st.InviteCode)
		m := e.lineLogin(app, "Ureset", "Reset member")
		tok := e.scanToken(old.SessionToken, m.SessionToken)
		e.clk.Advance(time.Second)
		var fresh api.StaffCreated
		e.must(e.do(http.MethodPost, "/v1/dashboard/staff/"+st.Id.String()+"/invite", nil, idemHeaders(owner)), http.StatusOK, "reset").into(t, &fresh)
		wantCode(t, e.staffEarn(old.SessionToken, tok, uuid.NewString(), 500), http.StatusUnauthorized, "unauthorized")
		newPhone := e.staffSession(app, "Unewphone", fresh.InviteCode)
		e.must(e.staffEarn(newPhone.SessionToken, tok, uuid.NewString(), 500), http.StatusOK, "same staff record, new phone, old scan token")
	})

	// Authorization, including the scan token, runs before a replay: a
	// retry after the token expired is refused with scan_invalid instead of
	// replaying the stored outcome. The till has to scan again, and a new
	// scan token is a new body, so the old key is a mismatch.
	t.Run("earn retried with the same key", func(t *testing.T) {
		m := e.lineLogin(app, "Uretry", "Retry")
		tok := e.scanToken(meiSess.SessionToken, m.SessionToken)
		key := uuid.NewString()
		first := e.must(e.staffEarn(meiSess.SessionToken, tok, key, 700), http.StatusOK, "first")
		e.clk.Advance(card.ScanTokenTTL - time.Second)
		replay := e.must(e.staffEarn(meiSess.SessionToken, tok, key, 700), http.StatusOK, "replay within the token's life")
		if !bytes.Equal(first.body, replay.body) {
			t.Errorf("replay differs:\n%s\n%s", first.body, replay.body)
		}
		e.clk.Advance(time.Second)
		wantCode(t, e.staffEarn(meiSess.SessionToken, tok, key, 700), http.StatusForbidden, "scan_invalid")
		fresh := e.scanToken(meiSess.SessionToken, m.SessionToken)
		wantCode(t, e.staffEarn(meiSess.SessionToken, fresh, key, 700), http.StatusUnprocessableEntity, "idempotency_mismatch")
		var balance int64
		if err := e.db.Admin.QueryRow(context.Background(), "SELECT balance FROM points_accounts WHERE member_id = $1", uuid.UUID(m.Member.Id)).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if balance != 7 {
			t.Errorf("balance = %d, want 7 (one earn)", balance)
		}
	})
}

// Invariant: a QR code from window w is accepted until the last second of
// window w+1 and refused from the first second of w+2, which is the card's
// expires_at; a scanner whose clock runs one window behind still accepts
// it, two windows behind does not.
func TestQRWindowBoundariesOverHTTP(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Ustaff", st.InviteCode)
	e.clk.Advance(5 * time.Minute) // sessions are well in the past for the backward steps
	member := e.lineLogin(app, "Umember", "Lin")

	// Move to 10 seconds into a window.
	now := e.clk.Now()
	e.clk.Advance(time.Duration(30-now.Unix()%30)*time.Second + 10*time.Second - time.Duration(now.Nanosecond()))
	windowStart := e.clk.Now().Add(-10 * time.Second)
	c := e.cardOf(member.SessionToken)
	if !c.ExpiresAt.Equal(windowStart.Add(2 * card.Window)) {
		t.Fatalf("expires_at = %s, want %s", c.ExpiresAt, windowStart.Add(2*card.Window))
	}
	if c.RefreshInSeconds != 20 {
		t.Errorf("refresh_in_seconds = %d, want 20", c.RefreshInSeconds)
	}

	steps := []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"last second of w+1", windowStart.Add(59 * time.Second), true},
		{"first second of w+2 (expires_at)", windowStart.Add(60 * time.Second), false},
		{"last nanosecond of w+1", windowStart.Add(60*time.Second - time.Nanosecond), true},
		{"first second of w", windowStart, true},
		{"last second of w-1", windowStart.Add(-time.Second), true},
		{"first second of w-1", windowStart.Add(-30 * time.Second), true},
		{"last second of w-2", windowStart.Add(-31 * time.Second), false},
	}
	for _, s := range steps {
		e.clk.Advance(s.at.Sub(e.clk.Now()))
		r := e.scanQR(staff.SessionToken, c.Qr)
		if s.ok {
			if r.status != http.StatusOK {
				t.Errorf("%s: status = %d, want 200; body %s", s.name, r.status, r.body)
			}
			continue
		}
		if r.status != http.StatusUnprocessableEntity || errCode(t, r).Code != "card_expired" {
			t.Errorf("%s: status = %d, want 422 card_expired; body %s", s.name, r.status, r.body)
		}
	}
}

// Invariant: an invite code binds exactly one LINE account. Two phones
// racing with the same code: one is signed in, the other refused, and the
// binding is audited once.
func TestConcurrentStaffBindingsWithOneCodeOneWins(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	const rounds = 10
	for i := 0; i < rounds; i++ {
		st := e.staffInvite(owner, app, "Racer")
		subs := []string{"Uracer-a-" + uuid.NewString(), "Uracer-b-" + uuid.NewString()}
		bodies := make([]map[string]any, len(subs))
		for j, sub := range subs {
			bodies[j] = map[string]any{"app_id": app.Id, "id_token": e.staffIDToken(app, sub), "invite_code": st.InviteCode}
		}
		results := make([]resp, len(subs))
		release := make(chan struct{})
		var wg sync.WaitGroup
		for j := range subs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-release
				results[j] = e.do(http.MethodPost, "/v1/staff/auth/line", bodies[j], nil)
			}()
		}
		close(release)
		wg.Wait()

		winner := -1
		for j, r := range results {
			switch r.status {
			case http.StatusOK:
				if winner >= 0 {
					t.Fatalf("round %d: both phones bound the code", i)
				}
				winner = j
			case http.StatusUnauthorized:
			default:
				t.Fatalf("round %d phone %d: %d %s", i, j, r.status, r.body)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: neither phone bound the code: %s / %s", i, results[0].body, results[1].body)
		}
		var line string
		var bound int
		ctx := context.Background()
		if err := e.db.Admin.QueryRow(ctx, "SELECT line_user_id FROM staff WHERE id = $1", uuid.UUID(st.Id)).Scan(&line); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Admin.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'staff.login_bound' AND target_id = $1", st.Id.String()).Scan(&bound); err != nil {
			t.Fatal(err)
		}
		if line != subs[winner] || bound != 1 {
			t.Fatalf("round %d: bound to %q (winner %q), %d audit rows", i, line, subs[winner], bound)
		}
		// The loser cannot sign in with the spent code afterwards either.
		loser := subs[1-winner]
		if r := e.staffLogin(app, loser, st.InviteCode); r.status != http.StatusUnauthorized {
			t.Fatalf("round %d: loser retry: %d", i, r.status)
		}
	}
}

// Invariant: invite codes are compared after removing spaces, tabs and
// dashes and upper-casing, the way the staff app normalizes them; any other
// difference is a wrong code.
func TestStaffInviteCodeFormats(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	cases := []struct {
		name string
		edit func(code string) string
		ok   bool
	}{
		{"as issued", func(c string) string { return c }, true},
		{"lower case", strings.ToLower, true},
		{"dashes every four", func(c string) string { return c[:4] + "-" + c[4:8] + "-" + c[8:] }, true},
		{"lower case with dashes", func(c string) string { return strings.ToLower(c[:4] + "-" + c[4:8] + "-" + c[8:]) }, true},
		{"spaces and tabs around", func(c string) string { return " \t" + c + " " }, true},
		{"spaced groups", func(c string) string { return c[:4] + " " + c[4:8] + " " + c[8:] }, true},
		{"mixed case", func(c string) string { return strings.ToLower(c[:6]) + c[6:] }, true},
		{"one character wrong", func(c string) string {
			b := []byte(c)
			if b[0] == 'A' {
				b[0] = 'B'
			} else {
				b[0] = 'A'
			}
			return string(b)
		}, false},
		{"one character missing", func(c string) string { return c[1:] }, false},
		{"one character extra", func(c string) string { return c + "A" }, false},
		{"dots instead of dashes", func(c string) string { return c[:4] + "." + c[4:8] + "." + c[8:] }, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := e.staffInvite(owner, app, "Format")
			r := e.staffLogin(app, "Uformat"+string(rune('a'+i)), tc.edit(st.InviteCode))
			switch {
			case tc.ok && r.status != http.StatusOK:
				t.Fatalf("status = %d, want 200; body %s", r.status, r.body)
			case !tc.ok && r.status != http.StatusUnauthorized:
				t.Fatalf("status = %d, want 401; body %s", r.status, r.body)
			}
		})
	}
}

// Invariant: invite code attempts are limited per app and client address.
// Exhausting the limit on one app does not lock the same client out of
// another app, and does not count sign-ins that carry no code.
func TestInviteAttemptLimitIsPerApp(t *testing.T) {
	e := newMemberEnvWith(t, staffauth.Config{IPPerMinute: 10000, InvitePerMinute: 3})
	ownerA, ownerB := e.owner("a@example.com"), e.owner("b@example.com")
	appA, appB := e.app(ownerA), e.app(ownerB)
	stA, stB := e.staffInvite(ownerA, appA, "Mei"), e.staffInvite(ownerB, appB, "Kenji")

	for i := 0; i < 3; i++ {
		if r := e.staffLogin(appA, "Uguess", "WRONGWRONG23"); r.status != http.StatusUnauthorized {
			t.Fatalf("app A attempt %d: %d %s", i, r.status, r.body)
		}
	}
	// App A is now limited, even for the right code.
	if r := e.staffLogin(appA, "Umei", stA.InviteCode); r.status != http.StatusTooManyRequests {
		t.Fatalf("app A after the limit: %d %s", r.status, r.body)
	}
	// App B, same client address: its own allowance.
	for i := 0; i < 2; i++ {
		if r := e.staffLogin(appB, "Uguess", "WRONGWRONG23"); r.status != http.StatusUnauthorized {
			t.Fatalf("app B attempt %d: %d %s", i, r.status, r.body)
		}
	}
	e.staffSession(appB, "Ukenji", stB.InviteCode)
	if r := e.staffLogin(appB, "Uguess", "WRONGWRONG23"); r.status != http.StatusTooManyRequests {
		t.Fatalf("app B fourth attempt: %d %s", r.status, r.body)
	}
	// Sign-ins without a code are not invite attempts.
	e.staffSession(appB, "Ukenji", "")
	// The window passes and app A accepts the right code.
	e.clk.Advance(time.Minute)
	e.staffSession(appA, "Umei", stA.InviteCode)
}
