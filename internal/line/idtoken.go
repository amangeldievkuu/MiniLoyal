// Package line talks to the LINE Platform: ID token verification for LIFF
// sign-in, stateless channel access tokens, the Messaging API and webhook
// signatures. Every client takes an *http.Client and base URLs so tests run
// against httptest servers.
package line

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// LINE endpoints and constants.
const (
	DefaultJWKSURL   = "https://api.line.me/oauth2/v2.1/certs"
	DefaultVerifyURL = "https://api.line.me/oauth2/v2.1/verify"
	DefaultIssuer    = "https://access.line.me"

	// idTokenSkew tolerates clock drift between LINE and this server.
	idTokenSkew = 30 * time.Second
	// jwksRefreshMinInterval bounds how often an unknown key ID may trigger a
	// JWKS download, so a flood of bad tokens cannot hammer LINE.
	jwksRefreshMinInterval = time.Minute
	// jwksMaxAge forces a refresh of a key set this old, so retired keys stop
	// being trusted even without a restart.
	jwksMaxAge = 24 * time.Hour
)

// errBadIDToken is what clients see; the reason is logged at debug level.
var errBadIDToken = apierr.Unauthorized("invalid or expired LINE ID token")

// VerifyInput scopes a verification to one app: its accepted channel IDs (the
// token audience), the channel secrets for HS256 tokens keyed by channel ID,
// and the nonce the app passed to liff.login(), if any.
type VerifyInput struct {
	ChannelIDs     []string
	ChannelSecrets map[string]string
	Nonce          string
}

// Claims are the verified contents of a LINE ID token.
type Claims struct {
	Sub       string
	Name      string
	Picture   string
	Email     string
	Aud       string
	Amr       []string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// VerifierOptions configure a Verifier. Zero values take LINE's production
// endpoints and the real clock.
type VerifierOptions struct {
	HTTPClient *http.Client
	JWKSURL    string
	VerifyURL  string
	Issuer     string
	Clock      clock.Clock
	Log        *slog.Logger
	// VerifyEndpointFallback sends the token to LINE's verify endpoint when
	// it cannot be checked locally (unknown key, HS256 without a secret).
	VerifyEndpointFallback bool
}

// Verifier validates LIFF and LINE Login ID tokens: ES256 against LINE's
// JWKS, HS256 with the channel secret, or LINE's verify endpoint as a
// fallback.
type Verifier struct {
	http      *http.Client
	jwksURL   string
	verifyURL string
	issuer    string
	clock     clock.Clock
	log       *slog.Logger
	fallback  bool

	mu        sync.Mutex
	keys      jwk.Set
	fetchedAt time.Time
}

// NewVerifier returns a Verifier.
func NewVerifier(o VerifierOptions) *Verifier {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if o.JWKSURL == "" {
		o.JWKSURL = DefaultJWKSURL
	}
	if o.VerifyURL == "" {
		o.VerifyURL = DefaultVerifyURL
	}
	if o.Issuer == "" {
		o.Issuer = DefaultIssuer
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Verifier{
		http: o.HTTPClient, jwksURL: o.JWKSURL, verifyURL: o.VerifyURL, issuer: o.Issuer,
		clock: o.Clock, log: o.Log, fallback: o.VerifyEndpointFallback,
	}
}

// Verify checks idToken for the app described by in and returns its claims.
// Failures are apierr.Unauthorized with one stable message.
func (v *Verifier) Verify(ctx context.Context, idToken string, in VerifyInput) (Claims, error) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" || len(in.ChannelIDs) == 0 {
		return Claims{}, errBadIDToken
	}
	msg, err := jws.Parse([]byte(idToken))
	if err != nil || len(msg.Signatures()) != 1 {
		return Claims{}, v.reject(ctx, "token is not a compact JWS", err)
	}
	headers := msg.Signatures()[0].ProtectedHeaders()
	alg, _ := headers.Algorithm()
	kid, _ := headers.KeyID()

	// The audience decides which channel secret applies, so read it before
	// verifying; nothing is trusted until the signature checks out.
	unverified, err := jwt.Parse([]byte(idToken), jwt.WithVerify(false), jwt.WithValidate(false))
	if err != nil {
		return Claims{}, v.reject(ctx, "token payload is not a JWT", err)
	}
	aud := singleAudience(unverified)
	if aud == "" || !slices.Contains(in.ChannelIDs, aud) {
		return Claims{}, v.reject(ctx, "audience is not one of the app's channels", nil)
	}

	switch alg.String() {
	case jwa.ES256().String():
		key, err := v.keyFor(ctx, kid)
		if err != nil {
			if v.fallback {
				return v.verifyRemote(ctx, idToken, aud, in.Nonce)
			}
			return Claims{}, v.reject(ctx, "no JWKS key for kid", err)
		}
		return v.verifyLocal(ctx, idToken, jwa.ES256(), key, in.Nonce)
	case jwa.HS256().String():
		secret := in.ChannelSecrets[aud]
		if secret == "" {
			if v.fallback {
				return v.verifyRemote(ctx, idToken, aud, in.Nonce)
			}
			return Claims{}, v.reject(ctx, "HS256 token but no channel secret configured", nil)
		}
		return v.verifyLocal(ctx, idToken, jwa.HS256(), []byte(secret), in.Nonce)
	default:
		return Claims{}, v.reject(ctx, "unsupported alg "+alg.String(), nil)
	}
}

