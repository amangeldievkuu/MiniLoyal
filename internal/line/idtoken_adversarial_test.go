package line

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// Adversarial ID tokens. Each case is a token that is valid in every respect
// but one, or an attack on the way the verifier chooses its key, and every
// one of them must come back as the single stable 401. Nothing here touches
// a database or the network beyond the local JWKS stub.

// rawJWS signs an arbitrary JSON payload as a compact JWS with alg and key,
// bypassing the jwt builder so claims can be malformed on purpose.
func rawJWS(t *testing.T, alg jwa.SignatureAlgorithm, key any, kid string, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	hdrs := jws.NewHeaders()
	if kid != "" {
		if err := hdrs.Set(jws.KeyIDKey, kid); err != nil {
			t.Fatal(err)
		}
	}
	signed, err := jws.Sign(body, jws.WithKey(alg, key, jws.WithProtectedHeaders(hdrs)))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// standardClaims are the claims LINE issues, as a mutable map.
func standardClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": DefaultIssuer, "sub": "U0123456789abcdef", "aud": testChannel,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "name": "Mei", "amr": []string{"linesso"},
	}
}

// with returns a copy of m with k set to v; v nil deletes the key.
func with(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for kk, vv := range m {
		out[kk] = vv
	}
	if v == nil {
		delete(out, k)
	} else {
		out[k] = v
	}
	return out
}

// rewriteHeader decodes the protected header of a compact JWS, lets mutate
// change it, and re-encodes it over the original payload and signature.
func rewriteHeader(t *testing.T, token string, mutate func(h map[string]any)) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWS: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	mutate(h)
	enc, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(enc) + "." + parts[1] + "." + parts[2]
}

// publicKeyEncodings are the byte forms of an EC public key an attacker
// might feed a confused verifier as an HMAC secret.
func publicKeyEncodings(t *testing.T, priv jwk.Key) map[string][]byte {
	t.Helper()
	var raw ecdsa.PrivateKey
	if err := jwk.Export(priv, &raw); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&raw.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		t.Fatal(err)
	}
	jwkJSON, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	point, err := raw.PublicKey.Bytes() // uncompressed 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"pem":   pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}),
		"der":   der,
		"jwk":   jwkJSON,
		"point": point,
	}
}

