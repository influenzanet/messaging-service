package bulk_messages

// End-to-end check that the per-instance decision now sits with user-management: a
// WhatsApp-only participant (preferred_channels == ["whatsapp"], verified phone) gets the
// e-mail when user-management says WhatsApp is off for the instance, and the WhatsApp message
// when it says on - exactly the two halves of the activation design in one test, reusing the
// generator harness of generators_whatsapp_only_test.go. They need MESSAGE_DB_CONNECTION_STR
// and are skipped otherwise, and must not run in parallel because the log capture swaps a
// package-level writer.

import (
	"testing"

	"github.com/influenzanet/messaging-service/internal/config"
	"go.uber.org/mock/gomock"
)

// platformConfiguredWhatsAppSettings builds a WhatsAppSettings whose kill switch and
// credentials are always on, so that the only thing deciding Enabled is the source's answer -
// the per-instance half this test is about.
func platformConfiguredWhatsAppSettings(on bool) config.WhatsAppSettings {
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
	}, allInstancesPlatformConfigSource(on))
}

func TestWhatsAppOnlyUserFollowsUserManagementPlatformConfig(t *testing.T) {
	for _, g := range whatsAppOnlyGenerators {
		g := g
		t.Run(g.name, func(t *testing.T) {
			t.Run("user-management says WhatsApp is off for the instance", func(t *testing.T) {
				previous := configuredWhatsAppSettings()
				t.Cleanup(func() { Configure(previous) })
				Configure(platformConfiguredWhatsAppSettings(false))

				db, instanceID, cleanup := fallbackDBService(t)
				defer cleanup()
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				clients := newFallbackClients(t, ctrl)
				logs := captureLogs(t)

				g.run(t, clients, db, instanceID, true)

				rows := countQueues(t, db, instanceID)
				t.Logf("%s (UM off): emails=%v whatsapp=%v", g.name, rows.emails, rows.whatsapp)
				if rows.whatsapp["waonly"] != 0 || rows.emails["waonly"] != 1 {
					t.Errorf("whatsapp-only user: expected the e-mail fallback (0 WhatsApp, 1 e-mail), got %d / %d",
						rows.whatsapp["waonly"], rows.emails["waonly"])
				}
				if rows.waN != 0 {
					t.Errorf("expected no WhatsApp row at all while user-management is off, got %d", rows.waN)
				}
				total, failed := logs.counters(t)
				if total != len(fallbackUsers) || failed != 0 {
					t.Errorf("expected total=%d failed=0, got total=%d failed=%d", len(fallbackUsers), total, failed)
				}
			})

			t.Run("user-management says WhatsApp is on for the instance", func(t *testing.T) {
				previous := configuredWhatsAppSettings()
				t.Cleanup(func() { Configure(previous) })
				Configure(platformConfiguredWhatsAppSettings(true))

				db, instanceID, cleanup := fallbackDBService(t)
				defer cleanup()
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				clients := newFallbackClients(t, ctrl)
				logs := captureLogs(t)

				g.run(t, clients, db, instanceID, true)

				rows := countQueues(t, db, instanceID)
				t.Logf("%s (UM on): emails=%v whatsapp=%v", g.name, rows.emails, rows.whatsapp)
				if rows.whatsapp["waonly"] != 1 || rows.emails["waonly"] != 0 {
					t.Errorf("whatsapp-only user: expected the WhatsApp message (1 WhatsApp, 0 e-mail), got %d / %d",
						rows.whatsapp["waonly"], rows.emails["waonly"])
				}
				total, failed := logs.counters(t)
				if total != len(fallbackUsers) || failed != 0 {
					t.Errorf("expected total=%d failed=0, got total=%d failed=%d", len(fallbackUsers), total, failed)
				}
			})
		})
	}
}
