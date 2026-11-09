package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

func listEvents(t *testing.T, svc *auth.Service, sess *auth.Session) []auth.SecurityEvent {
	t.Helper()
	page, err := svc.ListSecurityEvents(context.Background(), sess.User.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func countEvents(t *testing.T, items []auth.SecurityEvent, name string) int {
	t.Helper()
	n := 0
	for _, e := range items {
		if e.Event == name {
			n++
		}
	}
	return n
}

func totalEvents(t *testing.T, d *testutil.DB) int {
	t.Helper()
	var n int
	if err := d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM security_events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSecurityEventsRecordAccountActivity(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "events@example.com", "correct horse battery")

	// Unknown emails leave no trace: an attacker cannot fill the table.
	before := totalEvents(t, d)
	if _, err := svc.Login(ctx, "nobody@example.com", "whatever password", client); status(t, err) != 401 {
		t.Fatalf("unknown email: %v", err)
	}
	if totalEvents(t, d) != before {
		t.Fatal("unknown-email login recorded an event")
	}

	if _, err := svc.Login(ctx, "events@example.com", "wrong horse battery", client); status(t, err) != 401 {
		t.Fatalf("wrong password: %v", err)
	}
	res, err := svc.Login(ctx, "events@example.com", "correct horse battery", client)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	next, err := svc.ChangePassword(ctx, sess.User.ID, "correct horse battery", "another long password", client)
	if err != nil {
		t.Fatal(err)
	}
	// A replayed refresh token: the family dies and the event says so.
	clk.Advance(2 * time.Minute)
	if _, err := svc.Refresh(ctx, res.Session.RefreshToken, client); status(t, err) != 401 {
		t.Fatalf("revoked token: %v", err)
	}
	if err := svc.LogoutAll(auth.WithClient(ctx, client), sess.User.ID); err != nil {
		t.Fatal(err)
	}
	_ = next

	items := listEvents(t, svc, sess)
	want := map[string]int{
		auth.EventLoginFailed:     1,
		auth.EventLoginSuccess:    1,
		auth.EventPasswordChanged: 1,
		auth.EventSessionsEnded:   1,
	}
	for name, n := range want {
		if got := countEvents(t, items, name); got != n {
			t.Errorf("%s: %d events, want %d", name, got, n)
		}
	}
	// Newest first, each with the device that caused it.
	if len(items) < 4 || items[0].Event != auth.EventSessionsEnded {
		t.Fatalf("events are not newest first: %+v", items)
	}
	for _, e := range items {
		if e.IP == nil || *e.IP != client.IP || e.UserAgent == nil || *e.UserAgent != client.UserAgent {
			t.Errorf("%s lacks device info: ip=%v ua=%v", e.Event, e.IP, e.UserAgent)
		}
	}
}

func TestSecurityEventsForMFAAndTOTP(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "mfa-events@example.com", "correct horse battery")
	secret := enrollTOTP(t, svc, clk, sess)

	token := challenge(t, svc, "mfa-events@example.com")
	for i := 0; i < 5; i++ {
		if _, err := svc.CompleteMFA(ctx, token, "000000", client); status(t, err) != 401 {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if _, err := svc.CompleteMFA(ctx, token, codeAt(t, secret, clk.Now()), client); status(t, err) != 401 {
		t.Fatalf("exhausted: %v", err)
	}
	if _, err := svc.CompleteMFA(ctx, challenge(t, svc, "mfa-events@example.com"), codeAt(t, secret, clk.Now()), client); err != nil {
		t.Fatalf("fresh challenge: %v", err)
	}
	clk.Advance(31 * time.Second)
	if err := svc.DisableTOTP(auth.WithClient(ctx, client), sess.User.ID, "correct horse battery", codeAt(t, secret, clk.Now())); err != nil {
		t.Fatal(err)
	}

	items := listEvents(t, svc, sess)
	want := map[string]int{
		auth.EventTOTPEnabled:  1,
		auth.EventMFAFailed:    5,
		auth.EventMFAExhausted: 1,
		auth.EventLoginSuccess: 1,
		auth.EventTOTPDisabled: 1,
	}
	for name, n := range want {
		if got := countEvents(t, items, name); got != n {
			t.Errorf("%s: %d events, want %d", name, got, n)
		}
	}
}

func TestSecurityEventsArePerUserAndPaged(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.Real{}, auth.Config{})
	ctx := context.Background()
	a := register(t, svc, "a@example.com", "correct horse battery")
	b := register(t, svc, "b@example.com", "correct horse battery")
	for i := 0; i < 3; i++ {
		if _, err := svc.Login(ctx, "a@example.com", "correct horse battery", client); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Login(ctx, "b@example.com", "correct horse battery", client); err != nil {
		t.Fatal(err)
	}

	first, err := svc.ListSecurityEvents(ctx, a.User.ID, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %d items, cursor %q", len(first.Items), first.NextCursor)
	}
	second, err := svc.ListSecurityEvents(ctx, a.User.ID, 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %d items, cursor %q", len(second.Items), second.NextCursor)
	}
	if second.Items[0].ID >= first.Items[1].ID {
		t.Error("pages overlap")
	}
	if _, err := svc.ListSecurityEvents(ctx, a.User.ID, 2, "not-a-cursor!"); status(t, err) != 400 {
		t.Errorf("bad cursor: %v", err)
	}
	bEvents := listEvents(t, svc, b)
	if len(bEvents) != 1 {
		t.Errorf("user b sees %d events, want only their own 1", len(bEvents))
	}
}
