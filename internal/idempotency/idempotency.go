// Package idempotency makes write requests safe to retry. The first request
// with a given (tenant, scope, key) runs and stores its outcome; a retry with
// the same request replays that outcome; the same key with a different
// request is refused.
//
// Everything happens inside one tenant transaction: claiming the key, running
// the work and storing the outcome. If the work fails the transaction rolls
// back, the key is released and the client may retry.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

// TTL is how long an outcome is kept for replay.
const TTL = 24 * time.Hour

// Store runs idempotent writes against a pool.
type Store struct {
	pool  *pgxpool.Pool
	codec Codec
	clock clock.Clock
}

// New returns a Store. codec nil means plain JSON; use NewEncryptedCodec when
// stored outcomes may hold secrets (API keys, invite codes).
func New(pool *pgxpool.Pool, codec Codec, clk clock.Clock) *Store {
	if codec == nil {
		codec = JSONCodec{}
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Store{pool: pool, codec: codec, clock: clk}
}

// Request identifies one logical write.
type Request struct {
	// TenantID scopes the key; it is also the RLS context of the transaction.
	TenantID uuid.UUID
	// Scope is the principal, for example "user:<uuid>" or "apikey:<uuid>",
	// so two callers cannot collide on the same key.
	Scope string
	// Key is the client's Idempotency-Key header.
	Key string
	// Hash is Hash(method, path, body) of the request.
	Hash []byte
	// Before runs inside the tenant transaction before the key is claimed or
	// replayed, so authorization applies to replays too.
	Before func(ctx context.Context, tx pgx.Tx) error
}

// Result is the outcome of Run.
type Result struct {
	Status int
	// Body is the stored outcome, as produced by the codec. Replays hand it
	// back exactly as the first run stored it.
	Body json.RawMessage
	// Replayed is true when fn did not run because the outcome was stored.
	Replayed bool
}

// Fn does the work inside the tenant transaction and returns the status and
// the value to store for replays.
type Fn func(ctx context.Context, tx pgx.Tx) (status int, body any, err error)

// Run claims req, runs fn once and stores its outcome, or replays a stored
// outcome. Errors from fn roll everything back and are returned as is.
func (s *Store) Run(ctx context.Context, req Request, fn Fn) (Result, error) {
	if req.Key == "" {
		return Result{}, apierr.BadRequest("Idempotency-Key header is required")
	}
	if len(req.Key) > 255 {
		return Result{}, apierr.BadRequest("Idempotency-Key is too long")
	}
	if req.Scope == "" || len(req.Hash) == 0 {
		return Result{}, errors.New("idempotency: scope and hash are required")
	}
	var res Result
	err := db.WithTenant(ctx, s.pool, req.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		if req.Before != nil {
			if err := req.Before(ctx, tx); err != nil {
				return err
			}
		}
		// A stale outcome past its TTL is treated as absent.
		if err := q.ExpireIdempotencyKey(ctx, sqlc.ExpireIdempotencyKeyParams{TenantID: req.TenantID, Scope: req.Scope, Key: req.Key}); err != nil {
			return fmt.Errorf("idempotency: expire: %w", err)
		}
		claim, err := q.ClaimIdempotencyKey(ctx, sqlc.ClaimIdempotencyKeyParams{
			TenantID:    req.TenantID,
			Scope:       req.Scope,
			Key:         req.Key,
			RequestHash: req.Hash,
			ExpiresAt:   pgtype.Timestamptz{Time: s.clock.Now().Add(TTL), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("idempotency: claim: %w", err)
		}
		if !claim.Inserted {
			if !hashEqual(claim.RequestHash, req.Hash) {
				return apierr.IdempotencyMismatch()
			}
			if claim.StatusCode == nil {
				return apierr.Conflict("a request with this Idempotency-Key is still in progress")
			}
			body, err := s.codec.Decode(ctx, aad(req), claim.ResponseBody)
			if err != nil {
				return fmt.Errorf("idempotency: decode stored outcome: %w", err)
			}
			res = Result{Status: int(*claim.StatusCode), Body: body, Replayed: true}
			return nil
		}

		status, body, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		encoded, err := s.codec.Encode(ctx, aad(req), body)
		if err != nil {
			return fmt.Errorf("idempotency: encode outcome: %w", err)
		}
		code := int32(status)
		if err := q.CompleteIdempotencyKey(ctx, sqlc.CompleteIdempotencyKeyParams{
			TenantID:     req.TenantID,
			Scope:        req.Scope,
			Key:          req.Key,
			StatusCode:   &code,
			ResponseBody: encoded,
		}); err != nil {
			return fmt.Errorf("idempotency: store outcome: %w", err)
		}
		plain, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("idempotency: marshal outcome: %w", err)
		}
		res = Result{Status: status, Body: plain}
		return nil
	})
	return res, err
}

// DeleteExpired removes outcomes past their TTL. Meant for a periodic job.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	var n int64
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		n, err = sqlc.New(tx).DeleteExpiredIdempotencyKeys(ctx)
		return err
	})
	return n, err
}

// Hash fingerprints a request: method, path and the JSON body.
func Hash(method, path string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{'\n'})
	h.Write([]byte(path))
	h.Write([]byte{'\n'})
	h.Write(body)
	return h.Sum(nil)
}

// Scope names a principal for Request.Scope.
func Scope(kind string, id uuid.UUID) string {
	return kind + ":" + id.String()
}

func aad(req Request) string {
	return "idempotency_keys.response_body:" + req.TenantID.String() + ":" + req.Scope + ":" + req.Key
}

func hashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// Codec turns an outcome into the bytes stored in response_body and back.
// Decode returns plain JSON.
type Codec interface {
	Encode(ctx context.Context, aad string, v any) ([]byte, error)
	Decode(ctx context.Context, aad string, stored []byte) (json.RawMessage, error)
}

// JSONCodec stores outcomes as plain JSON.
type JSONCodec struct{}

func (JSONCodec) Encode(_ context.Context, _ string, v any) ([]byte, error) {
	return json.Marshal(v)
}

func (JSONCodec) Decode(_ context.Context, _ string, stored []byte) (json.RawMessage, error) {
	if !json.Valid(stored) {
		return nil, errors.New("stored outcome is not JSON")
	}
	return json.RawMessage(stored), nil
}

// encryptedCodec stores outcomes encrypted with the keyring, as the JSON
// document {"enc": "<base64>"}, because outcomes can carry secrets shown
// once (API keys, invite codes).
type encryptedCodec struct {
	keyring crypto.Keyring
}

// NewEncryptedCodec returns a Codec that encrypts stored outcomes.
func NewEncryptedCodec(k crypto.Keyring) Codec {
	return encryptedCodec{keyring: k}
}

type encryptedDoc struct {
	Enc string `json:"enc"`
}

func (c encryptedCodec) Encode(ctx context.Context, aad string, v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	ct, err := c.keyring.Encrypt(ctx, plain, aad)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encryptedDoc{Enc: base64.StdEncoding.EncodeToString(ct)})
}

func (c encryptedCodec) Decode(ctx context.Context, aad string, stored []byte) (json.RawMessage, error) {
	var doc encryptedDoc
	if err := json.Unmarshal(stored, &doc); err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(doc.Enc)
	if err != nil {
		return nil, err
	}
	plain, err := c.keyring.Decrypt(ctx, ct, aad)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(plain), nil
}
