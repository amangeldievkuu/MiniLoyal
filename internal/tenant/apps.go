package tenant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// AppCreate is the input for CreateApp.
type AppCreate struct {
	Name   string
	Slug   *string
	Locale *string
}

// AppUpdate is the input for UpdateApp. Nil means unchanged. For the OA
// fields an empty string clears the value.
type AppUpdate struct {
	Name            *string
	Locale          *string
	Status          *string
	OAChannelID     *string
	OABasicID       *string
	OAChannelSecret *string
	// AllowHS256 accepts HS256 ID tokens (LINE web login) for the app.
	AllowHS256 *bool
}

// ChannelPut is the input for PutChannel. The secret is required: it proves
// control of the channel and verifies HS256 tokens.
type ChannelPut struct {
	ChannelID string
	LiffID    string
	Secret    *string
}

func oaSecretAAD(appID uuid.UUID) string {
	return "apps.oa_channel_secret_enc:" + appID.String()
}

func channelSecretAAD(appID uuid.UUID, environment string) string {
	return "app_channels.channel_secret_enc:" + appID.String() + ":" + environment
}

// ListApps returns the tenant's apps.
func (s *Service) ListApps(ctx context.Context, p principal.Principal, tenantID uuid.UUID) ([]sqlc.App, error) {
	var apps []sqlc.App
	err := s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		apps, err = q.ListApps(ctx, tenantID)
		return err
	})
	if apps == nil {
		apps = []sqlc.App{}
	}
	return apps, err
}

// CreateApp creates an app within the tenant's plan limit. Owners and admins.
func (s *Service) CreateApp(ctx context.Context, p principal.Principal, tenantID uuid.UUID, idem Idem, in AppCreate) (sqlc.App, error) {
	in.Name = strings.TrimSpace(in.Name)
	if fields := validateName("name", in.Name, 100); fields != nil {
		return sqlc.App{}, apierr.Validation(fields)
	}
	locale := "zh-TW"
	if in.Locale != nil && *in.Locale != "" {
		locale = *in.Locale
	}
	if err := validateLocale(locale); err != nil {
		return sqlc.App{}, err
	}
	explicitSlug := in.Slug != nil && *in.Slug != ""
	if explicitSlug {
		if err := validateSlug(*in.Slug); err != nil {
			return sqlc.App{}, err
		}
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.App, error) {
		q := sqlc.New(tx)
		t, err := q.GetTenant(ctx, tenantID)
		if err != nil {
			return sqlc.App{}, notFound(err, "tenant")
		}
		if limit := planAppLimit(t.Plan); limit > 0 {
			n, err := q.CountApps(ctx, tenantID)
			if err != nil {
				return sqlc.App{}, fmt.Errorf("tenant: count apps: %w", err)
			}
			if n >= int64(limit) {
				return sqlc.App{}, apierr.Conflict("your plan allows " + fmt.Sprint(limit) + " app(s); upgrade to add more").
					WithDetails(map[string]any{"code": "plan_limit", "plan": t.Plan, "limit": limit})
			}
		}

		slug := deriveSlug(in.Name)
		if explicitSlug {
			slug = *in.Slug
		}
		var app sqlc.App
		for attempt := 0; ; attempt++ {
			// A savepoint keeps a slug collision from poisoning the transaction.
			sp, err := tx.Begin(ctx)
			if err != nil {
				return sqlc.App{}, fmt.Errorf("tenant: savepoint: %w", err)
			}
			app, err = sqlc.New(sp).CreateApp(ctx, sqlc.CreateAppParams{ID: newID(), TenantID: tenantID, Name: in.Name, Slug: slug, Locale: locale})
			if err == nil {
				if err := sp.Commit(ctx); err != nil {
					return sqlc.App{}, fmt.Errorf("tenant: release savepoint: %w", err)
				}
				break
			}
			_ = sp.Rollback(ctx)
			if !db.IsUniqueViolation(err, "apps_tenant_id_slug_key") {
				return sqlc.App{}, fmt.Errorf("tenant: create app: %w", err)
			}
			if explicitSlug || attempt >= 3 {
				return sqlc.App{}, apierr.Conflict("that slug is already used in this tenant")
			}
			slug = withSuffix(deriveSlug(in.Name))
		}
		return app, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &app.ID, Actor: p, Action: audit.AppCreate,
			TargetType: "app", TargetID: app.ID.String(),
			Metadata: map[string]any{"name": app.Name, "slug": app.Slug},
		})
	})
}

