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

// ListMemberships returns the tenants the user belongs to with their role.
func (s *Service) ListMemberships(ctx context.Context, p principal.Principal) ([]sqlc.ListMembershipsForUserRow, error) {
	if !p.IsUser() {
		return nil, apierr.Unauthorized("")
	}
	var rows []sqlc.ListMembershipsForUserRow
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		rows, err = sqlc.New(tx).ListMembershipsForUser(ctx, p.UserID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("tenant: list memberships: %w", err)
	}
	if rows == nil {
		rows = []sqlc.ListMembershipsForUserRow{}
	}
	return rows, nil
}

// GetTenant returns a tenant the caller belongs to.
func (s *Service) GetTenant(ctx context.Context, p principal.Principal, tenantID uuid.UUID) (sqlc.Tenant, error) {
	var t sqlc.Tenant
	err := s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		t, err = q.GetTenant(ctx, tenantID)
		return notFound(err, "tenant")
	})
	return t, err
}

// UpdateTenantName renames the tenant. Owners and admins.
func (s *Service) UpdateTenantName(ctx context.Context, p principal.Principal, tenantID uuid.UUID, idem Idem, name string) (sqlc.Tenant, error) {
	name = strings.TrimSpace(name)
	if fields := validateName("name", name, 100); fields != nil {
		return sqlc.Tenant{}, apierr.Validation(fields)
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.Tenant, error) {
		t, err := sqlc.New(tx).UpdateTenantName(ctx, sqlc.UpdateTenantNameParams{ID: tenantID, Name: name})
		if err != nil {
			return sqlc.Tenant{}, notFound(err, "tenant")
		}
		return t, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, Actor: p, Action: audit.TenantUpdate,
			TargetType: "tenant", TargetID: tenantID.String(),
			Metadata: map[string]any{"name": name},
		})
	})
}

// ListTeam returns the tenant's members.
func (s *Service) ListTeam(ctx context.Context, p principal.Principal, tenantID uuid.UUID) ([]sqlc.ListTeamRow, error) {
	var rows []sqlc.ListTeamRow
	err := s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) (err error) {
		rows, err = q.ListTeam(ctx, tenantID)
		return err
	})
	if rows == nil {
		rows = []sqlc.ListTeamRow{}
	}
	return rows, err
}

// UpdateTeamMember changes a member's role. Owners only; the last owner
// cannot be demoted.
func (s *Service) UpdateTeamMember(ctx context.Context, p principal.Principal, tenantID, userID uuid.UUID, idem Idem, role principal.Role) (sqlc.GetTeamMemberRow, error) {
	if !role.Valid() {
		return sqlc.GetTeamMemberRow{}, apierr.Validation(map[string]string{"role": "must be owner, admin or staff"})
	}
	return write(ctx, s, p, tenantID, principal.RoleOwner, idem, func(ctx context.Context, tx pgx.Tx, _ principal.Role) (sqlc.GetTeamMemberRow, error) {
		q := sqlc.New(tx)
		current, err := q.GetMembership(ctx, sqlc.GetMembershipParams{TenantID: tenantID, UserID: userID})
		if err != nil {
			return sqlc.GetTeamMemberRow{}, notFound(err, "team member")
		}
		if principal.Role(current.Role) == principal.RoleOwner && role != principal.RoleOwner {
			if err := s.ensureAnotherOwner(ctx, q, tenantID); err != nil {
				return sqlc.GetTeamMemberRow{}, err
			}
		}
		if _, err := q.UpdateMembershipRole(ctx, sqlc.UpdateMembershipRoleParams{TenantID: tenantID, UserID: userID, Role: string(role)}); err != nil {
			return sqlc.GetTeamMemberRow{}, fmt.Errorf("tenant: update role: %w", err)
		}
		row, err := q.GetTeamMember(ctx, sqlc.GetTeamMemberParams{TenantID: tenantID, UserID: userID})
		if err != nil {
			return sqlc.GetTeamMemberRow{}, fmt.Errorf("tenant: read member: %w", err)
		}
		return row, audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, Actor: p, Action: audit.TeamUpdate,
			TargetType: "user", TargetID: userID.String(),
			Metadata: map[string]any{"role": string(role), "previous_role": current.Role},
		})
	})
}

// RemoveTeamMember removes a member. Owners and admins; admins cannot remove
// owners; the last owner cannot be removed.
func (s *Service) RemoveTeamMember(ctx context.Context, p principal.Principal, tenantID, userID uuid.UUID) error {
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		actor, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin)
		if err != nil {
			return err
		}
		q := sqlc.New(tx)
		current, err := q.GetMembership(ctx, sqlc.GetMembershipParams{TenantID: tenantID, UserID: userID})
		if err != nil {
			return notFound(err, "team member")
		}
		if principal.Role(current.Role) == principal.RoleOwner {
			if actor != principal.RoleOwner {
				return apierr.Forbidden("admins cannot remove owners")
			}
			if err := s.ensureAnotherOwner(ctx, q, tenantID); err != nil {
				return err
			}
		}
		if n, err := q.DeleteMembership(ctx, sqlc.DeleteMembershipParams{TenantID: tenantID, UserID: userID}); err != nil {
			return fmt.Errorf("tenant: remove member: %w", err)
		} else if n == 0 {
			return apierr.NotFound("team member")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, Actor: p, Action: audit.TeamRemove,
			TargetType: "user", TargetID: userID.String(),
			Metadata: map[string]any{"previous_role": current.Role},
		})
	})
}

func (s *Service) ensureAnotherOwner(ctx context.Context, q *sqlc.Queries, tenantID uuid.UUID) error {
	n, err := q.CountOwners(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("tenant: count owners: %w", err)
	}
	if n <= 1 {
		return apierr.Conflict("a tenant needs at least one owner")
	}
	return nil
}
