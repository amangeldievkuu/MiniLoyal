package stamp

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// ProgressFor lists the member's progress on every active card of the app
// (zero for cards not started), inside the caller's tenant transaction.
// includeArchived adds archived cards the member has progress on.
func ProgressFor(ctx context.Context, q *sqlc.Queries, m sqlc.Member, includeArchived bool) ([]Progress, error) {
	rows, err := q.ListMemberStampProgress(ctx, sqlc.ListMemberStampProgressParams{MemberID: m.ID, AppID: m.AppID, IncludeArchived: includeArchived})
	if err != nil {
		return nil, fmt.Errorf("stamp: progress: %w", err)
	}
	out := make([]Progress, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProgress(r))
	}
	return out, nil
}

// ListMine is the signed-in member's progress on the app's active cards.
func (s *Service) ListMine(ctx context.Context, p principal.Principal) ([]Progress, error) {
	if !p.IsMember() {
		return nil, access.ErrMemberNotFound
	}
	return s.list(ctx, p, p.MemberID, false)
}

// ListMember is a member's progress for the dashboard (any role), archived
// cards the member has progress on included.
func (s *Service) ListMember(ctx context.Context, p principal.Principal, memberID uuid.UUID) ([]Progress, error) {
	if !p.IsUser() {
		return nil, access.ErrMemberNotFound
	}
	return s.list(ctx, p, memberID, true)
}

func (s *Service) list(ctx context.Context, p principal.Principal, memberID uuid.UUID, includeArchived bool) ([]Progress, error) {
	var out []Progress
	err := access.Read(ctx, s.pool, s.tenants, p, memberID, principal.RoleStaff, func(ctx context.Context, tx pgx.Tx, m sqlc.Member) (err error) {
		out, err = ProgressFor(ctx, sqlc.New(tx), m, includeArchived)
		return err
	})
	return out, err
}

// ExportMember reads the member's progress (archived cards included) and
// every stamp event, newest first, inside the caller's tenant transaction.
// The caller has already authorized access to m.
func ExportMember(ctx context.Context, q *sqlc.Queries, m sqlc.Member) (MemberData, error) {
	progress, err := ProgressFor(ctx, q, m, true)
	if err != nil {
		return MemberData{}, err
	}
	out := MemberData{Progress: progress, Events: []Event{}}
	var before *int64
	for {
		rows, err := q.ListStampEvents(ctx, sqlc.ListStampEventsParams{AppID: m.AppID, MemberID: m.ID, BeforeID: before, PageLimit: exportPage})
		if err != nil {
			return MemberData{}, fmt.Errorf("stamp: events: %w", err)
		}
		for _, r := range rows {
			out.Events = append(out.Events, toEvent(r))
		}
		if len(rows) < exportPage {
			return out, nil
		}
		last := rows[len(rows)-1].ID
		before = &last
	}
}
