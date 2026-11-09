package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
)

// webhookMaxBody caps a LINE webhook delivery. LINE batches events, so this
// is larger than the general API limit.
const webhookMaxBody = 1 << 20

// ReceiveLineWebhook verifies and applies a delivery from the app's LINE
// Official Account. The spec declares no request body so the raw bytes stay
// available for the signature check.
func (s *Server) ReceiveLineWebhook(ctx context.Context, req api.ReceiveLineWebhookRequestObject) (api.ReceiveLineWebhookResponseObject, error) {
	if s.webhooks == nil {
		return nil, apierr.Unavailable("webhooks are not configured")
	}
	r := httpRequest(ctx)
	if r == nil || r.Body == nil {
		return nil, apierr.BadRequest("empty body")
	}
	body, err := io.ReadAll(http.MaxBytesReader(httpResponse(ctx), r.Body, webhookMaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, apierr.New(http.StatusRequestEntityTooLarge, "payload_too_large", "webhook body is too large")
		}
		return nil, apierr.BadRequest("could not read the body")
	}
	if _, err := s.webhooks.Receive(ctx, uuid.UUID(req.AppId), req.Params.XLineSignature, body); err != nil {
		return nil, err
	}
	return api.ReceiveLineWebhook200Response{}, nil
}
