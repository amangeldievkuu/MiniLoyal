package line

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// fakeTokenServer issues tokens for known channel credentials and counts
// requests per channel.
type fakeTokenServer struct {
	srv   *httptest.Server
	creds map[string]string // channel ID -> secret
	mu    sync.Mutex
	count map[string]int
	seq   atomic.Int32
}

func newFakeTokenServer(t *testing.T, creds map[string]string) *fakeTokenServer {
	t.Helper()
	f := &fakeTokenServer{creds: creds, count: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/v3/token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		f.mu.Lock()
		f.count[id]++
		f.mu.Unlock()
		if r.PostForm.Get("grant_type") != "client_credentials" || f.creds[id] == "" || f.creds[id] != secret {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"invalid client credentials"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token_type": "Bearer", "access_token": "tok-" + id + "-" + itoa(int(f.seq.Add(1))), "expires_in": 900})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (f *fakeTokenServer) requests(channel string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count[channel]
}

func TestTokenSourceSharesOneRefreshAcrossGoroutines(t *testing.T) {
	f := newFakeTokenServer(t, map[string]string{"2001": "secret-a"})
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	ts := NewTokenSource(TokenSourceOptions{HTTPClient: f.srv.Client(), TokenURL: f.srv.URL + "/oauth2/v3/token", Clock: clk})

	var wg sync.WaitGroup
	tokens := make([]string, 20)
	start := make(chan struct{})
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tok, err := ts.Token(context.Background(), "2001", "secret-a")
			if err != nil {
				t.Error(err)
			}
			tokens[i] = tok
		}(i)
	}
	close(start)
	wg.Wait()
	for _, tok := range tokens[1:] {
		if tok != tokens[0] {
			t.Fatalf("goroutines received different tokens: %q vs %q", tokens[0], tok)
		}
	}
	if n := f.requests("2001"); n != 1 {
		t.Errorf("token requests = %d, want 1", n)
	}
}

func TestTokenSourceRenewsEarlyAndIsolatesChannels(t *testing.T) {
	f := newFakeTokenServer(t, map[string]string{"2001": "secret-a", "2002": "secret-b"})
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	ts := NewTokenSource(TokenSourceOptions{HTTPClient: f.srv.Client(), TokenURL: f.srv.URL + "/oauth2/v3/token", Clock: clk})
	ctx := context.Background()

	first, err := ts.Token(ctx, "2001", "secret-a")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(13 * time.Minute)
	if again, _ := ts.Token(ctx, "2001", "secret-a"); again != first {
		t.Error("token renewed too early")
	}
	clk.Advance(61 * time.Second) // 14m01s: inside the 60 s renewal window
	renewed, _ := ts.Token(ctx, "2001", "secret-a")
	if renewed == first || f.requests("2001") != 2 {
		t.Errorf("token not renewed 60 s before expiry (requests %d)", f.requests("2001"))
	}
	other, err := ts.Token(ctx, "2002", "secret-b")
	if err != nil || other == renewed || f.requests("2002") != 1 {
		t.Errorf("channels share a cache: %v %q", err, other)
	}
	ts.Forget("2001")
	if _, err := ts.Token(ctx, "2001", "secret-a"); err != nil || f.requests("2001") != 3 {
		t.Errorf("Forget did not drop the cache: %v %d", err, f.requests("2001"))
	}
}

func TestTokenSourceErrors(t *testing.T) {
	f := newFakeTokenServer(t, map[string]string{"2001": "secret-a"})
	ts := NewTokenSource(TokenSourceOptions{HTTPClient: f.srv.Client(), TokenURL: f.srv.URL + "/oauth2/v3/token"})
	if _, err := ts.Token(context.Background(), "2001", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong secret: %v", err)
	}
	if _, _, err := ts.Mint(context.Background(), "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("empty credentials: %v", err)
	}
	down := NewTokenSource(TokenSourceOptions{HTTPClient: &http.Client{Timeout: time.Second}, TokenURL: "http://127.0.0.1:1/oauth2/v3/token"})
	if _, err := down.Token(context.Background(), "2001", "secret-a"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
}
