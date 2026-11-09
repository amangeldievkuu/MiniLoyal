package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

func TestHash(t *testing.T) {
	a := Hash("POST", "/v1/x", []byte(`{"a":1}`))
	b := Hash("POST", "/v1/x", []byte(`{"a":1}`))
	c := Hash("POST", "/v1/x", []byte(`{"a":2}`))
	d := Hash("PATCH", "/v1/x", []byte(`{"a":1}`))
	if !bytes.Equal(a, b) || bytes.Equal(a, c) || bytes.Equal(a, d) || len(a) != 32 {
		t.Fatal("hash must be deterministic over method, path and body")
	}
}

func TestRunValidatesInput(t *testing.T) {
	s := New(nil, nil, nil)
	_, err := s.Run(context.Background(), Request{TenantID: uuid.New(), Scope: "user:x", Hash: []byte{1}}, nil)
	if ae, ok := apierr.As(err); !ok || ae.Status != 400 {
		t.Fatalf("missing key: %v", err)
	}
}

type outcome struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestRunExecutesOnceAndReplays(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, NewEncryptedCodec(crypto.Static(bytes.Repeat([]byte{3}, 32))), nil)
	ctx := context.Background()

	var runs atomic.Int32
	fn := func(context.Context, pgx.Tx) (int, any, error) {
		runs.Add(1)
		return 201, outcome{ID: "app_1", Name: "Cafe"}, nil
	}
	req := Request{TenantID: tenant, Scope: Scope("user", uuid.New()), Key: "k1", Hash: Hash("POST", "/p", []byte(`{}`))}

	first, err := s.Run(ctx, req, fn)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Run(ctx, req, fn)
	if err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("fn ran %d times", runs.Load())
	}
	if first.Replayed || !second.Replayed || first.Status != 201 || second.Status != 201 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	var got outcome
	if err := json.Unmarshal(second.Body, &got); err != nil || got != (outcome{ID: "app_1", Name: "Cafe"}) {
		t.Fatalf("replayed body %s: %v", second.Body, err)
	}

	// The row stores ciphertext, not the outcome.
	var stored []byte
	if err := d.Admin.QueryRow(ctx, "SELECT response_body FROM idempotency_keys WHERE key = 'k1'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("Cafe")) {
		t.Fatal("stored outcome is not encrypted")
	}

	// Same key, different request.
	_, err = s.Run(ctx, Request{TenantID: tenant, Scope: req.Scope, Key: "k1", Hash: Hash("POST", "/p", []byte(`{"x":1}`))}, fn)
	if ae, ok := apierr.As(err); !ok || ae.Status != 422 {
		t.Fatalf("mismatch: %v", err)
	}
	// Same key, another principal: independent.
	other, err := s.Run(ctx, Request{TenantID: tenant, Scope: Scope("user", uuid.New()), Key: "k1", Hash: req.Hash}, fn)
	if err != nil || other.Replayed {
		t.Fatalf("other scope: %+v %v", other, err)
	}
}

func TestRunRollsBackOnError(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, nil, nil)
	ctx := context.Background()
	req := Request{TenantID: tenant, Scope: "user:a", Key: "boom", Hash: Hash("POST", "/p", nil)}

	boom := errors.New("boom")
	_, err := s.Run(ctx, req, func(context.Context, pgx.Tx) (int, any, error) { return 0, nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	// The key was released, so a retry runs the work.
	res, err := s.Run(ctx, req, func(context.Context, pgx.Tx) (int, any, error) { return 200, "ok", nil })
	if err != nil || res.Replayed {
		t.Fatalf("retry after failure: %+v %v", res, err)
	}
}

func TestInFlightDuplicateIsConflict(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, nil, nil)
	ctx := context.Background()
	hash := Hash("POST", "/p", nil)
	d.Exec(t, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at) VALUES ($1, 'user:a', 'inflight', $2, now() + interval '1 day')", tenant, hash)

	_, err := s.Run(ctx, Request{TenantID: tenant, Scope: "user:a", Key: "inflight", Hash: hash}, func(context.Context, pgx.Tx) (int, any, error) {
		t.Fatal("fn must not run")
		return 0, nil, nil
	})
	if ae, ok := apierr.As(err); !ok || ae.Status != 409 {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentDuplicatesRunOnce(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, nil, nil)
	req := Request{TenantID: tenant, Scope: "user:a", Key: "race", Hash: Hash("POST", "/p", nil)}

	var runs atomic.Int32
	var replays atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Run(context.Background(), req, func(context.Context, pgx.Tx) (int, any, error) {
				runs.Add(1)
				return 201, map[string]int{"n": 1}, nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			if res.Replayed {
				replays.Add(1)
			}
		}()
	}
	wg.Wait()
	if runs.Load() != 1 || replays.Load() != 7 {
		t.Fatalf("runs = %d, replays = %d", runs.Load(), replays.Load())
	}
}

func TestDeleteExpired(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	s := New(d.App, nil, nil)
	d.Exec(t, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, status_code, expires_at) VALUES ($1, 'user:a', 'old', '\\x00', 200, now() - interval '1 hour')", tenant)
	d.Exec(t, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, status_code, expires_at) VALUES ($1, 'user:a', 'fresh', '\\x00', 200, now() + interval '1 hour')", tenant)
	n, err := s.DeleteExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
}
