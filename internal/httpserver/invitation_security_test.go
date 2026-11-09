package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Invitation security: token strength and secrecy, the expiry boundary,
// role rules, revocation after acceptance and independence across tenants.

// Invariant: an invitation token carries 32 random bytes, every token is
// distinct, only its SHA-256 is stored, and neither the token, its hash nor
// the invite URL ever reaches the logs, the list, or the audit log.
func TestInviteTokenEntropyAndSecrecy(t *testing.T) {
	e, logs := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)

	seen := map[string]bool{}
	var tokens, urls []string
	for i := 0; i < 5; i++ {
		created, token := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "p"+string(rune('a'+i))+"@example.com", "staff", uuid.NewString()))
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("token %q is not base64url: %v", token, err)
		}
		if len(raw) != 32 {
			t.Errorf("token carries %d bytes, want 32", len(raw))
		}
		if seen[token] {
			t.Errorf("token repeated: %s", token)
		}
		seen[token] = true
		tokens = append(tokens, token)
		urls = append(urls, created.InviteUrl)

		var stored string
		if err := e.db.Admin.QueryRow(context.Background(), "SELECT token_hash FROM invitations WHERE id = $1", uuid.UUID(created.Id)).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(token))
		if stored != hex.EncodeToString(sum[:]) {
			t.Errorf("stored hash is not sha256(token)")
		}
		if stored == token || strings.Contains(stored, token) {
			t.Error("the raw token is stored")
		}
	}

	list := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/invitations", nil, bearer(owner.AccessToken))
	audit := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/audit-log", nil, bearer(owner.AccessToken))
	if list.status != http.StatusOK || audit.status != http.StatusOK {
		t.Fatalf("list %d, audit %d", list.status, audit.status)
	}
	var auditRows string
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT coalesce(string_agg(a::text, '|'), '') FROM audit_log a WHERE tenant_id = $1", uuid.UUID(tn.Id)).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	for i, token := range tokens {
		sum := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(sum[:])
		for where, text := range map[string]string{"logs": logs.String(), "invitation list": string(list.body), "audit API": string(audit.body), "audit rows": auditRows} {
			if strings.Contains(text, token) {
				t.Errorf("%s contain invitation token %d", where, i)
			}
			if strings.Contains(text, urls[i]) || strings.Contains(text, "accept-invite?token=") {
				t.Errorf("%s contain an invite URL", where)
			}
			if where != "audit rows" && strings.Contains(text, hash) {
				t.Errorf("%s contain a token hash", where)
			}
		}
	}
	// The hash is not in the API either way; the DB row may hold it.
	if strings.Contains(string(list.body), "token") {
		t.Errorf("the invitation list has a token field: %s", list.body)
	}
}

// Invariant (security review P1): an invitation for an existing account is
// not a password oracle and cannot bypass TOTP. Any password, right or wrong,
// gets the same 401 sign_in_required; the account has to be signed in to
// accept. An unknown token is a plain 401. No response names the account.
func TestAcceptWrongPasswordAndUnknownTokenLookAlike(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	existing, _ := e.register("existing@example.com", "existing long password")
	_, token := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "existing@example.com", "staff", uuid.NewString()))

	wrong := e.accept(map[string]any{"token": token, "password": "not the right password"})
	right := e.accept(map[string]any{"token": token, "password": "existing long password"})
	unknown := e.accept(map[string]any{"token": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "password": "not the right password"})
	for name, r := range map[string]resp{"wrong password": wrong, "right password": right, "unknown token": unknown} {
		if r.status != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
		if len(r.cookies) != 0 {
			t.Errorf("%s: a refused acceptance set a cookie", name)
		}
		for _, secret := range []string{"existing@example.com", existing.User.Id.String(), token} {
			if bytes.Contains(r.body, []byte(secret)) {
				t.Errorf("%s: 401 body names %q: %s", name, secret, r.body)
			}
		}
	}
	var wrongErr, rightErr, unknownErr api.Error
	wrong.into(t, &wrongErr)
	right.into(t, &rightErr)
	unknown.into(t, &unknownErr)
	if wrongErr.Code != "sign_in_required" || rightErr.Code != wrongErr.Code || rightErr.Message != wrongErr.Message {
		t.Errorf("right and wrong passwords must be indistinguishable: %+v vs %+v", rightErr, wrongErr)
	}
	if unknownErr.Code != "unauthorized" {
		t.Errorf("unknown token code = %q", unknownErr.Code)
	}
	// Still pending, and accepted once the account itself is signed in.
	list := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/invitations", nil, bearer(owner.AccessToken))
	var pending []api.Invitation
	list.into(t, &pending)
	if len(pending) != 1 {
		t.Errorf("pending after refused attempts = %d, want 1", len(pending))
	}
	joined := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": token}, bearer(existing.AccessToken))
	if joined.status != http.StatusOK {
		t.Fatalf("accept while signed in: %d %s", joined.status, joined.body)
	}
	var sess api.Session
	joined.into(t, &sess)
	if sess.User.Id != existing.User.Id {
		t.Errorf("acceptance created another account: %s vs %s", sess.User.Id, existing.User.Id)
	}
}

