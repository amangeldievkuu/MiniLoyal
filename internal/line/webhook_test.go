package line

import (
	"testing"
)

const sampleWebhook = `{
  "destination": "Uabcdef0123456789abcdef0123456789",
  "events": [
    {"type": "follow", "webhookEventId": "01HAAAAAAAAAAAAAAAAAAAAAA1", "deliveryContext": {"isRedelivery": false}, "timestamp": 1762689600000, "mode": "active", "source": {"type": "user", "userId": "U1111111111111111111111111111111"}, "replyToken": "r1"},
    {"type": "unfollow", "webhookEventId": "01HAAAAAAAAAAAAAAAAAAAAAA2", "deliveryContext": {"isRedelivery": true}, "timestamp": 1762689601000, "mode": "active", "source": {"type": "user", "userId": "U2222222222222222222222222222222"}},
    {"type": "message", "webhookEventId": "01HAAAAAAAAAAAAAAAAAAAAAA3", "deliveryContext": {"isRedelivery": false}, "timestamp": 1762689602000, "mode": "active", "source": {"type": "group", "groupId": "Cg", "userId": "U3"}, "message": {"type": "text", "id": "m", "text": "hi"}}
  ]
}`

func TestVerifySignature(t *testing.T) {
	body := []byte(sampleWebhook)
	sig := Sign("secret-1", body)
	if sig == "" || !VerifySignature("secret-1", body, sig) {
		t.Fatal("signature does not verify")
	}
	cases := map[string]struct {
		secret string
		body   []byte
		sig    string
	}{
		"wrong secret":   {"secret-2", body, sig},
		"tampered body":  {"secret-1", append([]byte(`{"x":1}`), body[7:]...), sig},
		"empty sig":      {"secret-1", body, ""},
		"empty secret":   {"", body, Sign("", body)},
		"truncated sig":  {"secret-1", body, sig[:len(sig)-2]},
		"not base64 sig": {"secret-1", body, "!!!"},
	}
	for name, tc := range cases {
		if VerifySignature(tc.secret, tc.body, tc.sig) {
			t.Errorf("%s: accepted", name)
		}
	}
	// A known vector computed independently: HMAC-SHA256("secret", "{}") base64.
	if got := Sign("secret", []byte("{}")); got != "dzJZAsrKgS3CWXM6rNBGtzgXNyx3e42VtAJkdHRRbhM=" {
		t.Errorf("known vector = %s", got)
	}
}

func TestParseEvents(t *testing.T) {
	req, err := ParseEvents([]byte(sampleWebhook))
	if err != nil {
		t.Fatal(err)
	}
	if req.Destination != "Uabcdef0123456789abcdef0123456789" || len(req.Events) != 3 {
		t.Fatalf("parsed %+v", req)
	}
	f := req.Events[0]
	if f.Type != EventFollow || f.WebhookEventID != "01HAAAAAAAAAAAAAAAAAAAAAA1" || f.DeliveryContext.IsRedelivery || f.Source == nil || f.Source.UserID != "U1111111111111111111111111111111" || f.Timestamp != 1762689600000 || f.Mode != "active" {
		t.Errorf("follow = %+v", f)
	}
	u := req.Events[1]
	if u.Type != EventUnfollow || !u.DeliveryContext.IsRedelivery {
		t.Errorf("unfollow = %+v", u)
	}
	m := req.Events[2]
	if m.Source.Type != "group" || len(m.Raw) == 0 {
		t.Errorf("message = %+v", m)
	}
	for _, bad := range []string{``, `[]`, `{"events":[{"webhookEventId":"x"}]}`, `{"events":"no"}`} {
		if _, err := ParseEvents([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	empty, err := ParseEvents([]byte(`{"destination":"U","events":[]}`))
	if err != nil || len(empty.Events) != 0 {
		t.Errorf("empty batch: %v %+v", err, empty)
	}
}
