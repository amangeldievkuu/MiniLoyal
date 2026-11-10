package coupon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Integration fixtures on a real database (TEST_DATABASE_URL).

type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *lockedClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var _ clock.Clock = (*lockedClock)(nil)

type env struct {
	d      *testutil.DB
	clk    *lockedClock
	svc    *Service
	codec  *card.Codec
	tenant uuid.UUID
	app    uuid.UUID
	owner  principal.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testutil.NewDB(t)
	clk := &lockedClock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	codec, err := card.New(bytes.Repeat([]byte{7}, 32), clk)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, clk: clk, codec: codec}
	e.svc = New(Deps{Pool: d.App, Tenants: tenant.New(d.App, keyring, clk, log), Keyring: keyring, Card: codec, Clock: clk, Log: log, StaffWritesPerMinute: -1})
	e.tenant = d.NewTenant(t, "Alpha")
	e.app = e.newApp(t, e.tenant)
	e.owner = e.user(t, e.tenant, "owner")
	return e
}

func (e *env) newApp(t *testing.T, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", id, tenantID, "cafe-"+id.String()[24:])
	return id
}

func (e *env) user(t *testing.T, tenantID uuid.UUID, role string) principal.Principal {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", id, id.String()+"@example.com")
	e.d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, id, role)
	return principal.Principal{Kind: principal.KindUser, UserID: id}
}

func (e *env) member(t *testing.T) uuid.UUID {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, e.tenant, e.app, "U"+id.String())
	return id
}

func (e *env) memberP(id uuid.UUID) principal.Principal {
	return principal.Principal{Kind: principal.KindMember, MemberID: id, AppID: e.app, TenantID: e.tenant}
}

func (e *env) staff() principal.Principal {
	return principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: "cashier", AppID: e.app, TenantID: e.tenant}
}

func (e *env) scan(t *testing.T, st principal.Principal, memberID uuid.UUID) string {
	t.Helper()
	tok, _, err := e.codec.IssueScanToken(st.StaffID, memberID, st.AppID, st.TenantID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *env) template(t *testing.T, in TemplateInput) Template {
	t.Helper()
	if in.Name == "" {
		in.Name = "NT$50 off"
	}
	if in.Type == "" {
		in.Type, in.Value = TypeAmountOff, intp(50)
	}
	tmpl, err := e.svc.CreateTemplate(context.Background(), e.owner, e.app, idem(), in)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	return tmpl
}

func (e *env) issue(t *testing.T, memberID, templateID uuid.UUID) Instance {
	t.Helper()
	inst, err := e.svc.Issue(context.Background(), e.owner, memberID, templateID, idem())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return inst
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

var keyN atomic.Int64

func idem() Idem {
	n := keyN.Add(1)
	return Idem{Key: fmt.Sprintf("k-%d-%s", n, uuid.NewString()), Hash: []byte(fmt.Sprintf("h-%d", n))}
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func code(err error) string {
	if err == nil {
		return ""
	}
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	return "internal: " + err.Error()
}

func statusOf(err error) int {
	if ae, ok := apierr.As(err); ok {
		return ae.Status
	}
	return 0
}

func detailsOf(err error) map[string]any {
	if ae, ok := apierr.As(err); ok {
		return ae.Details
	}
	return nil
}
