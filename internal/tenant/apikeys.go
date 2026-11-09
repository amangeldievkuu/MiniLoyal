package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// KeyPrefix starts every API key.
const KeyPrefix = "ml_"

// APIKeyCreated is a new key with its plaintext, shown once.
type APIKeyCreated struct {
	sqlc.ApiKey
	Key string `json:"key"`
}

// ListAPIKeys returns the app's active keys.
func (s *Service) ListAPIKeys(ctx context.Context, p principal.Principal, appID uuid.UUID) ([]sqlc.ApiKey, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return nil, err
	}
	var keys []sqlc.ApiKey
	err = s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		keys, err = q.ListApiKeys(ctx, appID)
		return err
	})
	if keys == nil {
		keys = []sqlc.ApiKey{}
	}
	return keys, err
}

// CreateAPIKey mints a key for the app. Owners and admins.
func (s *Service) CreateAPIKey(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, name string, scopes []string) (APIKeyCreated, error) {
	name = strings.TrimSpace(name)
	if fields := validateName("name", name, 100); fields != nil {
		return APIKeyCreated{}, apierr.Validation(fields)
	}
	scopes, err := validateScopes(scopes)
	if err != nil {
		return APIKeyCreated{}, err
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return APIKeyCreated{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (APIKeyCreated, error) {
		key := newAPIKey()
		row, err := sqlc.New(tx).CreateApiKey(ctx, sqlc.CreateApiKeyParams{
			ID: newID(), TenantID: tenantID, AppID: appID, Name: name,
			Prefix: key[:len(KeyPrefix)+8], KeyHash: hashKey(key), Scopes: scopes,
			CreatedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		})
		if err != nil {
			return APIKeyCreated{}, fmt.Errorf("tenant: create API key: %w", err)
		}
		return APIKeyCreated{ApiKey: row, Key: key}, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.APIKeyCreate,
			TargetType: "api_key", TargetID: row.ID.String(),
			Metadata: map[string]any{"name": name, "prefix": row.Prefix, "scopes": scopes},
		})
	})
}

// RevokeAPIKey revokes a key. Owners and admins. An already revoked key is 404.
func (s *Service) RevokeAPIKey(ctx context.Context, p principal.Principal, keyID uuid.UUID) error {
	tenantID, err := s.apiKeyTenant(ctx, keyID)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin); err != nil {
			return err
		}
		q := sqlc.New(tx)
		key, err := q.GetApiKey(ctx, keyID)
		if err != nil {
			return notFound(err, "API key")
		}
		n, err := q.RevokeApiKey(ctx, keyID)
		if err != nil {
			return fmt.Errorf("tenant: revoke API key: %w", err)
		}
		if n == 0 {
			return apierr.NotFound("API key")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &key.AppID, Actor: p, Action: audit.APIKeyRevoke,
			TargetType: "api_key", TargetID: keyID.String(),
			Metadata: map[string]any{"name": key.Name, "prefix": key.Prefix},
		})
	})
}

// AuthenticateAPIKey verifies an X-API-Key value and returns its principal.
// It implements httpserver.APIKeyAuthenticator.
func (s *Service) AuthenticateAPIKey(ctx context.Context, key string) (principal.Principal, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, KeyPrefix) || len(key) < len(KeyPrefix)+20 || len(key) > 128 {
		return principal.Principal{}, apierr.Unauthorized("invalid API key")
	}
	var row sqlc.ApiKey
	var app sqlc.App
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		var err error
		row, err = q.GetApiKeyByHash(ctx, hashKey(key))
		if err != nil {
			if db.IsNotFound(err) {
				return apierr.Unauthorized("invalid API key")
			}
			return fmt.Errorf("tenant: API key lookup: %w", err)
		}
		app, err = q.GetApp(ctx, row.AppID)
		if err != nil {
			if db.IsNotFound(err) {
				return apierr.Unauthorized("invalid API key")
			}
			return fmt.Errorf("tenant: API key app: %w", err)
		}
		return nil
	})
	if err != nil {
		return principal.Principal{}, err
	}
	if app.Status != "active" {
		return principal.Principal{}, apierr.Unauthorized("this app is disabled")
	}
	if !row.LastUsedAt.Valid || s.clock.Now().Sub(row.LastUsedAt.Time) > time.Minute {
		go s.touchAPIKey(row)
	}
	return principal.Principal{
		Kind:     principal.KindAPIKey,
		APIKeyID: row.ID,
		TenantID: row.TenantID,
		AppID:    row.AppID,
		Scopes:   row.Scopes,
		IssuedAt: row.CreatedAt.Time,
	}, nil
}

// touchAPIKey records a use without holding up the request.
func (s *Service) touchAPIKey(row sqlc.ApiKey) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := db.WithTenant(ctx, s.pool, row.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return sqlc.New(tx).TouchApiKey(ctx, row.ID)
	})
	if err != nil {
		s.log.Debug("api key touch failed", "api_key_id", row.ID, "err", err)
	}
}

func newAPIKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("tenant: random: " + err.Error())
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