func (v *Verifier) verifyLocal(ctx context.Context, idToken string, alg jwa.SignatureAlgorithm, key any, nonce string) (Claims, error) {
	tok, err := jwt.Parse([]byte(idToken),
		jwt.WithKey(alg, key),
		jwt.WithIssuer(v.issuer),
		jwt.WithAcceptableSkew(idTokenSkew),
		jwt.WithClock(v.clock),
	)
	if err != nil {
		return Claims{}, v.reject(ctx, "signature or standard claims failed", err)
	}
	now := v.clock.Now()
	iat, ok := tok.IssuedAt()
	if !ok || iat.After(now.Add(idTokenSkew)) {
		return Claims{}, v.reject(ctx, "iat missing or in the future", nil)
	}
	exp, ok := tok.Expiration()
	if !ok {
		return Claims{}, v.reject(ctx, "exp missing", nil)
	}
	if nonce != "" {
		var got string
		if err := tok.Get("nonce", &got); err != nil || got != nonce {
			return Claims{}, v.reject(ctx, "nonce mismatch", nil)
		}
	}
	sub, ok := tok.Subject()
	if !ok || sub == "" {
		return Claims{}, v.reject(ctx, "sub missing", nil)
	}
	c := Claims{Sub: sub, Aud: singleAudience(tok), IssuedAt: iat, ExpiresAt: exp}
	_ = tok.Get("name", &c.Name)
	_ = tok.Get("picture", &c.Picture)
	_ = tok.Get("email", &c.Email)
	var amr []any
	if err := tok.Get("amr", &amr); err == nil {
		for _, a := range amr {
			if s, ok := a.(string); ok {
				c.Amr = append(c.Amr, s)
			}
		}
	}
	return c, nil
}

// verifyRemote asks LINE to verify the token: POST id_token and client_id.
func (v *Verifier) verifyRemote(ctx context.Context, idToken, channelID, nonce string) (Claims, error) {
	form := url.Values{"id_token": {idToken}, "client_id": {channelID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, fmt.Errorf("line: verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.http.Do(req)
	if err != nil {
		return Claims{}, apierr.Unavailable("LINE is not reachable").WithCause(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Claims{}, v.reject(ctx, fmt.Sprintf("verify endpoint answered %d", resp.StatusCode), nil)
	}
	var out struct {
		Iss     string   `json:"iss"`
		Sub     string   `json:"sub"`
		Aud     string   `json:"aud"`
		Exp     int64    `json:"exp"`
		Iat     int64    `json:"iat"`
		Nonce   string   `json:"nonce"`
		Amr     []string `json:"amr"`
		Name    string   `json:"name"`
		Picture string   `json:"picture"`
		Email   string   `json:"email"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Claims{}, v.reject(ctx, "verify endpoint returned invalid JSON", err)
	}
	if out.Iss != v.issuer || out.Aud != channelID || out.Sub == "" {
		return Claims{}, v.reject(ctx, "verify endpoint claims do not match", nil)
	}
	if nonce != "" && out.Nonce != nonce {
		return Claims{}, v.reject(ctx, "nonce mismatch (remote)", nil)
	}
	exp := time.Unix(out.Exp, 0)
	if !exp.After(v.clock.Now().Add(-idTokenSkew)) {
		return Claims{}, v.reject(ctx, "remote-verified token is expired", nil)
	}
	return Claims{
		Sub: out.Sub, Name: out.Name, Picture: out.Picture, Email: out.Email, Aud: out.Aud, Amr: out.Amr,
		IssuedAt: time.Unix(out.Iat, 0), ExpiresAt: exp,
	}, nil
}

// keyFor returns the JWKS key for kid, downloading the set when the key is
// unknown and the last download is older than jwksRefreshMinInterval.
func (v *Verifier) keyFor(ctx context.Context, kid string) (jwk.Key, error) {
	if kid == "" {
		return nil, errors.New("kid header missing")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.clock.Now()
	if v.keys != nil {
		if key, ok := v.keys.LookupKeyID(kid); ok {
			if now.Sub(v.fetchedAt) <= jwksMaxAge {
				return key, nil
			}
			// A key LINE may have retired: refresh and look again; keep
			// serving the old set when LINE is unreachable.
			if err := v.refreshLocked(ctx); err != nil {
				return key, nil
			}
			if key, ok := v.keys.LookupKeyID(kid); ok {
				return key, nil
			}
			return nil, errors.New("kid no longer in JWKS")
		}
	}
	// One download per minute, whether the last one succeeded or not, so a
	// flood of unknown kids (or a JWKS outage) cannot turn into a flood of
	// outbound requests.
	if !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < jwksRefreshMinInterval {
		return nil, errors.New("unknown kid and JWKS refreshed recently")
	}
	if err := v.refreshLocked(ctx); err != nil {
		return nil, err
	}
	key, ok := v.keys.LookupKeyID(kid)
	if !ok {
		return nil, errors.New("unknown kid after JWKS refresh")
	}
	return key, nil
}

func (v *Verifier) refreshLocked(ctx context.Context) error {
	v.fetchedAt = v.clock.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read JWKS: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint answered %d", resp.StatusCode)
	}
	set, err := jwk.Parse(body)
	if err != nil {
		return fmt.Errorf("parse JWKS: %w", err)
	}
	v.keys = set
	v.fetchedAt = v.clock.Now()
	v.log.DebugContext(ctx, "line: JWKS refreshed", "keys", set.Len())
	return nil
}

// reject logs the reason at debug level and returns the stable client error.
func (v *Verifier) reject(ctx context.Context, reason string, err error) error {
	v.log.DebugContext(ctx, "line: id token rejected", "reason", reason, "err", err)
	return errBadIDToken
}

func singleAudience(tok jwt.Token) string {
	aud, ok := tok.Audience()
	if !ok || len(aud) != 1 {
		return ""
	}
	return aud[0]
}