// Invariant: an invitation expires exactly at expires_at on the service
// clock: one second before it is listed and redeemable, at the instant it
// is gone and refused.
func TestInvitationExpiresExactlyAtExpiresAt(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	createdAt := e.clk.Now()
	early, earlyToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "early@example.com", "staff", uuid.NewString()))
	late, lateToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "late@example.com", "staff", uuid.NewString()))
	if !early.ExpiresAt.Equal(createdAt.Add(tenant.InviteTTL)) || !late.ExpiresAt.Equal(createdAt.Add(tenant.InviteTTL)) {
		t.Fatalf("expires_at = %s, want created + %s", early.ExpiresAt, tenant.InviteTTL)
	}

	relogin := func() string {
		res, r := e.login("owner@example.com", "correct horse battery", nil)
		if r.status != http.StatusOK || res.Session == nil {
			t.Fatalf("re-login: %d %s", r.status, r.body)
		}
		return res.Session.AccessToken
	}
	pending := func(access string) []api.Invitation {
		t.Helper()
		r := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/invitations", nil, bearer(access))
		if r.status != http.StatusOK {
			t.Fatalf("list: %d %s", r.status, r.body)
		}
		var out []api.Invitation
		r.into(t, &out)
		return out
	}

	e.clk.Advance(tenant.InviteTTL - time.Second)
	access := relogin()
	if got := pending(access); len(got) != 2 {
		t.Errorf("one second before expiry: %d pending, want 2", len(got))
	}
	if r := e.accept(map[string]any{"token": earlyToken, "password": "a long enough password"}); r.status != http.StatusOK {
		t.Errorf("accept one second before expiry: %d %s", r.status, r.body)
	}

	e.clk.Advance(time.Second) // now == expires_at
	access = relogin()
	if got := pending(access); len(got) != 0 {
		t.Errorf("at expiry: %d pending, want 0: %+v", len(got), got)
	}
	if r := e.accept(map[string]any{"token": lateToken, "password": "a long enough password"}); r.status != http.StatusUnauthorized {
		t.Errorf("accept at expiry: %d %s", r.status, r.body)
	}
	// Revoking an expired invitation is still a no-op 204 while the row is
	// pending in the database (the list hides it, revoke marks it); doing it
	// again is 404.
	if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+late.Id.String(), nil, bearer(access)); r.status != http.StatusNoContent && r.status != http.StatusNotFound {
		t.Errorf("revoke expired: %d %s", r.status, r.body)
	}
	// The address can be invited again and gets a fresh expiry.
	again, _ := inviteToken(t, e.invite(access, tn.Id, "late@example.com", "staff", uuid.NewString()))
	if !again.ExpiresAt.Equal(e.clk.Now().Add(tenant.InviteTTL)) {
		t.Errorf("re-invite expires_at = %s, want now + %s", again.ExpiresAt, tenant.InviteTTL)
	}
}

