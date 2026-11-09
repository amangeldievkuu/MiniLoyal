package tenant

import (
	"context"
	"encoding/base64"
	"strconv"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// AuditQuery selects a page of audit entries.
type AuditQuery struct {
	AppID  *uuid.UUID
	Limit  int
	Cursor string
}

// AuditPage is one page of entries, newest first.
type AuditPage struct {
	Items      []sqlc.AuditLog
	NextCursor string
}

// ListAuditLog returns a page of the tenant's audit log.
func (s *Service) ListAuditLog(ctx context.Context, p principal.Principal, tenantID uuid.UUID, in AuditQuery) (AuditPage, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var before *int64
	if in.Cursor != "" {
		id, err := decodeCursor(in.Cursor)
		if err != nil {
			return AuditPage{}, apierr.BadRequest("invalid cursor")
		}
		before = &id
	}
	page := AuditPage{Items: []sqlc.AuditLog{}}
	err := s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) error {
		params := sqlc.ListAuditLogParams{TenantID: tenantID, BeforeID: before, PageLimit: int32(limit)}
		if in.AppID != nil {
			params.AppID = uuid.NullUUID{UUID: *in.AppID, Valid: true}
		}
		items, err := q.ListAuditLog(ctx, params)
		if err != nil {
			return err
		}
		if items != nil {
			page.Items = items
		}
		if len(items) == limit {
			page.NextCursor = encodeCursor(items[len(items)-1].ID)
		}
		return nil
	})
	return page, err
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeCursor(c string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(raw), 10, 64)
}
