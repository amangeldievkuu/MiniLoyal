package member

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
)

// Paging limits.
const (
	DefaultLimit = 50
	MaxLimit     = 100
	MaxQueryLen  = 100
)

// listCursor is the keyset position: the last row's (created_at, id).
type listCursor struct {
	createdAt time.Time
	id        uuid.UUID
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (*listCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, apierr.BadRequest("cursor is not valid")
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, apierr.BadRequest("cursor is not valid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, apierr.BadRequest("cursor is not valid")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, apierr.BadRequest("cursor is not valid")
	}
	return &listCursor{createdAt: createdAt, id: id}, nil
}

func parseListQuery(in ListQuery) (int, *listCursor, error) {
	limit := in.Limit
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		return 0, nil, apierr.BadRequest("limit must be between 1 and 100")
	}
	if len(in.Q) > MaxQueryLen {
		return 0, nil, apierr.BadRequest("q is too long")
	}
	if !cleanText(in.Q) {
		return 0, nil, apierr.BadRequest("q must be text")
	}
	cursor, err := decodeCursor(in.Cursor)
	if err != nil {
		return 0, nil, err
	}
	return limit, cursor, nil
}