// Invariant: an admin cannot grant the owner role, and the refused attempt
// leaves no invitation and no audit entry.
func TestAdminCannotInviteOwnerAndLeavesNoTrace(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	_, adminToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "admin@example.com", "admin", uuid.NewString()))
	accepted := e.accept(map[string]any{"token": adminToken, "password": "admin long password"})
	var admin api.Session
	accepted.into(t, &admin)

	var invitations, audits int
	count := func() {
		t.Helper()
		if err := e.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM invitations WHERE tenant_id = $1 AND email = 'boss@example.com'", uuid.UUID(tn.Id)).Scan(&invitations); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action = 'team.invite' AND metadata->>'email' = 'boss@example.com'", uuid.UUID(tn.Id)).Scan(&audits); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"owner", "Owner", "OWNER", "root", ""} {
		r := e.invite(admin.AccessToken, tn.Id, "boss@example.com", role, uuid.NewString())
		if r.status != http.StatusForbidden && r.status != http.StatusBadRequest {
			t.Errorf("admin invites role %q: %d %s", role, r.status, r.body)
		}
		if r.status == http.StatusForbidden && role != "owner" {
			t.Errorf("role %q should be a validation error, not a role check: %s", role, r.body)
		}
	}
	count()
	if invitations != 0 || audits != 0 {
		t.Errorf("refused owner invitation left %d invitations and %d audit rows", invitations, audits)
	}
	// Replaying the refused request with the same key is refused again, and
	// an owner using the same key is not served the admin's refusal.
	key := uuid.NewString()
	if r := e.invite(admin.AccessToken, tn.Id, "boss@example.com", "owner", key); r.status != http.StatusForbidden {
		t.Errorf("first: %d", r.status)
	}
	if r := e.invite(admin.AccessToken, tn.Id, "boss@example.com", "owner", key); r.status != http.StatusForbidden {
		t.Errorf("replay: %d", r.status)
	}
	if r := e.invite(owner.AccessToken, tn.Id, "boss@example.com", "owner", key); r.status != http.StatusCreated {
		t.Errorf("owner with the admin's key: %d %s", r.status, r.body)
	}
	count()
	if invitations != 1 || audits != 1 {
		t.Errorf("after the owner's invitation: %d invitations, %d audit rows, want 1 and 1", invitations, audits)
	}
}

// Invariant: an accepted invitation cannot be revoked (404) and revocation
// never removes the membership it created.
func TestRevokeAfterAcceptIs404(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	created, token := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "joiner@example.com", "staff", uuid.NewString()))
	if r := e.accept(map[string]any{"token": token, "password": "joiner long password"}); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+created.Id.String(), nil, bearer(owner.AccessToken))
	if r.status != http.StatusNotFound {
		t.Fatalf("revoke after accept: %d %s", r.status, r.body)
	}
	team := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/team", nil, bearer(owner.AccessToken))
	var members []api.TeamMember
	team.into(t, &members)
	found := false
	for _, m := range members {
		if string(m.Email) == "joiner@example.com" && m.Role == "staff" {
			found = true
		}
	}
	if !found {
		t.Errorf("membership missing after the revoke attempt: %+v", members)
	}
	var revoked bool
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT revoked_at IS NOT NULL FROM invitations WHERE id = $1", uuid.UUID(created.Id)).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Error("an accepted invitation was marked revoked")
	}
	// Nor can the used token be redeemed again, or the ID be revoked by an
	// outsider.
	if r := e.accept(map[string]any{"token": token, "password": "joiner long password"}); r.status != http.StatusUnauthorized {
		t.Errorf("second redemption: %d", r.status)
	}
	outsider, _ := e.register("outsider@example.com", "outsider long password")
	if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+created.Id.String(), nil, bearer(outsider.AccessToken)); r.status != http.StatusNotFound {
		t.Errorf("outsider revoke: %d", r.status)
	}
}

