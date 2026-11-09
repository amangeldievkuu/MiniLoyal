package line

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

const (
	testChannel = "1661234567"
	testSecret  = "channel-secret-value"
)

// jwksServer serves a JWKS and counts downloads. Keys are added by kid.
type jwksServer struct {
	t     *testing.T
	srv   *httptest.Server
	set   jwk.Set
	keys  map[string]jwk.Key // private keys by kid
	count atomic.Int32
}

func newJWKSServer(t *testing.T) *jwksServer {
	t.Helper()
	s := &jwksServer{t: t, set: jwk.NewSet(), keys: map[string]jwk.Key{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.count.Add(1)
		body, err := json.Marshal(s.set)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// addKey generates an ES256 key pair under kid and publishes the public half.
func (s *jwksServer) addKey(kid string) jwk.Key {
	s.t.Helper()
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	priv, err := jwk.Import(raw)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := priv.Set(jwk.KeyIDKey, kid); err != nil {
		s.t.Fatal(err)
	}
	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.set.AddKey(pub); err != nil {
		s.t.Fatal(err)
	}
	s.keys[kid] = priv
	return priv
}

type tokenOpts struct {
	iss, sub, aud, nonce string
	iat, exp             time.Time
}

func claimsAt(now time.Time) tokenOpts {
	return tokenOpts{iss: DefaultIssuer, sub: "U0123456789abcdef", aud: testChannel, iat: now, exp: now.Add(time.Hour)}
}

func buildToken(t *testing.T, o tokenOpts) jwt.Token {
	t.Helper()
	b := jwt.NewBuilder().Issuer(o.iss).Subject(o.sub).Audience([]string{o.aud}).IssuedAt(o.iat).Expiration(o.exp).
		Claim("name", "Mei").Claim("picture", "https://profile.line-scdn.net/x").Claim("amr", []string{"linesso"})
	if o.nonce != "" {
		b = b.Claim("nonce", o.nonce)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func signES256(t *testing.T, tok jwt.Token, priv jwk.Key) string {
	t.Helper()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), priv))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func signHS256(t *testing.T, tok jwt.Token, secret string) string {
	t.Helper()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), []byte(secret)))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func newTestVerifier(jwks *jwksServer, clk clock.Clock, fallback bool, verifyURL string) *Verifier {
	return NewVerifier(VerifierOptions{
		HTTPClient: jwks.srv.Client(), JWKSURL: jwks.srv.URL, VerifyURL: verifyURL, Clock: clk,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), VerifyEndpointFallback: fallback,
	})
}

func isUnauthorized(err error) bool {
	ae, ok := apierr.As(err)
	return ok && ae.Status == http.StatusUnauthorized
}

func TestVerifyES256(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clk := clock.At(now)
	jwks := newJWKSServer(t)
	priv := jwks.addKey("kid-1")
	v := newTestVerifier(jwks, clk, false, "")
	in := VerifyInput{ChannelIDs: []string{"other", testChannel}, Nonce: "n-123"}

	o := claimsAt(now)
	o.nonce = "n-123"
	c, err := v.Verify(context.Background(), signES256(t, buildToken(t, o), priv), in)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if c.Sub != o.sub || c.Aud != testChannel || c.Name != "Mei" || c.Picture == "" || len(c.Amr) != 1 || !c.IssuedAt.Equal(now) {
		t.Errorf("claims = %+v", c)
	}
	if jwks.count.Load() != 1 {
		t.Errorf("JWKS downloads = %d, want 1", jwks.count.Load())
	}

	cases := map[string]func() (string, VerifyInput){
		"wrong audience": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.aud = "9999"
			o.nonce = "n-123"
			return signES256(t, buildToken(t, o), priv), in
		},
		"wrong issuer": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.iss = "https://evil.example"
			o.nonce = "n-123"
			return signES256(t, buildToken(t, o), priv), in
		},
		"expired": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.exp = now.Add(-2 * time.Minute)
			o.nonce = "n-123"
			return signES256(t, buildToken(t, o), priv), in
		},
		"issued in the future": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.iat = now.Add(5 * time.Minute)
			o.nonce = "n-123"
			return signES256(t, buildToken(t, o), priv), in
		},
		"nonce mismatch": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.nonce = "someone-else"
			return signES256(t, buildToken(t, o), priv), in
		},
		"nonce missing when required": func() (string, VerifyInput) {
			return signES256(t, buildToken(t, claimsAt(now)), priv), in
		},
		"garbage": func() (string, VerifyInput) { return "not.a.jwt", in },
		"no channels configured": func() (string, VerifyInput) {
			o := claimsAt(now)
			o.nonce = "n-123"
			return signES256(t, buildToken(t, o), priv), VerifyInput{}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			tok, in := mk()
			if _, err := v.Verify(context.Background(), tok, in); !isUnauthorized(err) {
				t.Errorf("got %v, want 401", err)
			}
		})
	}

	// Without a nonce requirement the same token verifies.
	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, claimsAt(now)), priv), VerifyInput{ChannelIDs: []string{testChannel}}); err != nil {
		t.Errorf("nonce optional: %v", err)
	}
	// Signed by a key LINE never published.
	strayJWKS := newJWKSServer(t)
	stray := strayJWKS.addKey("kid-1") // same kid, different key
	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, claimsAt(now)), stray), VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
		t.Errorf("forged signature accepted: %v", err)
	}
}

