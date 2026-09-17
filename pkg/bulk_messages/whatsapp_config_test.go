package bulk_messages

import "testing"

// This test writes the package-level whatsAppEnabled flag, the same flag the generators
// read, so it must not run in parallel with the other WhatsApp tests.
func TestSetWhatsAppGenerationEnabled(t *testing.T) {
	previous := WhatsAppGenerationEnabled()
	defer SetWhatsAppGenerationEnabled(previous)

	SetWhatsAppGenerationEnabled(true)
	if !WhatsAppGenerationEnabled() {
		t.Fatal("expected WhatsApp generation to be enabled")
	}
	if !whatsAppEnabled {
		t.Fatal("expected the flag the generators read to be enabled")
	}

	SetWhatsAppGenerationEnabled(false)
	if WhatsAppGenerationEnabled() {
		t.Fatal("expected WhatsApp generation to be disabled")
	}
	if whatsAppEnabled {
		t.Fatal("expected the flag the generators read to be disabled")
	}
}
