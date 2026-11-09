package line

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// DefaultTokenURL issues stateless channel access tokens (15 minutes).
const DefaultTokenURL = "https://api.line.me/oauth2/v3/token"

// tokenRenewEarly renews a cached token this long before it expires.
const tokenRenewEarly = 60 * time.Second

// Sentinel errors from the token endpoint.
var (
	// ErrInvalidCredentials means LINE rejected the channel ID or secret.
	ErrInvalidCredentials = errors.New("line: invalid channel credentials")
	// ErrUnavailable means LINE could not be reached or answered 5xx.
	ErrUnavailable = errors.New("line: platform unavailable")
)

// TokenSourceOptions configure a TokenSource.
type TokenSourceOptions struct {
	HTTPClient *http.Client
	TokenURL   string
	Clock      clock.Clock
}

// TokenSource mints and caches stateless channel access tokens per channel.
// Concurrent callers for the same channel share one in-flight request; tokens
// live only in memory and are never logged.
type TokenSource struct {
	http     *http.Client
	tokenURL string
	clock    clock.Clock

	mu      sync.Mutex
	entries map[string]*tokenEntry
}

type tokenEntry struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewTokenSource returns a TokenSource.
func NewTokenSource(o TokenSourceOptions) *TokenSource {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	return &TokenSource{http: o.HTTPClient, tokenURL: o.TokenURL, clock: o.Clock, entries: map[string]*tokenEntry{}}
}

// Token returns a valid access token for the channel, minting one when the
// cache is empty or within tokenRenewEarly of expiry.
func (t *TokenSource) Token(ctx context.Context, channelID, channelSecret string) (string, error) {
	t.mu.Lock()
	e, ok := t.entries[channelID]
	if !ok {
		e = &tokenEntry{}
		t.entries[channelID] = e
	}
	t.mu.Unlock()

	// The entry lock serializes refreshes for one channel: waiters wake up to
	// a fresh token instead of each hitting LINE.
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token != "" && t.clock.Now().Before(e.expiresAt.Add(-tokenRenewEarly)) {
		return e.token, nil
	}
	token, ttl, err := t.Mint(ctx, channelID, channelSecret)
	if err != nil {
		return "", err
	}
	e.token = token
	e.expiresAt = t.clock.Now().Add(ttl)
	return token, nil
}

// Forget drops the cached token for a channel, for example after its secret
// changed.
func (t *TokenSource) Forget(channelID string) {
	t.mu.Lock()
	delete(t.entries, channelID)
	t.mu.Unlock()
}

// Mint issues a stateless token without touching the cache. It is also how
// credentials are proven: LINE only issues a token for a valid ID and secret.
func (t *TokenSource) Mint(ctx context.Context, channelID, channelSecret string) (token string, ttl time.Duration, err error) {
	if channelID == "" || channelSecret == "" {
		return "", 0, ErrInvalidCredentials
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {channelID},
		"client_secret": {channelSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("line: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized:
		return "", 0, ErrInvalidCredentials
	case resp.StatusCode >= 500:
		return "", 0, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	default:
		return "", 0, fmt.Errorf("line: token endpoint answered %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", 0, fmt.Errorf("line: token response invalid")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 900
	}
	return out.AccessToken, time.Duration(out.ExpiresIn) * time.Second, nil
}
