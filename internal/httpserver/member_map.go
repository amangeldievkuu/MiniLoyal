package httpserver

import (
	"encoding/json"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/member"
)

// Mapping from member rows to API documents. The LINE user ID only appears
// in MemberDetail, which the member themselves never receive.

func toMember(m sqlc.Member) api.Member {
	out := api.Member{
		Id:           openapi_types.UUID(m.ID),
		AppId:        openapi_types.UUID(m.AppID),
		DisplayName:  m.DisplayName,
		PictureUrl:   m.PictureUrl,
		Language:     m.Language,
		IsFriend:     m.IsFriend,
		Phone:        m.Phone,
		CustomFields: customFields(m.CustomFields),
		CreatedAt:    m.CreatedAt.Time,
	}
	if m.Birthday.Valid {
		out.Birthday = &openapi_types.Date{Time: m.Birthday.Time}
	}
	if m.Gender != nil {
		g := api.Gender(*m.Gender)
		out.Gender = &g
	}
	if m.ConsentVersion != nil && m.ConsentAt.Valid {
		out.Consent = &api.Consent{Version: *m.ConsentVersion, AcceptedAt: m.ConsentAt.Time}
	}
	return out
}

func toMemberDetail(m sqlc.Member) api.MemberDetail {
	base := toMember(m)
	out := api.MemberDetail{
		Id:           base.Id,
		AppId:        base.AppId,
		DisplayName:  base.DisplayName,
		PictureUrl:   base.PictureUrl,
		Language:     base.Language,
		IsFriend:     base.IsFriend,
		Phone:        base.Phone,
		Birthday:     base.Birthday,
		Gender:       base.Gender,
		CustomFields: base.CustomFields,
		Consent:      base.Consent,
		CreatedAt:    base.CreatedAt,
		UpdatedAt:    m.UpdatedAt.Time,
	}
	// Empty when the caller's role sees a redacted member.
	if m.LineUserID != "" {
		id := m.LineUserID
		out.LineUserId = &id
	}
	if m.FriendChangedAt.Valid {
		t := m.FriendChangedAt.Time
		out.FriendChangedAt = &t
	}
	if m.LastLoginAt.Valid {
		t := m.LastLoginAt.Time
		out.LastLoginAt = &t
	}
	return out
}

func toMemberPage(p member.Page) api.MemberPage {
	out := api.MemberPage{Items: make([]api.MemberDetail, 0, len(p.Items))}
	for _, m := range p.Items {
		out.Items = append(out.Items, toMemberDetail(m))
	}
	if p.NextCursor != "" {
		c := p.NextCursor
		out.NextCursor = &c
	}
	return out
}

func toMemberExport(e member.Export) api.MemberExport {
	out := api.MemberExport{
		Member:   toMemberDetail(e.Member),
		Consents: make([]api.Consent, 0, len(e.Consents)),
		Points: api.MemberExportPoints{
			Summary: toPointsSummary(e.Points.Summary),
			History: make([]api.PointsTransaction, 0, len(e.Points.History)),
		},
		// filled by the coupon and stamp module
		Coupons: []api.CouponInstance{},
		// filled by the coupon and stamp module
		Stamps:     api.MemberExportStamps{Progress: []api.StampProgress{}, Events: []api.StampEvent{}},
		ExportedAt: e.ExportedAt,
	}
	for _, c := range e.Consents {
		out.Consents = append(out.Consents, api.Consent{Version: c.Version, AcceptedAt: c.AcceptedAt.Time})
	}
	for _, t := range e.Points.History {
		out.Points.History = append(out.Points.History, toPointsTxn(t))
	}
	return out
}

func toMemberSession(s *lineauth.MemberSession) api.MemberSession {
	return api.MemberSession{
		SessionToken: s.Token,
		TokenType:    api.MemberSessionTokenTypeBearer,
		ExpiresIn:    s.ExpiresIn,
		Member:       toMember(s.Member),
		IsNew:        s.IsNew,
		App: api.AppPublic{
			Id:        openapi_types.UUID(s.App.ID),
			Name:      s.App.Name,
			Locale:    s.App.Locale,
			OaBasicId: s.App.OaBasicID,
		},
	}
}

// customFields decodes the stored JSON object; anything unreadable becomes an
// empty object rather than an error on a read path.
func customFields(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
