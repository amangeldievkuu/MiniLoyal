package line

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// SignatureHeader carries the webhook signature.
const SignatureHeader = "X-Line-Signature"

// VerifySignature reports whether signature is the base64 HMAC-SHA256 of
// body under channelSecret, compared in constant time.
func VerifySignature(channelSecret string, body []byte, signature string) bool {
	if channelSecret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// Sign computes the signature LINE would send for body; tests and tools use it.
func Sign(channelSecret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Webhook event types we act on.
const (
	EventFollow   = "follow"
	EventUnfollow = "unfollow"
)

// WebhookRequest is the body LINE posts: the bot's user ID and a batch of
// events. Only the fields MiniLoyal reads are modelled.
type WebhookRequest struct {
	Destination string  `json:"destination"`
	Events      []Event `json:"events"`
}

// Event is one webhook event.
type Event struct {
	Type            string          `json:"type"`
	WebhookEventID  string          `json:"webhookEventId"`
	DeliveryContext DeliveryContext `json:"deliveryContext"`
	Timestamp       int64           `json:"timestamp"`
	Mode            string          `json:"mode"`
	Source          *Source         `json:"source"`
	// Raw keeps the full event for handlers that need more than the common
	// fields (message events in later phases).
	Raw json.RawMessage `json:"-"`
}

// DeliveryContext says whether LINE is redelivering an event it already sent.
type DeliveryContext struct {
	IsRedelivery bool `json:"isRedelivery"`
}

// Source is who triggered the event. UserID is the provider-scoped LINE user ID.
type Source struct {
	Type    string `json:"type"`
	UserID  string `json:"userId"`
	GroupID string `json:"groupId"`
	RoomID  string `json:"roomId"`
}

// ParseEvents decodes a webhook body. Call VerifySignature on the raw body
// first; parsing trusts nothing.
func ParseEvents(body []byte) (WebhookRequest, error) {
	var envelope struct {
		Destination string            `json:"destination"`
		Events      []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return WebhookRequest{}, fmt.Errorf("line: webhook body: %w", err)
	}
	out := WebhookRequest{Destination: envelope.Destination, Events: make([]Event, 0, len(envelope.Events))}
	for i, raw := range envelope.Events {
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return WebhookRequest{}, fmt.Errorf("line: webhook event %d: %w", i, err)
		}
		if ev.Type == "" {
			return WebhookRequest{}, errors.New("line: webhook event without type")
		}
		ev.Raw = raw
		out.Events = append(out.Events, ev)
	}
	return out, nil
}