func TestVerifyRefreshesJWKSForUnknownKidOncePerMinute(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clk := clock.At(now)
	jwks := newJWKSServer(t)
	k1 := jwks.addKey("kid-1")
	v := newTestVerifier(jwks, clk, false, "")
	in := VerifyInput{ChannelIDs: []string{testChannel}}

	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, claimsAt(now)), k1), in); err != nil {
		t.Fatal(err)
	}
	// LINE rotates: a token signed by a key published after our first download.
	k2 := jwks.addKey("kid-2")
	clk.Advance(2 * time.Minute)
	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, claimsAt(clk.Now())), k2), in); err != nil {
		t.Fatalf("new kid after refresh: %v", err)
	}
	if jwks.count.Load() != 2 {
		t.Errorf("JWKS downloads = %d, want 2", jwks.count.Load())
	}
	// An unknown kid right after a refresh must not trigger another download.
	unknown := newJWKSServer(t).addKey("kid-3")
	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, claimsAt(clk.Now())), unknown), in); !isUnauthorized(err) {
		t.Errorf("unknown kid: got %v, want 401", err)
	}
	if jwks.count.Load() != 2 {
		t.Errorf("JWKS downloads = %d after an unknown kid inside the minimum interval, want 2", jwks.count.Load())
	}
}

func TestVerifyHS256(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	jwks := newJWKSServer(t)
	v := newTestVerifier(jwks, clock.At(now), false, "")
	tok := signHS256(t, buildToken(t, claimsAt(now)), testSecret)

	if _, err := v.Verify(context.Background(), tok, VerifyInput{ChannelIDs: []string{testChannel}, ChannelSecrets: map[string]string{testChannel: testSecret}}); err != nil {
		t.Fatalf("right secret: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok, VerifyInput{ChannelIDs: []string{testChannel}, ChannelSecrets: map[string]string{testChannel: "wrong"}}); !isUnauthorized(err) {
		t.Errorf("wrong secret: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok, VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
		t.Errorf("no secret and no fallback: %v", err)
	}
	if jwks.count.Load() != 0 {
		t.Errorf("HS256 must not download the JWKS, downloads = %d", jwks.count.Load())
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	jwks := newJWKSServer(t)
	v := newTestVerifier(jwks, clock.At(now), true, jwks.srv.URL+"/verify")
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	payload := map[string]any{"iss": DefaultIssuer, "sub": "U1", "aud": testChannel, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	for _, hdr := range []map[string]any{{"alg": "none", "typ": "JWT"}, {"alg": "RS256", "kid": "x"}} {
		tok := enc(hdr) + "." + enc(payload) + "."
		if _, err := v.Verify(context.Background(), tok, VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
			t.Errorf("alg %v accepted: %v", hdr["alg"], err)
		}
	}
}

func TestVerifyEndpointFallback(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	var gotClientID string
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotClientID = r.PostForm.Get("client_id")
		if r.PostForm.Get("id_token") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iss": DefaultIssuer, "sub": "U-remote", "aud": testChannel, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
			"nonce": "n-1", "amr": []string{"linesso"}, "name": "Remote", "picture": "https://p/x",
		})
	}))
	defer verify.Close()

	jwks := newJWKSServer(t) // publishes no keys: local verification is impossible
	v := NewVerifier(VerifierOptions{HTTPClient: verify.Client(), JWKSURL: jwks.srv.URL, VerifyURL: verify.URL, Clock: clock.At(now),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), VerifyEndpointFallback: true})
	// An ES256 token whose key we do not have: falls back to LINE.
	stray := newJWKSServer(t).addKey("kid-x")
	o := claimsAt(now)
	o.nonce = "n-1"
	c, err := v.Verify(context.Background(), signES256(t, buildToken(t, o), stray), VerifyInput{ChannelIDs: []string{testChannel}, Nonce: "n-1"})
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if c.Sub != "U-remote" || c.Name != "Remote" || gotClientID != testChannel || calls.Load() != 1 {
		t.Errorf("remote claims = %+v, client_id %q, calls %d", c, gotClientID, calls.Load())
	}
	// The remote answer must still match what the client asked for.
	if _, err := v.Verify(context.Background(), signES256(t, buildToken(t, o), stray), VerifyInput{ChannelIDs: []string{testChannel}, Nonce: "other"}); !isUnauthorized(err) {
		t.Errorf("remote nonce mismatch accepted: %v", err)
	}
	// Without the fallback the same token is simply refused, with no remote call.
	strict := NewVerifier(VerifierOptions{HTTPClient: verify.Client(), JWKSURL: jwks.srv.URL, VerifyURL: verify.URL, Clock: clock.At(now),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	before := calls.Load()
	if _, err := strict.Verify(context.Background(), signES256(t, buildToken(t, o), stray), VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
		t.Errorf("strict mode: %v", err)
	}
	if calls.Load() != before {
		t.Error("strict mode called the verify endpoint")
	}
}
