package webhook_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

// Adversarial deliveries. The signature covers the exact bytes LINE sent,
// every event is applied once per app however often it arrives, and
// malformed events are ignored without failing the batch.

// countMembers returns how many member rows the app has for the LINE user.
func (f *fixture) countMembers(t *testing.T, appID uuid.UUID, lineUserID string) int {
	t.Helper()
	var n int
	if err := f.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM members WHERE app_id = $1 AND line_user_id = $2", appID, lineUserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Invariant: the signature is checked over the raw body. A signature over
// any other serialization of the same JSON, or any other encoding of the
// right MAC, is refused and changes nothing.
func TestSignatureMustCoverTheRawBody(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// LINE's body: spaces after colons and keys in LINE's order.
	body := []byte(`{"destination": "Ubot", "events": [{"type": "follow", "webhookEventId": "sig-1", "deliveryContext": {"isRedelivery": false}, "timestamp":4102444800000, "mode": "active", "source": {"type": "user", "userId": "Usig1"}}]}`)
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	reserialized, err := json.Marshal(decoded) // compact, keys sorted
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(body, reserialized) {
		t.Fatal("test needs a body whose re-serialization differs")
	}
	good := line.Sign(oaSecret, body)
	mac, err := base64.StdEncoding.DecodeString(good)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		body []byte
		sig  string
	}{
		"signature over the re-serialized body":  {body, line.Sign(oaSecret, reserialized)},
		"re-serialized body, original signature": {reserialized, good},
		"body with a trailing newline":           {append(bytes.Clone(body), '\n'), good},
		"body with a leading BOM":                {append([]byte{0xEF, 0xBB, 0xBF}, body...), good},
		"signature with sha256= prefix":          {body, "sha256=" + good},
		"signature in hex":                       {body, hex.EncodeToString(mac)},
		"signature in base64url":                 {body, strings.NewReplacer("+", "-", "/", "_").Replace(good)},
		"signature without padding":              {body, strings.TrimRight(good, "=")},
		"signature with trailing newline":        {body, good + "\n"},
		"signature with surrounding spaces":      {body, " " + good + " "},
		"signature lower-cased":                  {body, strings.ToLower(good)},
		"signature of an empty body":             {body, line.Sign(oaSecret, nil)},
		"empty signature":                        {body, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Receive(ctx, f.app, tc.sig, tc.body)
			if status(t, err) != http.StatusUnauthorized {
				t.Errorf("got %v, want 401", err)
			}
		})
	}
	if f.countMembers(t, f.app, "Usig1") != 0 {
		t.Error("a refused delivery created a member")
	}
	var events int
	if err := f.d.Admin.QueryRow(ctx, "SELECT count(*) FROM webhook_inbound_events WHERE app_id = $1", f.app).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("refused deliveries recorded %d events", events)
	}
	// Control: the exact bytes with the exact signature are accepted.
	if res, err := f.svc.Receive(ctx, f.app, good, body); err != nil || res.Processed != 1 {
		t.Fatalf("control: %v %+v", err, res)
	}
}

// Invariant: a body that is not a LINE webhook payload is 400 even with a
// valid signature, and records nothing.
func TestValidSignatureOverANonWebhookBodyIs400(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cases := map[string][]byte{
		"plain text":            []byte("hello"),
		"empty":                 {},
		"json array":            []byte(`[{"type":"follow"}]`),
		"json string":           []byte(`"follow"`),
		"events is an object":   []byte(`{"events":{"type":"follow"}}`),
		"event without type":    []byte(`{"events":[{"webhookEventId":"x","source":{"type":"user","userId":"U1"}}]}`),
		"event is a string":     []byte(`{"events":["follow"]}`),
		"truncated json":        []byte(`{"destination":"U","events":[{"type":"follow"`),
		"xml":                   []byte(`<events><event type="follow"/></events>`),
		"form encoded":          []byte(`events=follow&userId=U1`),
		"deeply nested garbage": []byte(strings.Repeat("[", 10000) + strings.Repeat("]", 10000)),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
			if status(t, err) != http.StatusBadRequest {
				t.Errorf("got %v, want 400", err)
			}
		})
	}
	// Empty deliveries are accepted and apply nothing. LINE sends `{}` and
	// `{"events":[]}` when the webhook URL is verified; a bare JSON `null`
	// decodes into the same empty envelope, which is documented here rather
	// than treated as an error since it records and changes nothing.
	for name, body := range map[string][]byte{"empty object": []byte(`{}`), "empty events": []byte(`{"destination":"Ubot","events":[]}`), "json null": []byte(`null`)} {
		t.Run(name, func(t *testing.T) {
			res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
			if err != nil || res.Events != 0 || res.Processed != 0 {
				t.Errorf("got %v %+v, want an empty accepted delivery", err, res)
			}
		})
	}
	var events int
	if err := f.d.Admin.QueryRow(ctx, "SELECT count(*) FROM webhook_inbound_events WHERE app_id = $1", f.app).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("rejected or empty bodies recorded %d events", events)
	}
}

