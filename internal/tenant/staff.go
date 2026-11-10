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

// inviteTTL is how long a staff invite code stays valid.
const inviteTTL = 7 * 24 * time.Hour

// StaffCreate is the input for CreateStaff.
type StaffCreate struct {
	DisplayName string
	StoreID     *uuid.UUID
	Role        *string
}

// StaffUpdate is the input for UpdateStaff. Nil means unchanged.
type StaffUpdate struct {
	DisplayName *string
	StoreID     *uuid.UUID
	Role        *string
	Status      *string
}

// StaffCreated is a new staff member with the one-time invite code.
type StaffCreated struct {
	sqlc.Staff
	InviteCode string `json:"invite_code"`
}

// ListStaff returns the app's staff.
func (s *Service) ListStaff(ctx context.Context, p principal.Principal, appID uuid.UUID) ([]sqlc.Staff, error) {
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return nil, err
	}
	var staff []sqlc.Staff
	err = s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		staff, err = q.ListStaff(ctx, appID)
		return err
	})
	if staff == nil {
		staff = []sqlc.Staff{}
	}
	return staff, err
}

// CreateStaff adds a staff member and returns their invite code once. Owners
// and admins.
func (s *Service) CreateStaff(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in StaffCreate) (StaffCreated, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if fields := validateName("display_name", in.DisplayName, 100); fields != nil {
		return StaffCreated{}, apierr.Validation(fields)
	}
	role := "cashier"
	if in.Role != nil && *in.Role != "" {
		role = *in.Role
	}
	if err := validateStaffRole(role); err != nil {
		return StaffCreated{}, err
	}
	tenantID, err := s.appTenant(ctx, appID)
	if err != nil {
		return StaffCreated{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (StaffCreated, error) {
		q := sqlc.New(tx)
		storeID, err := s.storeInApp(ctx, q, in.StoreID, appID)
		if err != nil {
			return StaffCreated{}, err
		}
		code := randomInviteCode()
		hash := s.hashInvite(code)
		st, err := q.CreateStaff(ctx, sqlc.CreateStaffParams{
			ID: newID(), TenantID: tenantID, AppID: appID, StoreID: storeID,
			DisplayName: in.DisplayName, Role: role,
			InviteCodeHash:  &hash,
			InviteExpiresAt: pgtype.Timestamptz{Time: s.clock.Now().Add(inviteTTL), Valid: true},
		})
		if err != nil {
			return StaffCreated{}, fmt.Errorf("tenant: create staff: %w", err)
		}
		return StaffCreated{Staff: st, InviteCode: code}, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &appID, Actor: p, Action: audit.StaffCreate,
			TargetType: "staff", TargetID: st.ID.String(),
			Metadata: map[string]any{"display_name": st.DisplayName, "role": st.Role, "store_id": nullUUIDString(st.StoreID)},
		})
	})
}

// UpdateStaff changes a staff member. Owners and admins.
func (s *Service) UpdateStaff(ctx context.Context, p principal.Principal, staffID uuid.UUID, idem Idem, in StaffUpdate) (sqlc.Staff, error) {
	if in.DisplayName != nil {
		*in.DisplayName = strings.TrimSpace(*in.DisplayName)
		if fields := validateName("display_name", *in.DisplayName, 100); fields != nil {
			return sqlc.Staff{}, apierr.Validation(fields)
		}
	}
	if in.Role != nil {
		if err := validateStaffRole(*in.Role); err != nil {
			return sqlc.Staff{}, err
		}
	}
	if in.Status != nil {
		if err := validateStatus(*in.Status); err != nil {
			return sqlc.Staff{}, err
		}
	}
	tenantID, err := s.staffTenant(ctx, staffID)
	if err != nil {
		return sqlc.Staff{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.Staff, error) {
		q := sqlc.New(tx)
		current, err := q.GetStaff(ctx, staffID)
		if err != nil {
			return sqlc.Staff{}, notFound(err, "staff member")
		}
		params := sqlc.UpdateStaffParams{ID: staffID, DisplayName: in.DisplayName, Role: in.Role, Status: in.Status}
		if in.StoreID != nil {
			storeID, err := s.storeInApp(ctx, q, in.StoreID, current.AppID)
			if err != nil {
				return sqlc.Staff{}, err
			}
			params.SetStoreID = true
			params.StoreID = storeID
		}
		st, err := q.UpdateStaff(ctx, params)
		if err != nil {
			return sqlc.Staff{}, fmt.Errorf("tenant: update staff: %w", err)
		}
		// Disabling or demoting a staff member ends their sessions, so
		// re-enabling them later cannot revive a token issued before.
		disabled := current.Status == "active" && st.Status != "active"
		demoted := current.Role == "manager" && st.Role != "manager"
		if disabled || demoted {
			now := pgtype.Timestamptz{Time: s.clock.Now().Truncate(time.Millisecond), Valid: true}
			if err := q.EndStaffSessions(ctx, sqlc.EndStaffSessionsParams{ID: st.ID, SessionsValidFrom: now}); err != nil {
				return sqlc.Staff{}, fmt.Errorf("tenant: end staff sessions: %w", err)
			}
			st.SessionsValidFrom = now
		}
		return st, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &st.AppID, Actor: p, Action: audit.StaffUpdate,
			TargetType: "staff", TargetID: st.ID.String(),
			Metadata: map[string]any{"fields": changedFields(map[string]bool{"display_name": in.DisplayName != nil, "store_id": in.StoreID != nil, "role": in.Role != nil, "status": in.Status != nil})},
		})
	})
}