// Invariant: two tenants may invite the same address at the same time;
// each invitation is redeemed on its own, the second one against the
// account the first one created, and the user ends up in both tenants
// with the role each tenant granted.
func TestTwoTenantsInviteTheSameEmailIndependently(t *testing.T) {
	e, _ := newInviteEnv(t)
	ownerA, _ := e.register("a@example.com", "correct horse battery")
	ownerB, _ := e.register("b@example.com", "correct horse battery")
	tnA, tnB := e.tenantOf(ownerA.AccessToken), e.tenantOf(ownerB.AccessToken)
	if tnA.Id == tnB.Id {
		t.Fatal("registrations share a tenant")
	}

	createdA, tokenA := inviteToken(t, e.invite(ownerA.AccessToken, tnA.Id, "shared@example.com", "staff", uuid.NewString()))
	createdB, tokenB := inviteToken(t, e.invite(ownerB.AccessToken, tnB.Id, "shared@example.com", "admin", uuid.NewString()))
	if tokenA == tokenB || createdA.Id == createdB.Id {
		t.Fatal("the two tenants received the same invitation")
	}
	// Neither owner sees the other's invitation.
	for _, c := range []struct {
		access string
		other  api.Tenant
		id     api.InvitationCreated
	}{{ownerA.AccessToken, tnB, createdB}, {ownerB.AccessToken, tnA, createdA}} {
		if r := e.do(http.MethodGet, "/v1/dashboard/tenants/"+c.other.Id.String()+"/invitations", nil, bearer(c.access)); r.status != http.StatusNotFound {
			t.Errorf("cross-tenant list: %d", r.status)
		}
		if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+c.id.Id.String(), nil, bearer(c.access)); r.status != http.StatusNotFound {
			t.Errorf("cross-tenant revoke: %d", r.status)
		}
	}

	// Accepting A creates the account; B then needs that account signed in.
	first := e.accept(map[string]any{"token": tokenA, "password": "shared long password"})
	if first.status != http.StatusOK {
		t.Fatalf("accept A: %d %s", first.status, first.body)
	}
	var sessA api.Session
	first.into(t, &sessA)
	if r := e.accept(map[string]any{"token": tokenB}); r.status != http.StatusUnauthorized {
		t.Errorf("accept B without signing in: %d %s", r.status, r.body)
	}
	if r := e.accept(map[string]any{"token": tokenB, "password": "shared long password"}); r.status != http.StatusUnauthorized {
		t.Errorf("accept B with the password must be refused: %d %s", r.status, r.body)
	}
	second := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": tokenB}, bearer(sessA.AccessToken))
	if second.status != http.StatusOK {
		t.Fatalf("accept B while signed in: %d %s", second.status, second.body)
	}
	var sessB api.Session
	second.into(t, &sessB)
	if sessA.User.Id != sessB.User.Id {
		t.Errorf("two accounts for one address: %s and %s", sessA.User.Id, sessB.User.Id)
	}
	me := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sessB.AccessToken))
	var profile api.Me
	me.into(t, &profile)
	roles := map[string]string{}
	for _, m := range profile.Memberships {
		roles[m.Tenant.Id.String()] = string(m.Role)
	}
	if roles[tnA.Id.String()] != "staff" || roles[tnB.Id.String()] != "admin" || len(roles) != 2 {
		t.Errorf("memberships = %v", roles)
	}
	// Both invitations are spent; both lists are empty; both revokes 404.
	for _, c := range []struct {
		access string
		tn     api.Tenant
		id     api.InvitationCreated
	}{{ownerA.AccessToken, tnA, createdA}, {ownerB.AccessToken, tnB, createdB}} {
		r := e.do(http.MethodGet, "/v1/dashboard/tenants/"+c.tn.Id.String()+"/invitations", nil, bearer(c.access))
		if strings.TrimSpace(string(r.body)) != "[]" {
			t.Errorf("pending after accept: %s", r.body)
		}
		if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+c.id.Id.String(), nil, bearer(c.access)); r.status != http.StatusNotFound {
			t.Errorf("revoke after accept: %d", r.status)
		}
	}
	var users int
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM users WHERE email = 'shared@example.com'").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Errorf("users for the shared address = %d", users)
	}
}

// inviteURLPattern is what an invite_url must look like: the dashboard
// origin, the accept route and nothing but the token in the query.
var inviteURLPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(dashboardOrigin) + `/accept-invite\?token=[A-Za-z0-9_-]{43}$`)

// Invariant: the invite URL has the configured dashboard origin and a
// 43-character base64url token, nothing else.
func TestInviteURLShape(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	created, _ := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "shape@example.com", "staff", uuid.NewString()))
	if !inviteURLPattern.MatchString(created.InviteUrl) {
		t.Errorf("invite_url = %s", created.InviteUrl)
	}
}