// GetApp returns an app the caller's tenant owns.
func (s *Service) GetApp(ctx context.Context, p principal.Principal, appID uuid.UUID) (sqlc.App, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return sqlc.App{}, err
	}
	var app sqlc.App
	err = s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		app, err = q.GetApp(ctx, appID)
		return notFound(err, "app")
	})
	return app, err
}

// CurrentApp returns the app an API key principal belongs to.
func (s *Service) CurrentApp(ctx context.Context, p principal.Principal) (sqlc.App, error) {
	if !p.IsAPIKey() {
		return sqlc.App{}, apierr.Unauthorized("send a valid X-API-Key")
	}
	var app sqlc.App
	err := db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) (err error) {
		app, err = sqlc.New(tx).GetApp(ctx, p.AppID)
		return notFound(err, "app")
	})
	return app, err
}

// UpdateApp changes settings and LINE Official Account credentials. Owners
// and admins.
func (s *Service) UpdateApp(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in AppUpdate) (sqlc.App, error) {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if fields := validateName("name", *in.Name, 100); fields != nil {
			return sqlc.App{}, apierr.Validation(fields)
		}
	}
	if in.Locale != nil {
		if err := validateLocale(*in.Locale); err != nil {
			return sqlc.App{}, err
		}
	}
	if in.Status != nil {
		if err := validateStatus(*in.Status); err != nil {
			return sqlc.App{}, err
		}
	}
	if in.OAChannelID != nil && len(*in.OAChannelID) > 32 {
		return sqlc.App{}, apierr.Validation(map[string]string{"oa_channel_id": "is too long"})
	}
	if in.OABasicID != nil && len(*in.OABasicID) > 32 {
		return sqlc.App{}, apierr.Validation(map[string]string{"oa_basic_id": "is too long"})
	}
	if in.OAChannelSecret != nil && len(*in.OAChannelSecret) > 128 {
		return sqlc.App{}, apierr.Validation(map[string]string{"oa_channel_secret": "is too long"})
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return sqlc.App{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.App, error) {
		q := sqlc.New(tx)
		app, err := q.GetApp(ctx, appID)
		if err != nil {
			return sqlc.App{}, notFound(err, "app")
		}
		var changed []string
		if in.Name != nil || in.Locale != nil || in.Status != nil {
			app, err = q.UpdateApp(ctx, sqlc.UpdateAppParams{ID: appID, Name: in.Name, Locale: in.Locale, Status: in.Status})
			if err != nil {
				return sqlc.App{}, fmt.Errorf("tenant: update app: %w", err)
			}
			for name, v := range map[string]*string{"name": in.Name, "locale": in.Locale, "status": in.Status} {
				if v != nil {
					changed = append(changed, name)
				}
			}
		}
		if in.AllowHS256 != nil && *in.AllowHS256 != app.AllowHs256IDTokens {
			app, err = q.SetAppHS256(ctx, sqlc.SetAppHS256Params{ID: appID, AllowHs256IDTokens: *in.AllowHS256})
			if err != nil {
				return sqlc.App{}, fmt.Errorf("tenant: set HS256: %w", err)
			}
			changed = append(changed, "allow_hs256_id_tokens")
		}
		if in.OAChannelID != nil || in.OABasicID != nil || in.OAChannelSecret != nil {
			params := sqlc.UpdateAppOAParams{
				ID:                 appID,
				OaChannelID:        app.OaChannelID,
				OaBasicID:          app.OaBasicID,
				OaChannelSecretEnc: app.OaChannelSecretEnc,
			}
			// The plaintext secret in effect after this update, for verification.
			secret := ""
			if in.OAChannelSecret != nil {
				secret = *in.OAChannelSecret
			} else if len(app.OaChannelSecretEnc) > 0 {
				plain, err := s.keyring.Decrypt(ctx, app.OaChannelSecretEnc, oaSecretAAD(appID))
				if err != nil {
					return sqlc.App{}, fmt.Errorf("tenant: decrypt OA secret: %w", err)
				}
				secret = string(plain)
			}
			if in.OAChannelID != nil {
				params.OaChannelID = nullIfEmpty(strings.TrimSpace(*in.OAChannelID))
				changed = append(changed, "oa_channel_id")
			}
			if in.OABasicID != nil {
				params.OaBasicID = nullIfEmpty(strings.TrimSpace(*in.OABasicID))
				changed = append(changed, "oa_basic_id")
			}
			if in.OAChannelSecret != nil {
				changed = append(changed, "oa_channel_secret")
				if *in.OAChannelSecret == "" {
					params.OaChannelSecretEnc = nil
				} else {
					enc, err := s.keyring.Encrypt(ctx, []byte(*in.OAChannelSecret), oaSecretAAD(appID))
					if err != nil {
						return sqlc.App{}, fmt.Errorf("tenant: encrypt OA secret: %w", err)
					}
					params.OaChannelSecretEnc = enc
				}
			}
			// Proof of control: LINE must accept the ID and secret pair before
			// it is trusted. Verification also fills the basic ID when the
			// caller did not supply one.
			credentialsChanged := in.OAChannelID != nil || in.OAChannelSecret != nil
			verified := false
			if credentialsChanged && params.OaChannelID != nil && secret != "" && s.verifier != nil {
				info, err := s.verifier.VerifyMessagingChannel(ctx, *params.OaChannelID, secret)
				if err != nil {
					return sqlc.App{}, err
				}
				if in.OABasicID == nil && info.BasicID != "" && len(info.BasicID) <= 32 && strings.HasPrefix(info.BasicID, "@") {
					basic := info.BasicID
					params.OaBasicID = &basic
				}
				verified = true
			}
			app, err = q.UpdateAppOA(ctx, params)
			if err != nil {
				if db.IsUniqueViolation(err, "apps_oa_channel_idx") {
					return sqlc.App{}, apierr.Conflict("that LINE Official Account is already linked to another app")
				}
				return sqlc.App{}, fmt.Errorf("tenant: update OA: %w", err)
			}
			if credentialsChanged {
				if verified {
					err = q.SetAppOAVerified(ctx, appID)
				} else {
					err = q.ClearAppOAVerified(ctx, appID)
				}
				if err != nil {
					return sqlc.App{}, fmt.Errorf("tenant: OA verification stamp: %w", err)
				}
				if app, err = q.GetApp(ctx, appID); err != nil {
					return sqlc.App{}, fmt.Errorf("tenant: reload app: %w", err)
				}
			}
		}
		if len(changed) == 0 {
			return app, nil
		}
		meta := map[string]any{"fields": changed}
		if in.AllowHS256 != nil {
			meta["allow_hs256_id_tokens"] = app.AllowHs256IDTokens
		}
		return app, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.AppUpdate,
			TargetType: "app", TargetID: appID.String(),
			Metadata: meta,
		})
	})
}

