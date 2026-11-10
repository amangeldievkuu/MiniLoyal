// Package principal describes who is calling: a dashboard user, a server API
// key, and later a member or a staff member. Authentication middleware puts
// a Principal in the request context; handlers and services read it.
package principal

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Kind is the type of caller.
type Kind string

// Caller kinds.
const (
	KindUser   Kind = "user"    // dashboard user (JWT)
	KindAPIKey Kind = "api_key" // server key (X-API-Key)
	KindMember Kind = "member"  // MINI App member (Phase 2)
	KindStaff  Kind = "staff"   // staff app user (Phase 3)
)

// Role is a dashboard user's role in one tenant.
type Role string

// Dashboard roles, weakest to strongest.
const (
	RoleStaff Role = "staff"
	RoleAdmin Role = "admin"
	RoleOwner Role = "owner"
)

var roleRank = map[Role]int{RoleStaff: 1, RoleAdmin: 2, RoleOwner: 3}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return roleRank[r] > 0 }

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return roleRank[r] >= roleRank[min] && roleRank[min] > 0 }

// Principal is an authenticated caller.
type Principal struct {
	Kind Kind

	// UserID is set for KindUser.
	UserID uuid.UUID
	// IssuedAt is when the credential was issued (sessions_valid_from check).
	IssuedAt time.Time

	// APIKeyID, TenantID, AppID and Scopes are set for KindAPIKey. TenantID
	// and AppID are also set for members and staff.
	APIKeyID uuid.UUID
	TenantID uuid.UUID
	AppID    uuid.UUID
	Scopes   []string

	// MemberID is set for KindMember (a MINI App user signed in through LINE).
	MemberID uuid.UUID

	// StaffID, StaffRole and StoreID are set for KindStaff (a cashier or
	// manager signed in to the staff app). StoreID is uuid.Nil when the staff
	// member is not assigned to a store.
	StaffID   uuid.UUID
	StaffRole string
	StoreID   uuid.UUID
}

// HasScope reports whether an API key principal carries scope.
func (p Principal) HasScope(scope string) bool {
	return slices.Contains(p.Scopes, scope)
}

// IsUser reports whether the caller is a dashboard user.
func (p Principal) IsUser() bool { return p.Kind == KindUser && p.UserID != uuid.Nil }

// IsAPIKey reports whether the caller is a server API key.
func (p Principal) IsAPIKey() bool { return p.Kind == KindAPIKey && p.APIKeyID != uuid.Nil }

// IsStaff reports whether the caller is a staff member of an app.
func (p Principal) IsStaff() bool {
	return p.Kind == KindStaff && p.StaffID != uuid.Nil && p.AppID != uuid.Nil && p.TenantID != uuid.Nil
}

// IsManager reports whether a staff caller has the manager role.
func (p Principal) IsManager() bool { return p.IsStaff() && p.StaffRole == "manager" }

// IsMember reports whether the caller is a MINI App member.
func (p Principal) IsMember() bool {
	return p.Kind == KindMember && p.MemberID != uuid.Nil && p.AppID != uuid.Nil && p.TenantID != uuid.Nil
}

type ctxKey struct{}

// WithContext stores p in ctx.
func WithContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the caller stored by the authentication middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
