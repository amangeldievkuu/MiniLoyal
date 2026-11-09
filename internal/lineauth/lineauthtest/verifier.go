// Package lineauthtest holds test doubles for the lineauth package.
package lineauthtest

import (
	"context"
	"slices"
	"sync"

	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
)

// FakeVerifier accepts the tokens it was told about. A token is valid only
// when its registered audience is one of the acceptable channel IDs, which
// mirrors the real verifier's aud check.
type FakeVerifier struct {
	mu     sync.Mutex
	tokens map[string]lineauth.Claims
	// Calls records every VerifyInput seen, for assertions.
	Calls []lineauth.VerifyInput
	// Err, when set, is returned for every token.
	Err error
}

// NewFakeVerifier returns an empty verifier.
func NewFakeVerifier() *FakeVerifier {
	return &FakeVerifier{tokens: map[string]lineauth.Claims{}}
}

// Add registers a token with its claims.
func (f *FakeVerifier) Add(token string, claims lineauth.Claims) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = claims
}

// Verify implements lineauth.IDTokenVerifier.
func (f *FakeVerifier) Verify(_ context.Context, idToken string, in lineauth.VerifyInput) (lineauth.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, in)
	if f.Err != nil {
		return lineauth.Claims{}, f.Err
	}
	c, ok := f.tokens[idToken]
	if !ok || !slices.Contains(in.ChannelIDs, c.Aud) {
		return lineauth.Claims{}, lineauth.ErrInvalidIDToken
	}
	return c, nil
}
