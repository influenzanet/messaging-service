package messaging_service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/whatsappwebhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeWebhook struct {
	err    error
	bodies [][]byte
}

func (f *fakeWebhook) Process(_ context.Context, body []byte) error {
	f.bodies = append(f.bodies, body)
	return f.err
}

// The gateway reads the code to decide whether Meta sends the body again: only a body that can
// never be processed is refused for good.
// Fails if a failure that may pass is reported as InvalidArgument (Meta would not retry and the
// opt-out would be lost), if a malformed body is reported as one that may pass, or if the cause of
// a failure reaches the gateway.
func TestProcessWhatsAppWebhook(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account"}`)
	for _, c := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{"processed", nil, codes.OK},
		{"not a webhook payload", whatsappwebhook.ErrMalformedPayload, codes.InvalidArgument},
		{"part of it failed", fmt.Errorf("server selection error: mongodb://10.0.0.7: %w", errors.New("timeout")), codes.Unavailable},
		{"user-management refused to answer", status.Error(codes.Unavailable, "connection refused 10.0.0.8"), codes.Unavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			webhook := &fakeWebhook{err: c.err}
			s := messagingServer{webhook: webhook}
			_, err := s.ProcessWhatsAppWebhook(context.Background(), &api.ProcessWhatsAppWebhookReq{Payload: body})
			if status.Code(err) != c.want {
				t.Fatalf("code %v, want %v", status.Code(err), c.want)
			}
			if msg := status.Convert(err).Message(); strings.Contains(msg, "10.0.0.") {
				t.Errorf("the cause reached the gateway: %q", msg)
			}
			if len(webhook.bodies) != 1 || !bytes.Equal(webhook.bodies[0], body) {
				t.Errorf("processed %q, want the body as received", webhook.bodies)
			}
		})
	}

	t.Run("no payload", func(t *testing.T) {
		webhook := &fakeWebhook{}
		s := messagingServer{webhook: webhook}
		for _, req := range []*api.ProcessWhatsAppWebhookReq{nil, {}, {Payload: []byte{}}} {
			if _, err := s.ProcessWhatsAppWebhook(context.Background(), req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("%v: code %v, want InvalidArgument", req, status.Code(err))
			}
		}
		if len(webhook.bodies) != 0 {
			t.Error("an empty request was processed")
		}
	})

	t.Run("not configured: refused so that Meta sends it again, with an alert", func(t *testing.T) {
		var buf bytes.Buffer
		original := logger.Error.Writer()
		logger.Error.SetOutput(&buf)
		defer logger.Error.SetOutput(original)
		s := messagingServer{}
		if _, err := s.ProcessWhatsAppWebhook(context.Background(), &api.ProcessWhatsAppWebhookReq{Payload: body}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code %v, want FailedPrecondition", status.Code(err))
		}
		if !strings.Contains(buf.String(), "WA_ALERT webhook_not_configured") {
			t.Errorf("no alert: %q", buf.String())
		}
	})
}
