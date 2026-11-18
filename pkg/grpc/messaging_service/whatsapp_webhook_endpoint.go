package messaging_service

import (
	"context"
	"errors"

	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/whatsappwebhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// WebhookProcessor processes the body of a WhatsApp webhook request (see whatsappwebhook).
type WebhookProcessor interface {
	Process(ctx context.Context, body []byte) error
}

// ProcessWhatsAppWebhook is called by the gateway with the body of a webhook request whose
// signature it has checked. The answer tells the gateway whether Meta should send the body again:
// InvalidArgument for a body that is not a webhook payload (it never will be), FailedPrecondition
// when this service has no webhook configuration, Unavailable when part of it could not be
// processed yet.
func (s *messagingServer) ProcessWhatsAppWebhook(ctx context.Context, req *api.ProcessWhatsAppWebhookReq) (*emptypb.Empty, error) {
	if req == nil || len(req.Payload) == 0 {
		return nil, status.Error(codes.InvalidArgument, "missing payload")
	}
	if s.webhook == nil {
		logger.Error.Printf("WA_ALERT webhook_not_configured: a WhatsApp webhook notification arrived, and WHATSAPP_PHONE_NUMBER_ID or WHATSAPP_WEBHOOK_INSTANCE_ID is not set; Meta will send it again")
		return nil, status.Error(codes.FailedPrecondition, "whatsapp webhook not configured")
	}
	err := s.webhook.Process(ctx, req.Payload)
	switch {
	case err == nil:
		return &emptypb.Empty{}, nil
	case errors.Is(err, whatsappwebhook.ErrMalformedPayload):
		return nil, status.Error(codes.InvalidArgument, "not a webhook payload")
	}
	// The cause is logged by the processing; it is not passed on to the gateway.
	return nil, status.Error(codes.Unavailable, "the notification could not be processed completely")
}