// Invariant: events without a user source are ignored, never create a
// member and never fail the batch; the valid events around them still
// apply.
func TestEventsWithoutAUserAreIgnored(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	events := []string{
		`{"type":"follow","webhookEventId":"nou-1","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user"}}`,
		`{"type":"follow","webhookEventId":"nou-2","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":""}}`,
		`{"type":"follow","webhookEventId":"nou-3","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":null}`,
		`{"type":"follow","webhookEventId":"nou-4","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active"}`,
		`{"type":"follow","webhookEventId":"nou-5","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"group","groupId":"Cg1","userId":"Ugroupmember"}}`,
		`{"type":"unfollow","webhookEventId":"nou-6","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"room","roomId":"Rr1"}}`,
		`{"type":"follow","webhookEventId":"","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":"Unoid"}}`,
		`{"type":"follow","webhookEventId":"nou-8","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":"Ugood"}}`,
	}
	body := []byte(`{"destination":"Ubot","events":[` + strings.Join(events, ",") + `]}`)
	res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 8 || res.Processed != 1 || res.Ignored != 7 || res.Duplicates != 0 {
		t.Errorf("result = %+v, want 8 events, 1 processed, 7 ignored", res)
	}
	var n int
	if err := f.d.Admin.QueryRow(ctx, "SELECT count(*) FROM members WHERE app_id = $1", f.app).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("members = %d, want only Ugood", n)
	}
	if f.countMembers(t, f.app, "Ugroupmember") != 0 || f.countMembers(t, f.app, "Unoid") != 0 {
		t.Error("a group sender or an event without an ID created a member")
	}
}

