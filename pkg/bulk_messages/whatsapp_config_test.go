package bulk_messages

import (
	"context"
	"sync"
	"testing"

	"github.com/influenzanet/messaging-service/internal/config"
	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
)

// allInstancesPlatformConfigSource is a config.PlatformConfigSource that answers the same
// WhatsApp-enabled value for every instance it is asked about, for tests that do not care
// about per-instance behaviour and just need user-management to agree with the local switch.
type allInstancesPlatformConfigSource bool

func (s allInstancesPlatformConfigSource) GetPlatformConfig(_ context.Context, _ *umAPI.PlatformConfigReq, _ ...grpc.CallOption) (*umAPI.PlatformConfig, error) {
	return &umAPI.PlatformConfig{WhatsappEnabled: bool(s)}, nil
}

// testWhatsAppSettings builds a WhatsAppSettings for tests, enabled with fixed test
// credentials and a source that reports every instance as on when enabled is true, otherwise
// the zero (disabled) value.
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
	}, allInstancesPlatformConfigSource(true))
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

// The generators read the settings from many goroutines; Configure must be safe to call while
// they do. Run with -race: a plain variable here would be reported as a data race.
func TestConfigureIsSafeWhileGeneratorsRead(t *testing.T) {
	previous := configuredWhatsAppSettings()
	defer Configure(previous)

	user := &umAPI.User{Id: "race-user"}
	template := types.EmailTemplate{MessageType: "race", WhatsAppTemplateName: "race_tpl"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(enabled bool) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				Configure(testWhatsAppSettings(enabled))
			}
		}(i%2 == 0)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = prepareOutgoingWhatsApp("race-instance", user, template, map[string]string{})
			}
		}()
	}
	wg.Wait()
}