// OAChannelSecret decrypts the stored LINE Official Account channel secret.
// ok is false when none is set.
func (s *Service) OAChannelSecret(ctx context.Context, app sqlc.App) (secret string, ok bool, err error) {
	if len(app.OaChannelSecretEnc) == 0 {
		return "", false, nil
	}
	plain, err := s.keyring.Decrypt(ctx, app.OaChannelSecretEnc, oaSecretAAD(app.ID))
	if err != nil {
		return "", false, fmt.Errorf("tenant: decrypt OA secret: %w", err)
	}
	return string(plain), true, nil
}

// ListChannels returns the app's LINE MINI App channels.
func (s *Service) ListChannels(ctx context.Context, p principal.Principal, appID uuid.UUID) ([]sqlc.AppChannel, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return nil, err
	}
	var channels []sqlc.AppChannel
	err = s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		channels, err = q.ListAppChannels(ctx, appID)
		return err
	})
	if channels == nil {
		channels = []sqlc.AppChannel{}
	}
	return channels, err
}

// PutChannel creates or replaces the channel for an environment. Owners and
// admins.
func (s *Service) PutChannel(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, environment string, in ChannelPut) (sqlc.AppChannel, error) {
	if err := validateEnvironment(environment); err != nil {
		return sqlc.AppChannel{}, err
	}
	in.ChannelID = strings.TrimSpace(in.ChannelID)
	in.LiffID = strings.TrimSpace(in.LiffID)
	fields := map[string]string{}
	if in.ChannelID == "" || len(in.ChannelID) > 32 {
		fields["channel_id"] = "is required (at most 32 characters)"
	}
	if in.LiffID == "" || len(in.LiffID) > 64 {
		fields["liff_id"] = "is required (at most 64 characters)"
	}
	if in.Secret == nil || strings.TrimSpace(*in.Secret) == "" {
		fields["channel_secret"] = "is required"
	} else if len(*in.Secret) > 128 {
		fields["channel_secret"] = "is too long"
	}
	if len(fields) > 0 {
		return sqlc.AppChannel{}, apierr.Validation(fields)
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return sqlc.AppChannel{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.AppChannel, error) {
		// Proof of control: LINE must mint a token for this ID and secret.
		var verified pgtype.Timestamptz
		if s.verifier != nil {
			if err := s.verifier.VerifyMiniAppChannel(ctx, in.ChannelID, *in.Secret); err != nil {
				return sqlc.AppChannel{}, err
			}
			verified = pgtype.Timestamptz{Time: s.clock.Now(), Valid: true}
		}
		enc, err := s.keyring.Encrypt(ctx, []byte(*in.Secret), channelSecretAAD(appID, environment))
		if err != nil {
			return sqlc.AppChannel{}, fmt.Errorf("tenant: encrypt channel secret: %w", err)
		}
		ch, err := sqlc.New(tx).UpsertAppChannel(ctx, sqlc.UpsertAppChannelParams{
			ID: newID(), TenantID: tenantID, AppID: appID, Environment: environment,
			ChannelID: in.ChannelID, LiffID: in.LiffID, ChannelSecretEnc: enc, VerifiedAt: verified,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "app_channels_liff_idx") {
				return sqlc.AppChannel{}, apierr.Conflict("that LIFF ID is already registered")
			}
			if db.IsUniqueViolation(err, "app_channels_channel_env_idx") {
				return sqlc.AppChannel{}, apierr.Conflict("that LINE channel is already registered to another app")
			}
			return sqlc.AppChannel{}, fmt.Errorf("tenant: upsert channel: %w", err)
		}
		return ch, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.AppChannelPut,
			TargetType: "app_channel", TargetID: ch.ID.String(),
			Metadata: map[string]any{"environment": environment, "channel_id": in.ChannelID, "liff_id": in.LiffID, "verified": verified.Valid},
		})
	})
}

