package idempotency

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Invariant: the same Idempotency-Key used by two principals at once is two
// independent writes. Each scope runs exactly once and replays only its own
// outcome, however the goroutines interleave.
func TestConcurrentScopesRunIndependently(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, nil, nil)
	hash := Hash("POST", "/p", []byte(`{"n":1}`))
	scopes := []string{Scope("user", uuid.New()), Scope("apikey", uuid.New())}

	const goroutines = 16
	var runs, replays [2]atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		idx := i % 2
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := s.Run(context.Background(), Request{TenantID: tenant, Scope: scopes[idx], Key: "shared", Hash: hash}, func(context.Context, pgx.Tx) (int, any, error) {
				runs[idx].Add(1)
				return 201, map[string]string{"scope": scopes[idx]}, nil
			})
			if err != nil {
				errs <- err
				return
			}
			if res.Replayed {
				replays[idx].Add(1)
			}
			var body map[string]string
			if err := json.Unmarshal(res.Body, &body); err != nil {
				errs <- err
				return
			}
			if body["scope"] != scopes[idx] || res.Status != 201 {
				t.Errorf("scope %d got outcome %+v %s", idx, res, res.Body)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for idx := range scopes {
		if runs[idx].Load() != 1 || replays[idx].Load() != goroutines/2-1 {
			t.Errorf("scope %d: runs = %d, replays = %d", idx, runs[idx].Load(), replays[idx].Load())
		}
	}
	var rows int
	if err := d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = 'shared'", tenant).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("stored rows = %d, want one per scope", rows)
	}
}

// Invariant: a key is scoped to its tenant as well. The same scope and key
// in another tenant runs again and never sees the first tenant's outcome.
func TestSameKeyInAnotherTenantRunsAgain(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")
	s := New(d.App, nil, nil)
	scope := Scope("user", uuid.New())
	hash := Hash("POST", "/p", nil)

	first, err := s.Run(context.Background(), Request{TenantID: tenantA, Scope: scope, Key: "k", Hash: hash}, func(context.Context, pgx.Tx) (int, any, error) {
		return 201, map[string]string{"tenant": "A"}, nil
	})
	if err != nil || first.Replayed {
		t.Fatal(err, first)
	}
	second, err := s.Run(context.Background(), Request{TenantID: tenantB, Scope: scope, Key: "k", Hash: hash}, func(context.Context, pgx.Tx) (int, any, error) {
		return 201, map[string]string{"tenant": "B"}, nil
	})
	if err != nil || second.Replayed || tenantIn(t, second.Body) != "B" {
		t.Fatalf("tenant B: %+v %s %v", second, second.Body, err)
	}
	// A mismatching payload in tenant B does not disturb tenant A's record.
	if _, err := s.Run(context.Background(), Request{TenantID: tenantB, Scope: scope, Key: "k", Hash: Hash("POST", "/p", []byte("x"))}, nil); err == nil {
		t.Error("mismatch in tenant B should fail")
	}
	again, err := s.Run(context.Background(), Request{TenantID: tenantA, Scope: scope, Key: "k", Hash: hash}, nil)
	if err != nil || !again.Replayed || tenantIn(t, again.Body) != "A" {
		t.Fatalf("tenant A replay: %+v %s %v", again, again.Body, err)
	}
}

// tenantIn decodes the stored outcome; jsonb normalises spacing, so bodies
// are compared by value.
func tenantIn(t *testing.T, body []byte) string {
	t.Helper()
	var v map[string]string
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v["tenant"]
}