func TestVerifyAdversarialTokens(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clk := clock.At(now)
	jwks := newJWKSServer(t)
	k1 := jwks.addKey("kid-1")
	k2 := jwks.addKey("kid-2")

	// A JWKS that also publishes keys of other types, as a misconfigured or
	// compromised issuer might.
	rsaRaw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := jwk.Import(rsaRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsaKey.Set(jwk.KeyIDKey, "kid-rsa"); err != nil {
		t.Fatal(err)
	}
	rsaPub, err := jwk.PublicKeyOf(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := jwks.set.AddKey(rsaPub); err != nil {
		t.Fatal(err)
	}
	octSecret := []byte("published-symmetric-key-material")
	octKey, err := jwk.Import(octSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := octKey.Set(jwk.KeyIDKey, "kid-oct"); err != nil {
		t.Fatal(err)
	}
	if err := jwks.set.AddKey(octKey); err != nil {
		t.Fatal(err)
	}

	v := newTestVerifier(jwks, clk, false, "")
	in := VerifyInput{ChannelIDs: []string{testChannel, "other-channel"}, ChannelSecrets: map[string]string{testChannel: testSecret, "other-channel": "other-secret"}}
	noSecret := VerifyInput{ChannelIDs: []string{testChannel}}
	claims := standardClaims(now)
	genuine := signES256(t, buildToken(t, claimsAt(now)), k1)
	if _, err := v.Verify(context.Background(), genuine, in); err != nil {
		t.Fatalf("control token: %v", err)
	}
	pubs := publicKeyEncodings(t, k1)
	attackerEC, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	type tc struct {
		token string
		in    VerifyInput
	}
	cases := map[string]tc{
		// Key confusion: the ES256 public key, in any encoding, presented as
		// the HMAC secret of an HS256 token under the real key ID.
		"HS256 signed with the public key (PEM)":          {rawJWS(t, jwa.HS256(), pubs["pem"], "kid-1", claims), in},
		"HS256 signed with the public key (DER)":          {rawJWS(t, jwa.HS256(), pubs["der"], "kid-1", claims), in},
		"HS256 signed with the public key (JWK)":          {rawJWS(t, jwa.HS256(), pubs["jwk"], "kid-1", claims), in},
		"HS256 signed with the public key (point)":        {rawJWS(t, jwa.HS256(), pubs["point"], "kid-1", claims), in},
		"HS256 with the public key, no secret configured": {rawJWS(t, jwa.HS256(), pubs["pem"], "kid-1", claims), noSecret},
		"HS256 signed with a published oct JWKS key":      {rawJWS(t, jwa.HS256(), octSecret, "kid-oct", claims), noSecret},
		"HS256 signed with another channel's secret":      {rawJWS(t, jwa.HS256(), []byte("other-secret"), "", claims), in},
		// Algorithm header games on a genuinely signed token.
		"alg switched to HS256, ES256 signature kept": {rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "HS256" }), in},
		"alg none, empty signature":                   {strings.Join(strings.Split(rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "none" }), ".")[:2], ".") + ".", in},
		"alg none, ES256 signature kept":              {rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "none" }), in},
		"alg None (mixed case)":                       {rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "None" }), in},
		"alg NONE (upper case)":                       {rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "NONE" }), in},
		"alg missing":                                 {rewriteHeader(t, genuine, func(h map[string]any) { delete(h, "alg") }), in},
		"alg ES384 on a P-256 signature":              {rewriteHeader(t, genuine, func(h map[string]any) { h["alg"] = "ES384" }), in},
		// Key selection.
		"kid points at another published key":       {rewriteHeader(t, genuine, func(h map[string]any) { h["kid"] = "kid-2" }), in},
		"kid missing":                               {rewriteHeader(t, genuine, func(h map[string]any) { delete(h, "kid") }), in},
		"kid of an RSA key, ES256 by attacker":      {rawJWS(t, jwa.ES256(), attackerEC, "kid-rsa", claims), in},
		"kid of an oct key, ES256 by attacker":      {rawJWS(t, jwa.ES256(), attackerEC, "kid-oct", claims), in},
		"RS256 signed with the published RSA key":   {rawJWS(t, jwa.RS256(), rsaKey, "kid-rsa", claims), in},
		"kid of a published key, attacker's EC key": {rawJWS(t, jwa.ES256(), attackerEC, "kid-1", claims), in},
		"signed by kid-2 but header says kid-1":     {rewriteHeader(t, signES256(t, buildToken(t, claimsAt(now)), k2), func(h map[string]any) { h["kid"] = "kid-1" }), in},
		// Audience.
		"aud array with the channel first":  {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", []string{testChannel, "other-channel"})), in},
		"aud array with the channel second": {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", []string{"other-channel", testChannel})), in},
		"aud empty array":                   {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", []string{})), in},
		"aud missing":                       {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", nil)), in},
		"aud is a number":                   {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", 1661234567)), in},
		"aud differs only in case":          {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "aud", "1661234567a")), VerifyInput{ChannelIDs: []string{"1661234567A"}}},
		// Issuer.
		"iss with a trailing slash": {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iss", DefaultIssuer+"/")), in},
		"iss in another case":       {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iss", "https://ACCESS.line.me")), in},
		"iss with a subpath":        {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iss", DefaultIssuer+"/oauth2")), in},
		"iss missing":               {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iss", nil)), in},
		// Lifetime.
		"exp exactly skew ago":    {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "exp", now.Add(-idTokenSkew).Unix())), in},
		"exp missing":             {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "exp", nil)), in},
		"exp is a string":         {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "exp", "2099-01-01")), in},
		"iat missing":             {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iat", nil)), in},
		"iat after skew from now": {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "iat", now.Add(idTokenSkew+time.Second).Unix())), in},
		// Subject and nonce.
		"sub missing":                     {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "sub", nil)), in},
		"sub empty":                       {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "sub", "")), in},
		"nonce required, token has none":  {rawJWS(t, jwa.ES256(), k1, "kid-1", claims), VerifyInput{ChannelIDs: []string{testChannel}, Nonce: "n-1"}},
		"nonce required, token has empty": {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "nonce", "")), VerifyInput{ChannelIDs: []string{testChannel}, Nonce: "n-1"}},
		"nonce differs only in case":      {rawJWS(t, jwa.ES256(), k1, "kid-1", with(claims, "nonce", "N-1")), VerifyInput{ChannelIDs: []string{testChannel}, Nonce: "n-1"}},
		// Shape.
		"two segments":        {strings.Join(strings.Split(genuine, ".")[:2], "."), in},
		"four segments":       {genuine + ".extra", in},
		"empty":               {"", in},
		"whitespace only":     {"   \n", in},
		"payload not JSON":    {strings.Split(genuine, ".")[0] + "." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + "." + strings.Split(genuine, ".")[2], in},
		"10 KiB of garbage":   {strings.Repeat("A", 10<<10), in},
		"10 KiB with dots":    {strings.Repeat("A", 4<<10) + "." + strings.Repeat("B", 4<<10) + "." + strings.Repeat("C", 2<<10), in},
		"signature truncated": {genuine[:len(genuine)-8], in},
		"signature bit flip":  {flipSignatureByte(genuine), in},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), c.token, c.in)
			if !isUnauthorized(err) {
				t.Fatalf("accepted or wrong error: %v", err)
			}
			ae, _ := apierr.As(err)
			if ae.Message != errBadIDToken.Message {
				t.Errorf("message = %q, every rejection must read the same", ae.Message)
			}
		})
	}
	// None of the HS256 attempts may have triggered a JWKS download, and
	// the ES256 attempts all resolved against the single initial download.
	if n := jwks.count.Load(); n != 1 {
		t.Errorf("JWKS downloads = %d, want 1 (one for the control token)", n)
	}
	// The verifier is not broken by the onslaught: the control still passes.
	if _, err := v.Verify(context.Background(), genuine, in); err != nil {
		t.Errorf("control token after the table: %v", err)
	}
}

