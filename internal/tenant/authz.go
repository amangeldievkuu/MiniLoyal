package tenant

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Authorize checks, inside a transaction already scoped to tenantID, that p
// is a member of the tenant with at least role min. Non-members get NotFound
// so they learn nothing about the tenant; members below min get Forbidden.
// Other domains (members, points, coupons) use it instead of reimplementing
// the membership rules.
func (s *Service) Authorize(ctx context.Context, tx pgx.Tx, p principal.Principal, tenantID uuid.UUID, min principal.Role) (principal.Role, error) {
	return s.authorize(ctx, tx, p, tenantID, min)
}

// AppTenant resolves an app ID to its tenant with a single-column lookup
// that bypasses tenant policies. Callers then check membership.
func (s *Service) AppTenant(ctx context.Context, appID uuid.UUID) (uuid.UUID, error) {
	return s.appTenant(ctx, appID)
}
