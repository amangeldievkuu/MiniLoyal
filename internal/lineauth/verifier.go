package lineauth

import (
	"context"
	"errors"
	"time"
)

// IDTokenVerifier checks a LIFF ID token and returns its claims. The line
// package implements it against LINE's JWKS (ES256) and the channel secret
// (HS256); tests use lineauthtest.FakeVerifier.
type IDTokenVerifier interface {
	Verify(ctx context.Context, idToken string, in VerifyInput) (Claims, error)
}

// VerifyInput is what the verifier needs from the app's configuration.
type VerifyInput struct {
	// ChannelIDs are the acceptable audiences: every configured internal
	// channel (developing, review, published) of the MINI App.
	ChannelIDs []string
	// ChannelSecrets maps a channel ID to its secret, for HS256 tokens. It may
	// be partial; ES256 tokens need no secret.
	ChannelSecrets map[string]string
	// Nonce must match the token's nonce claim when set.
	Nonce string
}

// Claims are the verified ID token claims the service uses.
type Claims struct {
	// Sub is the LINE user ID, unique per provider.
	Sub     string
	Name    string
	Picture string
	Email   string
	// Aud is the channel ID the token was issued for.
	Aud       string
	Amr       []string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// ErrInvalidIDToken is returned, possibly wrapped, for every verification
// failure. The service shows the client one generic 401 whatever the cause.
var ErrInvalidIDToken = errors.New("lineauth: invalid ID token")