// flipSignatureByte changes the first character of the signature segment,
// which alters the first signature byte (the final character only carries
// padding bits that a lenient decoder ignores).
func flipSignatureByte(token string) string {
	i := strings.LastIndex(token, ".") + 1
	repl := "A"
	if token[i] == 'A' {
		repl = "B"
	}
	return token[:i] + repl + token[i+1:]
}

// Invariant: a JWS in JSON serialization with several signatures is not an
// ID token, even when one of its signatures is genuine.
func TestVerifyRejectsMultiSignatureJWS(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	jwks := newJWKSServer(t)
	k1 := jwks.addKey("kid-1")
	k2 := jwks.addKey("kid-2")
	v := newTestVerifier(jwks, clock.At(now), false, "")
	body, err := json.Marshal(standardClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	multi, err := jws.Sign(body, jws.WithKey(jwa.ES256(), k1), jws.WithKey(jwa.ES256(), k2), jws.WithJSON())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), string(multi), VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
		t.Errorf("multi-signature JWS: %v", err)
	}
	// A single-signature JSON serialization is also refused: LINE only
	// issues compact tokens, so nothing else needs parsing.
	single, err := jws.Sign(body, jws.WithKey(jwa.ES256(), k1), jws.WithJSON())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), string(single), VerifyInput{ChannelIDs: []string{testChannel}}); !isUnauthorized(err) {
		t.Errorf("JSON-serialized JWS: %v", err)
	}
}

// Documented behaviour at the edges that are accepted on purpose: exp is
// checked with a 30-second skew, a single-element aud array is the same as
// a string under RFC 7519, a nonce in the token is ignored when the app did
// not ask for one, and the verifier itself puts no size limit on tokens
// (lineauth enforces 4096 bytes before calling it).
func TestVerifyAcceptedEdges(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	jwks := newJWKSServer(t)
	k1 := jwks.addKey("kid-1")
	v := newTestVerifier(jwks, clock.At(now), false, "")
	in := VerifyInput{ChannelIDs: []string{testChannel}}
	claims := standardClaims(now)

	cases := map[string]map[string]any{
		"exp exactly now":                with(claims, "exp", now.Unix()),
		"exp one second inside the skew": with(claims, "exp", now.Add(-idTokenSkew+time.Second).Unix()),
		"iat exactly skew from now":      with(claims, "iat", now.Add(idTokenSkew).Unix()),
		"aud as a one-element array":     with(claims, "aud", []string{testChannel}),
		"nonce present but not required": with(claims, "nonce", "n-unrequested"),
		"5 KiB of extra claims":          with(claims, "picture", "https://profile.line-scdn.net/"+strings.Repeat("p", 5<<10)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := v.Verify(context.Background(), rawJWS(t, jwa.ES256(), k1, "kid-1", payload), in)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if c.Sub != "U0123456789abcdef" || c.Aud != testChannel {
				t.Errorf("claims = %+v", c)
			}
		})
	}
}