// DeleteStaff removes a staff member. Owners and admins.
func (s *Service) DeleteStaff(ctx context.Context, p principal.Principal, staffID uuid.UUID) error {
	tenantID, err := s.staffTenant(ctx, staffID)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin); err != nil {
			return err
		}
		q := sqlc.New(tx)
		st, err := q.GetStaff(ctx, staffID)
		if err != nil {
			return notFound(err, "staff member")
		}
		if n, err := q.DeleteStaff(ctx, staffID); err != nil {
			return fmt.Errorf("tenant: delete staff: %w", err)
		} else if n == 0 {
			return apierr.NotFound("staff member")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &st.AppID, Actor: p, Action: audit.StaffDelete,
			TargetType: "staff", TargetID: staffID.String(),
			Metadata: map[string]any{"display_name": st.DisplayName},
		})
	})
}

// storeInApp validates an optional store reference belongs to the app.
func (s *Service) storeInApp(ctx context.Context, q *sqlc.Queries, storeID *uuid.UUID, appID uuid.UUID) (uuid.NullUUID, error) {
	if storeID == nil || *storeID == uuid.Nil {
		return uuid.NullUUID{}, nil
	}
	st, err := q.GetStore(ctx, *storeID)
	if err != nil || st.AppID != appID {
		if err == nil || db.IsNotFound(err) {
			return uuid.NullUUID{}, apierr.NotFound("store")
		}
		return uuid.NullUUID{}, fmt.Errorf("tenant: store lookup: %w", err)
	}
	return uuid.NullUUID{UUID: st.ID, Valid: true}, nil
}

func nullUUIDString(v uuid.NullUUID) any {
	if !v.Valid {
		return nil
	}
	return v.UUID.String()
}

// ResetStaffInvite issues a new invite code and unlinks the staff member's
// LINE account (a new phone or a new person). Their sessions end at once.
// Owners and admins; the code is returned once.
func (s *Service) ResetStaffInvite(ctx context.Context, p principal.Principal, staffID uuid.UUID, idem Idem) (StaffCreated, error) {
	tenantID, err := s.staffTenant(ctx, staffID)
	if err != nil {
		return StaffCreated{}, err
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (StaffCreated, error) {
		q := sqlc.New(tx)
		now := s.clock.Now()
		code := randomInviteCode()
		hash := s.hashInvite(code)
		st, err := q.ResetStaffInvite(ctx, sqlc.ResetStaffInviteParams{
			ID:                staffID,
			InviteCodeHash:    &hash,
			InviteExpiresAt:   pgtype.Timestamptz{Time: now.Add(inviteTTL), Valid: true},
			SessionsValidFrom: pgtype.Timestamptz{Time: now.Truncate(time.Millisecond), Valid: true},
		})
		if err != nil {
			return StaffCreated{}, notFound(err, "staff member")
		}
		return StaffCreated{Staff: st, InviteCode: code}, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, AppID: &st.AppID, Actor: p, Action: audit.StaffInviteReset,
			TargetType: "staff", TargetID: st.ID.String(),
		})
	})
}

// NormalizeInviteCode uppercases a code and strips spaces and dashes.
func NormalizeInviteCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(code)))
}
