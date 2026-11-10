package httpserver

import (
	"context"
	"math"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/member"
)

// Handlers for the rotating member card: the member's own QR code, staff
// scanning it, and the dashboard invalidating it.

func (s *Server) GetMyCard(ctx context.Context, _ api.GetMyCardRequestObject) (api.GetMyCardResponseObject, error) {
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.staffService()
	if err != nil {
		return nil, err
	}
	c, err := svc.MemberCard(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.GetMyCard200JSONResponse(api.MemberCard{
		Qr:               c.Payload,
		ExpiresAt:        c.ExpiresAt,
		RefreshInSeconds: int(math.Ceil(c.RefreshIn.Seconds())),
		WindowSeconds:    int(card.Window.Seconds()),
	}), nil
}

func (s *Server) StaffScan(ctx context.Context, req api.StaffScanRequestObject) (api.StaffScanResponseObject, error) {
	p, err := requireStaff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.staffService()
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	res, err := svc.Scan(ctx, p, body.Qr)
	if err != nil {
		return nil, err
	}
	return api.StaffScan200JSONResponse(api.ScanResult{
		ScanToken: res.ScanToken,
		ExpiresAt: res.ExpiresAt,
		Balance:   res.Balance,
		Member: api.ScannedMember{
			Id:          openapi_types.UUID(res.Member.ID),
			DisplayName: res.Member.DisplayName,
			PictureUrl:  res.Member.PictureUrl,
			IsFriend:    res.Member.IsFriend,
		},
	}), nil
}

func (s *Server) RotateMemberCard(ctx context.Context, req api.RotateMemberCardRequestObject) (api.RotateMemberCardResponseObject, error) {
	ctx, svc, p, err := s.memberDashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	out, err := svc.RotateCard(ctx, p, uuid.UUID(req.MemberId), member.Idem(idem(ctx, req.Params.IdempotencyKey, nil)))
	if err != nil {
		return nil, err
	}
	return api.RotateMemberCard200JSONResponse(api.MemberCardRotated{QrKeyVersion: out.QRKeyVersion}), nil
}
