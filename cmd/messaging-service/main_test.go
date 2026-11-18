package main

import (
	"context"
	"testing"

	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
)

type platformSourceOn struct{}

func (platformSourceOn) GetPlatformConfig(context.Context, *umAPI.PlatformConfigReq, ...grpc.CallOption) (*umAPI.PlatformConfig, error) {
	return &umAPI.PlatformConfig{WhatsappEnabled: true}, nil
}

// The gRPC server only queues WhatsApp messages, so it decides without the Meta credentials:
// with none in its environment it still queues where user-management turns WhatsApp on, and it
// keeps no token even when one is set.
// Fails if the server is built with the settings of a process that sends.
func TestServerWhatsAppSettingsNeedNoMetaCredentials(t *testing.T) {
	t.Setenv("WHATSAPP_ENABLED", "")
	t.Setenv("WHATSAPP_TOKEN", "")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "")
	if !serverWhatsAppSettings(platformSourceOn{}).Enabled("italy") {
		t.Error("expected WhatsApp on for an instance where user-management turns it on, without credentials")
	}

	t.Setenv("WHATSAPP_TOKEN", "server-test-token")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "server-test-phone-id")
	if got := serverWhatsAppSettings(platformSourceOn{}).Token(); got != "" {
		t.Errorf("the server kept the Meta token %q", got)
	}
}
