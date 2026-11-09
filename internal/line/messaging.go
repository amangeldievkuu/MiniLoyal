package line

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultMessagingBaseURL is the Messaging API origin.
const DefaultMessagingBaseURL = "https://api.line.me"

// ErrNotFriend means the user is not a friend of the Official Account, so
// their profile is not available.
var ErrNotFriend = errors.New("line: user is not a friend of the account")

// MessagingOptions configure a Messaging client.
type MessagingOptions struct {
	HTTPClient *http.Client
	BaseURL    string
	Tokens     *TokenSource
}

// Messaging is a thin client for the brand's Official Account (Messaging
// API). Push and multicast arrive in Phase 5 with X-Line-Retry-Key handling.
type Messaging struct {
	http    *http.Client
	baseURL string
	tokens  *TokenSource
}

// NewMessaging returns a Messaging client over the token source.
func NewMessaging(o MessagingOptions) *Messaging {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultMessagingBaseURL
	}
	if o.Tokens == nil {
		o.Tokens = NewTokenSource(TokenSourceOptions{HTTPClient: o.HTTPClient})
	}
	return &Messaging{http: o.HTTPClient, baseURL: o.BaseURL, tokens: o.Tokens}
}

// BotInfo describes the Official Account behind a Messaging API channel.
type BotInfo struct {
	UserID         string `json:"userId"`
	BasicID        string `json:"basicId"`
	PremiumID      string `json:"premiumId"`
	DisplayName    string `json:"displayName"`
	PictureURL     string `json:"pictureUrl"`
	ChatMode       string `json:"chatMode"`
	MarkAsReadMode string `json:"markAsReadMode"`
}

// BotInfo fetches GET /v2/bot/info with a cached token for the channel.
func (m *Messaging) BotInfo(ctx context.Context, channelID, channelSecret string) (BotInfo, error) {
	token, err := m.tokens.Token(ctx, channelID, channelSecret)
	if err != nil {
		return BotInfo{}, err
	}
	return m.BotInfoWithToken(ctx, token)
}

// BotInfoWithToken fetches bot info with an explicit access token, which is
// how credentials are proven without caching an unverified secret.
func (m *Messaging) BotInfoWithToken(ctx context.Context, token string) (BotInfo, error) {
	var info BotInfo
	if err := m.get(ctx, token, "/v2/bot/info", &info); err != nil {
		return BotInfo{}, err
	}
	return info, nil
}

// Profile is a friend's LINE profile as the Official Account sees it.
type Profile struct {
	UserID        string `json:"userId"`
	DisplayName   string `json:"displayName"`
	PictureURL    string `json:"pictureUrl"`
	StatusMessage string `json:"statusMessage"`
	Language      string `json:"language"`
}

// Profile fetches GET /v2/bot/profile/{userId}. It only works while the user
// follows the account; otherwise ErrNotFriend.
func (m *Messaging) Profile(ctx context.Context, channelID, channelSecret, lineUserID string) (Profile, error) {
	token, err := m.tokens.Token(ctx, channelID, channelSecret)
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if err := m.get(ctx, token, "/v2/bot/profile/"+url.PathEscape(lineUserID), &p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// TODO(phase 5): Push(ctx, channelID, secret, to, messages, retryKey) and
// Multicast(...) with X-Line-Retry-Key, 429 backoff and the message log.

func (m *Messaging) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("line: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("line: %s: invalid JSON: %w", path, err)
		}
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrInvalidCredentials
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFriend
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s status %d", ErrUnavailable, path, resp.StatusCode)
	default:
		return fmt.Errorf("line: %s answered %d", path, resp.StatusCode)
	}
}
