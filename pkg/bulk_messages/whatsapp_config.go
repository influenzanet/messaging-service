package bulk_messages

import (
	"sync/atomic"

	"github.com/influenzanet/messaging-service/internal/config"
)

// whatsAppSettingsStore holds the WhatsAppSettings the generators read. A process that never
// calls Configure keeps the nil value, which configuredWhatsAppSettings reports as the zero
// WhatsAppSettings - disabled for every instance.
var whatsAppSettingsStore atomic.Pointer[config.WhatsAppSettings]

// Configure points the generators at settings, replacing whatever was configured before. Call
// it once at start-up, before any generator runs: the gRPC server configures its own
// environment-read settings directly, the message-scheduler configures a disabled value
// instead when it cannot deliver what it would generate.
func Configure(settings config.WhatsAppSettings) {
	whatsAppSettingsStore.Store(&settings)
}

// configuredWhatsAppSettings returns what Configure last stored, or the zero value (disabled)
// when Configure was never called.
func configuredWhatsAppSettings() config.WhatsAppSettings {
	if settings := whatsAppSettingsStore.Load(); settings != nil {
		return *settings
	}
	return config.WhatsAppSettings{}
}
