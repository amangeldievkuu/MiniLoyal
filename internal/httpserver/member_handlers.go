package httpserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/member"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Handlers for member sign-in, the member's own profile and data rights, and
// the member lists read by the dashboard and the server API.

func (s *Server) memberService() (*member.Service, error) {
	if s.memberSvc == nil {
		return nil, apierr.Unavailable("member service not configured")
	}
	return s.memberSvc, nil
}

func (s *Server) memberClient(ctx context.Context) member.Client {
	c := member.Client{UserAgent: userAgent(ctx)}
	if r := httpRequest(ctx); r != nil {
		c.IP = s.clientIP(r)
	}
	return c
}

// memberIdem fingerprints a member write: Idempotency-Key plus method, path
// and body.
func memberIdem(ctx context.Context, key api.IdempotencyKey, body any) member.Idem {
	method, path := "", ""
	if r := httpRequest(ctx); r != nil {
		method, path = r.Method, r.URL.Path
	}
	raw, _ := json.Marshal(body)
	return member.Idem{Key: string(key), Hash: idempotency.Hash(method, path, raw)}
}

// ---- sign-in

func (s *Server) LoginWithLine(ctx context.Context, req api.LoginWithLineRequestObject) (api.LoginWithLineResponseObject, error) {
	if s.lineauth == nil {
		return nil, apierr.Unavailable("LINE sign-in is not configured")
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	in := lineauth.LoginInput{AppID: uuid.UUID(body.AppId), IDToken: body.IdToken, Language: body.Language}
	if body.Nonce != nil {
		in.Nonce = *body.Nonce
	}
	if body.LiffAccessToken != nil {
		in.LiffAccessToken = *body.LiffAccessToken
	}
	c := lineauth.Client{UserAgent: userAgent(ctx)}
	if r := httpRequest(ctx); r != nil {
		c.IP = s.clientIP(r)
	}
	sess, err := s.lineauth.LoginWithLine(ctx, in, c)
	if err != nil {
		return nil, err
	}
	return api.LoginWithLine200JSONResponse(toMemberSession(sess)), nil
}

// ---- the signed-in member

func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	m, err := svc.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.GetMe200JSONResponse(toMember(m)), nil
}

func (s *Server) UpdateMe(ctx context.Context, req api.UpdateMeRequestObject) (api.UpdateMeResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	in := member.Update{
		DisplayName: body.DisplayName,
		Phone:       body.Phone,
		SetPhone:    body.Phone != nil,
		SetBirthday: body.Birthday != nil,
		SetGender:   body.Gender != nil,
	}
	if body.Birthday != nil {
		t := body.Birthday.Time
		in.Birthday = &t
	}
	if body.Gender != nil {
		g := string(*body.Gender)
		in.Gender = &g
	}
	if body.CustomFields != nil {
		in.CustomFields = *body.CustomFields
		if in.CustomFields == nil {
			in.CustomFields = map[string]any{}
		}
	}
	m, err := svc.Update(ctx, p, memberIdem(ctx, req.Params.IdempotencyKey, body), in)
	if err != nil {
		return nil, err
	}
	return api.UpdateMe200JSONResponse(toMember(m)), nil
}

func (s *Server) DeleteMe(ctx context.Context, _ api.DeleteMeRequestObject) (api.DeleteMeResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.EraseSelf(ctx, p); err != nil {
		return nil, err
	}
	return api.DeleteMe204Response{}, nil
}

func (s *Server) RecordConsent(ctx context.Context, req api.RecordConsentRequestObject) (api.RecordConsentResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	m, err := svc.Consent(ctx, p, memberIdem(ctx, req.Params.IdempotencyKey, body), body.Version, s.memberClient(ctx))
	if err != nil {
		return nil, err
	}
	return api.RecordConsent200JSONResponse(toMember(m)), nil
}

func (s *Server) ExportMe(ctx context.Context, _ api.ExportMeRequestObject) (api.ExportMeResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	e, err := svc.ExportSelf(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.ExportMe200JSONResponse(toMemberExport(e)), nil
}

// ---- dashboard

func (s *Server) memberDashboardCall(ctx context.Context) (context.Context, *member.Service, principal.Principal, error) {
	svc, err := s.memberService()
	if err != nil {
		return ctx, nil, principal.Principal{}, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return ctx, nil, principal.Principal{}, err
	}
	return s.auditContext(ctx), svc, p, nil
}

func listQuery(limit *api.Limit, cursor *api.Cursor, q *api.Query) member.ListQuery {
	out := member.ListQuery{}
	if limit != nil {
		out.Limit = int(*limit)
	}
	if cursor != nil {
		out.Cursor = string(*cursor)
	}
	if q != nil {
		out.Q = string(*q)
	}
	return out
}

func (s *Server) ListMembers(ctx context.Context, req api.ListMembersRequestObject) (api.ListMembersResponseObject, error) {
	ctx, svc, p, err := s.memberDashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	page, err := svc.List(ctx, p, uuid.UUID(req.AppId), listQuery(req.Params.Limit, req.Params.Cursor, req.Params.Q))
	if err != nil {
		return nil, err
	}
	return api.ListMembers200JSONResponse(toMemberPage(page)), nil
}

func (s *Server) GetMember(ctx context.Context, req api.GetMemberRequestObject) (api.GetMemberResponseObject, error) {
	ctx, svc, p, err := s.memberDashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	m, err := svc.Get(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.GetMember200JSONResponse(toMemberDetail(m)), nil
}

func (s *Server) EraseMember(ctx context.Context, req api.EraseMemberRequestObject) (api.EraseMemberResponseObject, error) {
	ctx, svc, p, err := s.memberDashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.Erase(ctx, p, uuid.UUID(req.MemberId)); err != nil {
		return nil, err
	}
	return api.EraseMember204Response{}, nil
}

func (s *Server) ExportMember(ctx context.Context, req api.ExportMemberRequestObject) (api.ExportMemberResponseObject, error) {
	ctx, svc, p, err := s.memberDashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	e, err := svc.Export(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.ExportMember200JSONResponse(toMemberExport(e)), nil
}

// ---- server API

func (s *Server) ServerListMembers(ctx context.Context, req api.ServerListMembersRequestObject) (api.ServerListMembersResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "members:read")
	if err != nil {
		return nil, err
	}
	page, err := svc.List(ctx, p, p.AppID, listQuery(req.Params.Limit, req.Params.Cursor, req.Params.Q))
	if err != nil {
		return nil, err
	}
	return api.ServerListMembers200JSONResponse(toMemberPage(page)), nil
}

func (s *Server) ServerGetMember(ctx context.Context, req api.ServerGetMemberRequestObject) (api.ServerGetMemberResponseObject, error) {
	svc, err := s.memberService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "members:read")
	if err != nil {
		return nil, err
	}
	m, err := svc.Get(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.ServerGetMember200JSONResponse(toMemberDetail(m)), nil
}
