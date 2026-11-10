// Package audit records who did what, inside the caller's tenant transaction,
// so an action and its audit entry commit or roll back together.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Actions recorded by the platform services.
const (
	TenantUpdate     = "tenant.update"
	TeamAdd          = "team.add"
	TeamUpdate       = "team.update"
	TeamRemove       = "team.remove"
	TeamInvite       = "team.invite"
	TeamInviteRevoke = "team.invite_revoke"
	TeamJoin         = "team.join"
	AppCreate        = "app.create"
	AppUpdate        = "app.update"
	AppChannelPut    = "app.channel.put"
	AppChannelDelete = "app.channel.delete"
	StoreCreate      = "store.create"
	StoreUpdate      = "store.update"
	StoreDelete      = "store.delete"
	StaffCreate      = "staff.create"
	StaffUpdate      = "staff.update"
	StaffDelete      = "staff.delete"
	APIKeyCreate     = "api_key.create"
	APIKeyRevoke     = "api_key.revoke"

	// Phase 3: member cards, member sessions and the staff app.
	MemberCardRotate  = "member.card_rotate"
	MemberSessionsEnd = "app.member_sessions_end"
	StaffInviteReset  = "staff.invite_reset"
	StaffLoginBound   = "staff.login_bound"
)

// Entry is one audit record. Metadata must never hold secrets.
type Entry struct {
	TenantID   uuid.UUID
	AppID      *uuid.UUID
	Actor      principal.Principal
	Action     string
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

// RequestInfo is what the HTTP layer knows and the services do not: the
// request ID and the client address. Handlers attach it with WithRequestInfo.
type RequestInfo struct {
	RequestID string
	IP        string
}

type ctxKey struct{}

// WithRequestInfo stores info in ctx for Record to pick up.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

func requestInfo(ctx context.Context) RequestInfo {
	info, _ := ctx.Value(ctxKey{}).(RequestInfo)
	return info
}

// Record inserts e using tx, which must already be scoped to e.TenantID.
func Record(ctx context.Context, tx pgx.Tx, e Entry) error {
	if e.Action == "" || e.TargetType == "" {
		return fmt.Errorf("audit: action and target type are required")
	}
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("audit: metadata: %w", err)
	}
	info := requestInfo(ctx)
	params := sqlc.InsertAuditEntryParams{
		TenantID:   e.TenantID,
		ActorType:  actorType(e.Actor),
		Action:     e.Action,
		TargetType: e.TargetType,
		Metadata:   metaJSON,
	}
	if e.AppID != nil {
		params.AppID = uuid.NullUUID{UUID: *e.AppID, Valid: true}
	}
	if id := actorID(e.Actor); id != uuid.Nil {
		params.ActorID = uuid.NullUUID{UUID: id, Valid: true}
	}
	if e.TargetID != "" {
		params.TargetID = &e.TargetID
	}
	if info.RequestID != "" {
		params.RequestID = &info.RequestID
	}
	if addr, err := netip.ParseAddr(info.IP); err == nil {
		params.Ip = &addr
	}
	if _, err := sqlc.New(tx).InsertAuditEntry(ctx, params); err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

func actorType(p principal.Principal) string {
	switch p.Kind {
	case principal.KindUser:
		return "user"
	case principal.KindAPIKey:
		return "api_key"
	case principal.KindStaff:
		return "staff"
	default:
		return "system"
	}
}

func actorID(p principal.Principal) uuid.UUID {
	switch p.Kind {
	case principal.KindUser:
		return p.UserID
	case principal.KindAPIKey:
		return p.APIKeyID
	default:
		return uuid.Nil
	}
}
