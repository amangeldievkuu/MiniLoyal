package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// StoreCreate is the input for CreateStore.
type StoreCreate struct {
	Name    string
	Code    *string
	Address *string
}

// StoreUpdate is the input for UpdateStore. Nil means unchanged; an empty
// Code or Address clears the value.
type StoreUpdate struct {
	Name    *string
	Code    *string
	Address *string
	Status  *string
}

// ListStores returns the app's stores.
func (s *Service) ListStores(ctx context.Context, p principal.Principal, appID uuid.UUID) ([]sqlc.Store, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return nil, err
	}
	var stores []sqlc.Store
	err = s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		stores, err = q.ListStores(ctx, appID)
		return err
	})
	if stores == nil {
		stores = []sqlc.Store{}
	}
	return stores, err
}

// CreateStore adds a store to the app. Owners and admins.
func (s *Service) CreateStore(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in StoreCreate) (sqlc.Store, error) {
	in.Name = strings.TrimSpace(in.Name)
	if fields := validateName("name", in.Name, 100); fields != nil {
		return sqlc.Store{}, apierr.Validation(fields)
	}
	code := cleanOptional(in.Code, 32)
	address := cleanOptional(in.Address, 300)
	if (in.Code != nil && code == nil && *in.Code != "") || (in.Address != nil && address == nil && *in.Address != "") {
		return sqlc.Store{}, apierr.Validation(map[string]string{"code": "at most 32 characters", "address": "at most 300 characters"})
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return sqlc.Store{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.Store, error) {
		st, err := sqlc.New(tx).CreateStore(ctx, sqlc.CreateStoreParams{ID: newID(), TenantID: tenantID, AppID: appID, Name: in.Name, Code: code, Address: address})
		if err != nil {
			if db.IsUniqueViolation(err, "stores_code_idx") {
				return sqlc.Store{}, apierr.Conflict("that store code is already used in this app")
			}
			return sqlc.Store{}, fmt.Errorf("tenant: create store: %w", err)
		}
		return st, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.StoreCreate,
			TargetType: "store", TargetID: st.ID.String(),
			Metadata: map[string]any{"name": st.Name, "code": derefOr(st.Code, "")},
		})
	})
}

// UpdateStore changes a store. Owners and admins.
func (s *Service) UpdateStore(ctx context.Context, p principal.Principal, storeID uuid.UUID, idem Idem, in StoreUpdate) (sqlc.Store, error) {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if fields := validateName("name", *in.Name, 100); fields != nil {
			return sqlc.Store{}, apierr.Validation(fields)
		}
	}
	if in.Status != nil {
		if err := validateStatus(*in.Status); err != nil {
			return sqlc.Store{}, err
		}
	}
	if in.Code != nil && len(*in.Code) > 32 {
		return sqlc.Store{}, apierr.Validation(map[string]string{"code": "at most 32 characters"})
	}
	if in.Address != nil && len(*in.Address) > 300 {
		return sqlc.Store{}, apierr.Validation(map[string]string{"address": "at most 300 characters"})
	}
	tenantID, err := s.storeTenant(ctx, storeID)
	if err != nil {
		return sqlc.Store{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.Store, error) {
		params := sqlc.UpdateStoreParams{ID: storeID, Name: in.Name, Status: in.Status}
		if in.Code != nil {
			params.SetCode = true
			params.Code = nullIfEmpty(strings.TrimSpace(*in.Code))
		}
		if in.Address != nil {
			params.SetAddress = true
			params.Address = nullIfEmpty(strings.TrimSpace(*in.Address))
		}
		st, err := sqlc.New(tx).UpdateStore(ctx, params)
		if err != nil {
			if db.IsUniqueViolation(err, "stores_code_idx") {
				return sqlc.Store{}, apierr.Conflict("that store code is already used in this app")
			}
			return sqlc.Store{}, notFound(err, "store")
		}
		return st, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &st.AppID, Actor: p, Action: audit.StoreUpdate,
			TargetType: "store", TargetID: st.ID.String(),
			Metadata: map[string]any{"fields": changedFields(map[string]bool{"name": in.Name != nil, "code": in.Code != nil, "address": in.Address != nil, "status": in.Status != nil})},
		})
	})
}

// DeleteStore removes a store that has no staff assigned. Owners and admins.
func (s *Service) DeleteStore(ctx context.Context, p principal.Principal, storeID uuid.UUID) error {
	tenantID, err := s.storeTenant(ctx, storeID)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin); err != nil {
			return err
		}
		q := sqlc.New(tx)
		st, err := q.GetStore(ctx, storeID)
		if err != nil {
			return notFound(err, "store")
		}
		n, err := q.CountStaffInStore(ctx, uuid.NullUUID{UUID: storeID, Valid: true})
		if err != nil {
			return fmt.Errorf("tenant: count staff: %w", err)
		}
		if n > 0 {
			return apierr.Conflict("reassign the store's staff before deleting it")
		}
		if rows, err := q.DeleteStore(ctx, storeID); err != nil {
			return fmt.Errorf("tenant: delete store: %w", err)
		} else if rows == 0 {
			return apierr.NotFound("store")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &st.AppID, Actor: p, Action: audit.StoreDelete,
			TargetType: "store", TargetID: storeID.String(),
			Metadata: map[string]any{"name": st.Name},
		})
	})
}

// cleanOptional trims an optional string; empty becomes nil and anything
// over max becomes nil too so the caller can report it.
func cleanOptional(v *string, max int) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" || len(t) > max {
		return nil
	}
	return &t
}

func derefOr(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}

func changedFields(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for _, name := range []string{"name", "code", "address", "status", "display_name", "store_id", "role"} {
		if set[name] {
			out = append(out, name)
		}
	}
	return out
}