// DeleteChannel removes the channel for an environment.
func (s *Service) DeleteChannel(ctx context.Context, p principal.Principal, appID uuid.UUID, environment string) error {
	if err := validateEnvironment(environment); err != nil {
		return apierr.NotFound("channel")
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin); err != nil {
			return err
		}
		n, err := sqlc.New(tx).DeleteAppChannel(ctx, sqlc.DeleteAppChannelParams{AppID: appID, Environment: environment})
		if err != nil {
			return fmt.Errorf("tenant: delete channel: %w", err)
		}
		if n == 0 {
			return apierr.NotFound("channel")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.AppChannelDelete,
			TargetType: "app_channel", TargetID: environment,
			Metadata: map[string]any{"environment": environment},
		})
	})
}

// ChannelSecret decrypts a stored MINI App channel secret. ok is false when
// none is set.
func (s *Service) ChannelSecret(ctx context.Context, ch sqlc.AppChannel) (secret string, ok bool, err error) {
	if len(ch.ChannelSecretEnc) == 0 {
		return "", false, nil
	}
	plain, err := s.keyring.Decrypt(ctx, ch.ChannelSecretEnc, channelSecretAAD(ch.AppID, ch.Environment))
	if err != nil {
		return "", false, fmt.Errorf("tenant: decrypt channel secret: %w", err)
	}
	return string(plain), true, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EndMemberSessions signs every member of the app out: member tokens issued
// before now are refused. Owners and admins; audited.
func (s *Service) EndMemberSessions(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem) (sqlc.App, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return sqlc.App{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.App, error) {
		q := sqlc.New(tx)
		// Millisecond precision matches the iat_ms member tokens carry, so a
		// token issued in this very instant is not refused by rounding.
		now := s.clock.Now().Truncate(time.Millisecond)
		app, err := q.EndMemberSessions(ctx, sqlc.EndMemberSessionsParams{ID: appID, MemberSessionsValidFrom: pgtype.Timestamptz{Time: now, Valid: true}})
		if err != nil {
			return sqlc.App{}, notFound(err, "app")
		}
		return app, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.MemberSessionsEnd,
			TargetType: "app", TargetID: appID.String(),
		})
	})
}