// Invariant: when the verify endpoint is used, its answer is checked
// against what the client asked for. A LINE response naming another
// channel, another issuer, no subject, or an expired token is refused;
// a transport failure is 503, not a silent accept.
func TestVerifyEndpointFallbackRejectsMismatchedAnswers(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	type answer struct {
		status int
		body   string
	}
	var next answer
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("id_token") == "" || r.PostForm.Get("client_id") != testChannel {
			t.Errorf("fallback request: form=%v err=%v", r.PostForm, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		_, _ = io.WriteString(w, next.body)
	}))
	defer verify.Close()
	jwks := newJWKSServer(t) // no keys: every ES256 token falls back
	v := NewVerifier(VerifierOptions{HTTPClient: verify.Client(), JWKSURL: jwks.srv.URL, VerifyURL: verify.URL, Clock: clock.At(now),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), VerifyEndpointFallback: true})
	stray := newJWKSServer(t).addKey("kid-x")
	token := signES256(t, buildToken(t, claimsAt(now)), stray)
	in := VerifyInput{ChannelIDs: []string{testChannel, "other-channel"}, Nonce: "n-1"}

	good := func(mut func(m map[string]any)) string {
		m := map[string]any{"iss": DefaultIssuer, "sub": "U-remote", "aud": testChannel, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "n-1"}
		mut(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := map[string]answer{
		"client_id of another configured channel": {200, good(func(m map[string]any) { m["aud"] = "other-channel" })},
		"client_id of an unknown channel":         {200, good(func(m map[string]any) { m["aud"] = "9999999999" })},
		"aud missing":                             {200, good(func(m map[string]any) { delete(m, "aud") })},
		"issuer with trailing slash":              {200, good(func(m map[string]any) { m["iss"] = DefaultIssuer + "/" })},
		"issuer missing":                          {200, good(func(m map[string]any) { delete(m, "iss") })},
		"empty subject":                           {200, good(func(m map[string]any) { m["sub"] = "" })},
		"subject missing":                         {200, good(func(m map[string]any) { delete(m, "sub") })},
		"expired by exactly the skew":             {200, good(func(m map[string]any) { m["exp"] = now.Add(-idTokenSkew).Unix() })},
		"exp missing":                             {200, good(func(m map[string]any) { delete(m, "exp") })},
		"nonce missing":                           {200, good(func(m map[string]any) { delete(m, "nonce") })},
		"nonce differs":                           {200, good(func(m map[string]any) { m["nonce"] = "n-2" })},
		"LINE says 400":                           {400, `{"error":"invalid_request","error_description":"Invalid IdToken."}`},
		"LINE says 500":                           {500, ``},
		"LINE says 200 with HTML":                 {200, `<html>maintenance</html>`},
		"LINE says 200 with an empty object":      {200, `{}`},
		"LINE says 200 with a JSON array":         {200, `[]`},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			next = a
			if _, err := v.Verify(context.Background(), token, in); !isUnauthorized(err) {
				t.Errorf("accepted or wrong error: %v", err)
			}
		})
	}
	// Control: the matching answer verifies.
	next = answer{200, good(func(map[string]any) {})}
	c, err := v.Verify(context.Background(), token, in)
	if err != nil || c.Sub != "U-remote" || c.Aud != testChannel {
		t.Fatalf("control: %v %+v", err, c)
	}
	// Transport failure: the client sees 503 and no claims.
	verify.Close()
	_, err = v.Verify(context.Background(), token, in)
	ae, ok := apierr.As(err)
	if !ok || ae.Status != http.StatusServiceUnavailable {
		t.Errorf("unreachable verify endpoint: %v", err)
	}
}

// Invariant: the acceptable clock skew is small and symmetric, so a token
// is valid from iat minus skew until exp plus skew, and the boundaries are
// exact to the second.
func TestVerifyExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	jwks := newJWKSServer(t)
	k1 := jwks.addKey("kid-1")
	clk := clock.At(now)
	v := newTestVerifier(jwks, clk, false, "")
	in := VerifyInput{ChannelIDs: []string{testChannel}}
	exp := now.Add(10 * time.Minute)
	token := rawJWS(t, jwa.ES256(), k1, "kid-1", with(standardClaims(now), "exp", exp.Unix()))

	steps := []struct {
		at   time.Time
		want bool
	}{
		{exp.Add(-time.Second), true},
		{exp, true},
		{exp.Add(idTokenSkew - time.Second), true},
		{exp.Add(idTokenSkew), false},
		{exp.Add(time.Hour), false},
	}
	for _, s := range steps {
		clk.Set(s.at)
		_, err := v.Verify(context.Background(), token, in)
		if got := err == nil; got != s.want {
			t.Errorf("at exp%+v: accepted=%v, want %v (%v)", s.at.Sub(exp), got, s.want, err)
		}
	}
	// Time cannot be rewound to accept a token issued in the future.
	clk.Set(now.Add(-idTokenSkew - time.Second))
	if _, err := v.Verify(context.Background(), token, in); err == nil {
		t.Error("token issued after now plus skew accepted")
	}
	clk.Set(now.Add(-idTokenSkew))
	if _, err := v.Verify(context.Background(), token, in); err != nil {
		t.Errorf("token issued exactly skew in the future rejected: %v", err)
	}
}