// Invariant: a delivery of 1000 events is applied in one pass with bounded
// time, and an event ID repeated inside one delivery is applied once.
func TestLargeBatchIsBoundedAndDeduplicatedWithinItself(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 1000
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"type":"follow","webhookEventId":"big-%04d","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":"Ubig%04d"}}`, i, i))
	}
	body := []byte(`{"destination":"Ubot","events":[` + strings.Join(parts, ",") + `]}`)
	start := time.Now()
	res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	elapsed := time.Since(start)
	if err != nil || res.Events != n || res.Processed != n {
		t.Fatalf("batch: %v %+v", err, res)
	}
	// LINE expects an answer within a second; the local database is slower
	// than production and the race detector slower still, so the bound here
	// only catches pathological behaviour such as one transaction per event.
	if elapsed > 10*time.Second {
		t.Errorf("1000 events took %s", elapsed)
	}
	var members int
	if err := f.d.Admin.QueryRow(ctx, "SELECT count(*) FROM members WHERE app_id = $1 AND is_friend", f.app).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != n {
		t.Errorf("friends = %d, want %d", members, n)
	}

	// The same event ID 50 times in one body: one application, 49 duplicates.
	same := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		same = append(same, `{"type":"follow","webhookEventId":"same-1","deliveryContext":{"isRedelivery":false},"timestamp":4102444800000,"mode":"active","source":{"type":"user","userId":"Usame"}}`)
	}
	body = []byte(`{"destination":"Ubot","events":[` + strings.Join(same, ",") + `]}`)
	res, err = f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil || res.Processed != 1 || res.Duplicates != 49 {
		t.Errorf("repeated id in one delivery: %v %+v", err, res)
	}
	if f.countMembers(t, f.app, "Usame") != 1 {
		t.Error("repeated event created more than one member")
	}
}

// Invariant: webhookEventId is unique per app, not globally. The same ID
// delivered to two apps is applied in both; a second tenant's events never
// touch the first tenant's members.
func TestSameEventIDInTwoAppsIsAppliedTwice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tenantB := f.d.NewTenant(t, "Beta")
	const secretB = "beta-oa-secret"
	appB := f.addApp(t, tenantB, "beta", "2002", secretB)

	body := event("follow", "shared-1", "Ushared", false)
	resA, errA := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	resB, errB := f.svc.Receive(ctx, appB, line.Sign(secretB, body), body)
	if errA != nil || errB != nil || resA.Processed != 1 || resB.Processed != 1 || resA.Duplicates != 0 || resB.Duplicates != 0 {
		t.Fatalf("A: %v %+v, B: %v %+v", errA, resA, errB, resB)
	}
	if f.countMembers(t, f.app, "Ushared") != 1 || f.countMembers(t, appB, "Ushared") != 1 {
		t.Error("each app must have its own member row for the LINE user")
	}
	// The signature of one app is worthless for the other.
	if _, err := f.svc.Receive(ctx, appB, line.Sign(oaSecret, body), body); status(t, err) != http.StatusUnauthorized {
		t.Errorf("app A's signature on app B: %v", err)
	}
	// Unfollow in B leaves A's member a friend.
	un := event("unfollow", "shared-2", "Ushared", false)
	if _, err := f.svc.Receive(ctx, appB, line.Sign(secretB, un), un); err != nil {
		t.Fatal(err)
	}
	var friendA, friendB bool
	if err := f.d.Admin.QueryRow(ctx, "SELECT is_friend FROM members WHERE app_id = $1 AND line_user_id = 'Ushared'", f.app).Scan(&friendA); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(ctx, "SELECT is_friend FROM members WHERE app_id = $1 AND line_user_id = 'Ushared'", appB).Scan(&friendB); err != nil {
		t.Fatal(err)
	}
	if !friendA || friendB {
		t.Errorf("after unfollow in B: friendA=%v friendB=%v", friendA, friendB)
	}
}

// Invariant: ten concurrent deliveries of one event apply it exactly once:
// one member row, one processed, nine duplicates, no errors.
func TestConcurrentDuplicateDeliveriesApplyOnce(t *testing.T) {
	f := newFixture(t)
	body := event("follow", "race-1", "Urace", false)
	sig := line.Sign(oaSecret, body)

	const workers = 10
	var processed, duplicates atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := f.svc.Receive(context.Background(), f.app, sig, body)
			if err != nil {
				errs <- err
				return
			}
			processed.Add(int32(res.Processed))
			duplicates.Add(int32(res.Duplicates))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("delivery failed: %v", err)
	}
	if processed.Load() != 1 || duplicates.Load() != workers-1 {
		t.Errorf("processed = %d, duplicates = %d, want 1 and %d", processed.Load(), duplicates.Load(), workers-1)
	}
	if f.countMembers(t, f.app, "Urace") != 1 {
		t.Errorf("members for Urace = %d, want 1", f.countMembers(t, f.app, "Urace"))
	}
	var events int
	if err := f.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM webhook_inbound_events WHERE app_id = $1 AND line_webhook_event_id = 'race-1'", f.app).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("dedup rows = %d, want 1", events)
	}
}

// Documented behaviour: after erasure the row keeps a synthetic LINE user
// ID, so a later follow from the same LINE user creates a new member. The
// erased row stays anonymized and never becomes a friend again.
func TestFollowAfterErasureCreatesANewMember(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := event("follow", "erase-1", "Uerased", false)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
		t.Fatal(err)
	}
	var oldID uuid.UUID
	if err := f.d.Admin.QueryRow(ctx, "SELECT id FROM members WHERE app_id = $1 AND line_user_id = 'Uerased'", f.app).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	// The same statement member.Service runs for an erasure.
	f.d.Exec(t, `UPDATE members SET line_user_id = 'erased:' || id::text, display_name = NULL, is_friend = false, anonymized_at = now() WHERE id = $1`, oldID)

	body = event("follow", "erase-2", "Uerased", false)
	res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil || res.Processed != 1 {
		t.Fatalf("follow after erasure: %v %+v", err, res)
	}
	var newID uuid.UUID
	var newFriend bool
	if err := f.d.Admin.QueryRow(ctx, "SELECT id, is_friend FROM members WHERE app_id = $1 AND line_user_id = 'Uerased'", f.app).Scan(&newID, &newFriend); err != nil {
		t.Fatalf("new member: %v", err)
	}
	if newID == oldID || !newFriend {
		t.Errorf("follow after erasure: id=%s (old %s) friend=%v", newID, oldID, newFriend)
	}
	var oldLine string
	var oldFriend, oldAnon bool
	if err := f.d.Admin.QueryRow(ctx, "SELECT line_user_id, is_friend, anonymized_at IS NOT NULL FROM members WHERE id = $1", oldID).Scan(&oldLine, &oldFriend, &oldAnon); err != nil {
		t.Fatal(err)
	}
	if oldLine != "erased:"+oldID.String() || oldFriend || !oldAnon {
		t.Errorf("erased row changed: line=%s friend=%v anon=%v", oldLine, oldFriend, oldAnon)
	}
	// An unfollow now reaches the new row only.
	body = event("unfollow", "erase-3", "Uerased", false)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(ctx, "SELECT is_friend FROM members WHERE id = $1", newID).Scan(&newFriend); err != nil {
		t.Fatal(err)
	}
	if newFriend {
		t.Error("unfollow did not reach the new member")
	}
}

// Invariant: deleting old deduplication rows never deletes members, and a
// redelivery after the sweep is applied again (LINE does not redeliver
// after seven days, so this is the documented trade-off).
func TestSweepKeepsMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := event("follow", "sweep-1", "Usweep", false)
	if _, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body); err != nil {
		t.Fatal(err)
	}
	f.d.Exec(t, "UPDATE webhook_inbound_events SET received_at = now() - interval '8 days' WHERE app_id = $1", f.app)
	if n, err := webhook.DeleteOldInboundEvents(ctx, f.d.App); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if f.countMembers(t, f.app, "Usweep") != 1 {
		t.Error("the sweep removed a member")
	}
	res, err := f.svc.Receive(ctx, f.app, line.Sign(oaSecret, body), body)
	if err != nil || res.Processed != 1 {
		t.Errorf("redelivery after the sweep: %v %+v", err, res)
	}
	if f.countMembers(t, f.app, "Usweep") != 1 {
		t.Error("re-applied follow duplicated the member")
	}
}
