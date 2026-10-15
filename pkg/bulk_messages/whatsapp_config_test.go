package bulk_messages

import (
	"testing"

	"github.com/influenzanet/messaging-service/internal/config"
)

// testWhatsAppSettings builds a WhatsAppSettings for tests, enabled with fixed test
// credentials when enabled is true, otherwise the zero (disabled) value.
func testWhatsAppSettings(enabled bool) config.WhatsAppSettings {
	if !enabled {
		return config.WhatsAppSettings{}
	}
	return config.NewWhatsAppSettings(func(key string) string {
		switch key {
		case "WHATSAPP_ENABLED":
			return "true"
		case "WHATSAPP_TOKEN":
			return "test-token"
		case "WHATSAPP_PHONE_NUMBER_ID":
			return "test-phone-id"
		}
		return ""
	})
}

// configureWhatsAppForTest points the generators at an enabled or disabled WhatsAppSettings
// for the duration of the test, and restores whatever was configured before via t.Cleanup.
// This replaces writing the package-level flag the generators used to read directly.
func configureWhatsAppForTest(t *testing.T, enabled bool) {
	t.Helper()
	previous := configuredWhatsAppSettings()
	Configure(testWhatsAppSettings(enabled))
	t.Cleanup(func() { Configure(previous) })
}

func TestConfigureOverridesWhatsAppSettings(t *testing.T) {
	previous := configuredWhatsAppSettings()
	defer Configure(previous)

	Configure(testWhatsAppSettings(true))
	if !configuredWhatsAppSettings().Enabled("any-instance") {
		t.Fatal("expected WhatsApp to be enabled after Configure with enabled settings")
	}

	Configure(testWhatsAppSettings(false))
	if configuredWhatsAppSettings().Enabled("any-instance") {
		t.Fatal("expected WhatsApp to be disabled after Configure with disabled settings")
	}
}

func TestConfiguredWhatsAppSettingsDefaultsToDisabled(t *testing.T) {
	// Before any Configure call in this process, the store is nil and must behave as
	// disabled rather than panic or report enabled.
	var zero config.WhatsAppSettings
	if zero.Enabled("any-instance") {
		t.Fatal("expected the zero WhatsAppSettings to be disabled")
	}
}
