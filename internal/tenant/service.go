// Package tenant manages tenants, their teams, apps, LINE channels, stores,
// staff and API keys. Every method takes the calling principal, authorizes it
// against the tenant's membership table inside a tenant-scoped transaction,
// writes an audit entry for every change, and runs writes through the
// idempotency store.
package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Service is the tenant domain.
type Service struct {
	pool    *pgxpool.Pool
	keyring crypto.Keyring
	idem    *idempotency.Store
	clock   clock.Clock
	log     *slog.Logger
}

// New returns a Service. Idempotent outcomes are stored encrypted with the
// keyring because some of them carry secrets shown once.
func New(pool *pgxpool.Pool, keyring crypto.Keyring, clk clock.Clock, log *slog.Logger) *Service {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		pool:    pool,
		keyring: keyring,
		idem:    idempotency.New(pool, idempotency.NewEncryptedCodec(keyring), clk),
		clock:   clk,
		log:     log,
	}
}

// Idem is the client's idempotency key and the request fingerprint.
type Idem struct {
	Key  string
	Hash []byte
}

// newID returns a time-ordered UUID.
func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// scopeOf names the principal for idempotency keys.
func scopeOf(p principal.Principal) string {
	if p.IsAPIKey() {
		return idempotency.Scope("apikey", p.APIKeyID)
	}
	return idempotency.Scope("user", p.UserID)
}

// membership returns the caller's role in tenantID or NotFound. It must run
// inside a transaction scoped to tenantID.
func (s *Service) membership(ctx context.Context, tx pgx.Tx, p principal.Principal, tenantID uuid.UUID) (principal.Role, error) {
	if !p.IsUser() {
		return "", apierr.NotFound("tenant")
	}
	m, err := sqlc.New(tx).GetMembership(ctx, sqlc.GetMembershipParams{TenantID: tenantID, UserID: p.UserID})
	if err != nil {
		if db.IsNotFound(err) {
			return "", apierr.NotFound("tenant")
		}
		return "", fmt.Errorf("tenant: membership: %w", err)
	}
	return principal.Role(m.Role), nil
}

// authorize checks membership and that the role grants at least min.
func (s *Service) authorize(ctx context.Context, tx pgx.Tx, p principal.Principal, tenantID uuid.UUID, min principal.Role) (principal.Role, error) {
	role, err := s.membership(ctx, tx, p, tenantID)
	if err != nil {
		return "", err
	}
	if !role.AtLeast(min) {
		return role, apierr.Forbidden("your role does not allow this")
	}
	return role, nil
}

// read runs fn inside tenantID's transaction after checking the caller has
// at least role min.
func (s *Service) read(ctx context.Context, p principal.Principal, tenantID uuid.UUID, min principal.Role, fn func(ctx context.Context, q *sqlc.Queries, role principal.Role) error) error {
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		role, err := s.authorize(ctx, tx, p, tenantID, min)
		if err != nil {
			return err
		}
		return fn(ctx, sqlc.New(tx), role)
	})
}

// write runs fn once per idempotency key inside tenantID's transaction after
// checking the caller has at least role min. Replays decode the stored
// outcome into T; fresh runs return fn's value directly.
func write[T any](ctx context.Context, s *Service, p principal.Principal, tenantID uuid.UUID, min principal.Role, idem Idem, fn func(ctx context.Context, tx pgx.Tx, role principal.Role) (T, error)) (T, error) {
	var fresh T
	var zero T
	res, err := s.idem.Run(ctx, idempotency.Request{
		TenantID: tenantID,
		Scope:    scopeOf(p),
		Key:      idem.Key,
		Hash:     idem.Hash,
	}, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		role, err := s.authorize(ctx, tx, p, tenantID, min)
		if err != nil {
			return 0, nil, err
		}
		v, err := fn(ctx, tx, role)
		if err != nil {
			return 0, nil, err
		}
		fresh = v
		return 200, v, nil
	})
	if err != nil {
		return zero, err
	}
	if !res.Replayed {
		return fresh, nil
	}
	var out T
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return zero, fmt.Errorf("tenant: decode replay: %w", err)
	}
	return out, nil
}

// tenantOf resolves an app, store, staff member or API key to its tenant
// with a single-column lookup that bypasses tenant policies. The caller
// then checks membership, so a non-member learns nothing beyond 404.
func (s *Service) tenantOf(ctx context.Context, what string, lookup func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error)) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		id, err := lookup(ctx, sqlc.New(tx))
		if err != nil {
			if db.IsNotFound(err) {
				return apierr.NotFound(what)
			}
			return fmt.Errorf("tenant: resolve %s: %w", what, err)
		}
		tenantID = id
		return nil
	})
	return tenantID, err
}

func (s *Service) appTenant(ctx context.Context, appID uuid.UUID) (uuid.UUID, error) {
	return s.tenantOf(ctx, "app", func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error) {
		return q.GetAppTenant(ctx, appID)
	})
}

func (s *Service) storeTenant(ctx context.Context, storeID uuid.UUID) (uuid.UUID, error) {
	return s.tenantOf(ctx, "store", func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error) {
		return q.GetStoreTenant(ctx, storeID)
	})
}

func (s *Service) staffTenant(ctx context.Context, staffID uuid.UUID) (uuid.UUID, error) {
	return s.tenantOf(ctx, "staff member", func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error) {
		return q.GetStaffTenant(ctx, staffID)
	})
}

func (s *Service) apiKeyTenant(ctx context.Context, keyID uuid.UUID) (uuid.UUID, error) {
	return s.tenantOf(ctx, "API key", func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error) {
		return q.GetApiKeyTenant(ctx, keyID)
	})
}

// notFound maps pgx.ErrNoRows to a client-visible 404.
func notFound(err error, what string) error {
	if db.IsNotFound(err) {
		return apierr.NotFound(what)
	}
	return err
}
