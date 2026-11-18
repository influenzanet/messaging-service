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

type recordingRemover struct {
	calls []*umAPI.RemoveWhatsAppChannelReq
}

func (r *recordingRemover) RemoveWhatsAppChannel(_ context.Context, in *umAPI.RemoveWhatsAppChannelReq, _ ...grpc.CallOption) (*umAPI.RemoveWhatsAppChannelResp, error) {
	r.calls = append(r.calls, in)
	return &umAPI.RemoveWhatsAppChannelResp{ChannelRemoved: true}, nil
}

func stopFrom(phoneNumberID string) []byte {
	return []byte(`{"object":"whatsapp_business_account","entry":[{"changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"` +
		phoneNumberID + `"},"messages":[{"from":"393331234567","type":"text","text":{"body":"STOP"}}]}}]}]}`)
}

// The server processes the webhook for the business number and instance its environment names,
// and for nothing else; without them it has no processing at all.
// Fails if the configured number is not mapped to the configured instance, if another number is
// accepted, or if a processing is built from an incomplete configuration.
func TestWebhookProcessorFollowsTheEnvironment(t *testing.T) {
	env := map[string]string{"WHATSAPP_PHONE_NUMBER_ID": "555", "WHATSAPP_WEBHOOK_INSTANCE_ID": "italy"}
	remover := &recordingRemover{}
	processor := webhookProcessor(func(k string) string { return env[k] }, nil, remover)
	if processor == nil {
		t.Fatal("no processing although both variables are set")
	}
	for _, id := range []string{"555", "556"} {
		if err := processor.Process(context.Background(), stopFrom(id)); err != nil {
			t.Fatal(err)
		}
	}
	if len(remover.calls) != 1 || remover.calls[0].InstanceId != "italy" {
		t.Fatalf("calls %v, want one, for italy (number 555 only)", remover.calls)
	}

	delete(env, "WHATSAPP_WEBHOOK_INSTANCE_ID")
	if p := webhookProcessor(func(k string) string { return env[k] }, nil, remover); p != nil {
		t.Errorf("a processing was built without an instance: %v", p)
	}
}
