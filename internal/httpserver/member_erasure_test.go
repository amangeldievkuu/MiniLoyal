package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/line"
)

// The erasure flow end to end: dashboard erasure, then LINE events and
// sign-ins for the same LINE user, through the real handlers.

const erasureOASecret = "oa-secret-for-erasure-tests"

// linkOA stores an Official Account channel on the app. memberEnv has no
// LINE verifier, so the credentials are stored unverified.
func (e *memberEnv) linkOA(access string, app api.App, channelID string) {
	e.t.Helper()
	e.must(e.do(http.MethodPatch, "/v1/dashboard/apps/"+app.Id.String(), map[string]any{"oa_channel_id": channelID, "oa_channel_secret": erasureOASecret}, idemHeaders(access)), http.StatusOK, "link OA")
}

// webhook posts one signed LINE event for the app.
func (e *memberEnv) webhook(app api.App, typ, eventID, lineUserID string) resp {
	e.t.Helper()
	body := []byte(fmt.Sprintf(`{"destination":"Ubot","events":[{"type":%q,"webhookEventId":%q,"deliveryContext":{"isRedelivery":false},"timestamp":1762689600000,"mode":"active","source":{"type":"user","userId":%q}}]}`, typ, eventID, lineUserID))
	return e.webhookBody(app, body)
}

// webhookBody posts an arbitrary body with its valid signature.
func (e *memberEnv) webhookBody(app api.App, body []byte) resp {
	e.t.Helper()
	return e.raw(http.MethodPost, "/webhooks/line/"+app.Id.String(), body, map[string]string{
		"Content-Type": "application/json", line.SignatureHeader: line.Sign(erasureOASecret, body),
	})
}

// memberRow reads the columns the erasure flow changes.
func (e *memberEnv) memberRow(id uuid.UUID) (lineUserID string, isFriend, anonymized bool) {
	e.t.Helper()
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT line_user_id, is_friend, anonymized_at IS NOT NULL FROM members WHERE id = $1", id).Scan(&lineUserID, &isFriend, &anonymized); err != nil {
		e.t.Fatal(err)
	}
	return lineUserID, isFriend, anonymized
}

// Documented behaviour: after eraseMember, a follow from the same LINE user
// creates a new member row. The erased row keeps its synthetic LINE user
// ID and stays anonymized; lists, lookups and the next sign-in all resolve
// to the new row only.
func TestEraseMemberThenFollowCreatesANewMember(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	e.linkOA(ownerTok, app, "2001000001")
	const lineUser = "Uerasethenfollow00000000000001"

	first := e.lineLogin(app, lineUser, "Mei")
	oldID := uuid.UUID(first.Member.Id)
	e.must(e.webhook(app, "follow", "ef-1", lineUser), http.StatusOK, "follow before erasure")
	if _, friend, _ := e.memberRow(oldID); !friend {
		t.Fatal("follow did not mark the member a friend")
	}

	e.must(e.do(http.MethodDelete, "/v1/dashboard/members/"+oldID.String(), nil, bearer(ownerTok)), http.StatusNoContent, "erase")
	lineID, friend, anon := e.memberRow(oldID)
	if !strings.HasPrefix(lineID, "erased:") || friend || !anon {
		t.Fatalf("erased row: line=%s friend=%v anon=%v", lineID, friend, anon)
	}
	// The member's session died with the erasure.
	if r := e.do(http.MethodGet, "/v1/me", nil, bearer(first.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("session after erasure: %d", r.status)
	}

	// LINE reports a follow for the same user: a new member, friend from
	// the start.
	e.must(e.webhook(app, "follow", "ef-2", lineUser), http.StatusOK, "follow after erasure")
	var newID uuid.UUID
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT id FROM members WHERE app_id = $1 AND line_user_id = $2", uuid.UUID(app.Id), lineUser).Scan(&newID); err != nil {
		t.Fatalf("new member row: %v", err)
	}
	if newID == oldID {
		t.Fatal("the follow revived the erased row")
	}
	if _, friend, anon := e.memberRow(newID); !friend || anon {
		t.Errorf("new row: friend=%v anon=%v", friend, anon)
	}
	if lineID, friend, anon := e.memberRow(oldID); !strings.HasPrefix(lineID, "erased:") || friend || !anon {
		t.Errorf("erased row changed by the follow: line=%s friend=%v anon=%v", lineID, friend, anon)
	}

	// The dashboard sees only the new member.
	var page api.MemberPage
	list := e.must(e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/members", nil, bearer(ownerTok)), http.StatusOK, "list")
	list.into(t, &page)
	if len(page.Items) != 1 || uuid.UUID(page.Items[0].Id) != newID || !page.Items[0].IsFriend {
		t.Errorf("list after follow: %+v", page.Items)
	}
	if bytes.Contains(list.body, []byte(oldID.String())) {
		t.Error("the erased member is listed")
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/members/"+oldID.String(), nil, bearer(ownerTok)); r.status != http.StatusNotFound {
		t.Errorf("erased member lookup: %d", r.status)
	}
	e.must(e.do(http.MethodGet, "/v1/dashboard/members/"+newID.String(), nil, bearer(ownerTok)), http.StatusOK, "new member lookup")
	// Searching by the LINE user ID finds the new row, not the erased one.
	search := e.must(e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/members?q="+lineUser, nil, bearer(ownerTok)), http.StatusOK, "search")
	search.into(t, &page)
	if len(page.Items) != 1 || uuid.UUID(page.Items[0].Id) != newID {
		t.Errorf("search by LINE user ID: %+v", page.Items)
	}

	// The LINE user signs in again and lands on the follow-created row.
	again := e.lineLogin(app, lineUser, "Mei")
	if again.IsNew || uuid.UUID(again.Member.Id) != newID || !again.Member.IsFriend {
		t.Errorf("sign-in after follow: new=%v id=%s friend=%v, want the follow-created member %s", again.IsNew, again.Member.Id, again.Member.IsFriend, newID)
	}
	// An unfollow reaches only the live row.
	e.must(e.webhook(app, "unfollow", "ef-3", lineUser), http.StatusOK, "unfollow")
	if _, friend, _ := e.memberRow(newID); friend {
		t.Error("unfollow did not reach the new member")
	}
	if lineID, friend, anon := e.memberRow(oldID); !strings.HasPrefix(lineID, "erased:") || friend || !anon {
		t.Errorf("erased row changed by the unfollow: line=%s friend=%v anon=%v", lineID, friend, anon)
	}
	// Erasing the new member works independently of the old one.
	e.must(e.do(http.MethodDelete, "/v1/dashboard/members/"+newID.String(), nil, bearer(ownerTok)), http.StatusNoContent, "erase again")
	if r := e.do(http.MethodDelete, "/v1/dashboard/members/"+newID.String(), nil, bearer(ownerTok)); r.status != http.StatusNotFound {
		t.Errorf("second erasure of the same member: %d", r.status)
	}
	var rows int
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM members WHERE app_id = $1 AND anonymized_at IS NOT NULL", uuid.UUID(app.Id)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("anonymized rows = %d, want 2 (erasure never deletes)", rows)
	}
}
