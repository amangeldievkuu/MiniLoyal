package httpserver

// Phase 2 stubs: inbound LINE webhooks (line-integration fork).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) ReceiveLineWebhook(context.Context, api.ReceiveLineWebhookRequestObject) (api.ReceiveLineWebhookResponseObject, error) {
	return nil, errNotImplemented
}
