package clients

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestAuthenticationTemplatePayload pins the send payload of a WhatsApp AUTHENTICATION
// template: copy-code, one-tap and zero-tap OTP templates (per Meta's authentication
// template docs) differ only in how they are registered (which
// autofill/zero-tap options the template declares), never in what is sent. All three send the
// same shape, made of existing keys with no OTP-specific code: a positional body parameter
// "1" (the code) and a url button at index 0 carrying the same code (the "OTP" shape).
// This test exists to catch a regression in either button-key or positional-body handling
// breaking that shape, not to add new behaviour.
func TestAuthenticationTemplatePayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	const code = "123456"
	params := map[string]string{"1": code, "button_0": code}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "login_otp", "en", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"login_otp","language":{"code":"en"},"components":[
		{"type":"body","parameters":[{"type":"text","text":"123456"}]},
		{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"123456"}]}]}}`)
}
