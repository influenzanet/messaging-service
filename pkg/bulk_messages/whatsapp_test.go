package bulk_messages

import (
	"os"
	"testing"

	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
)

func TestGetVerifiedPhone(t *testing.T) {
	t.Run("no contact infos", func(t *testing.T) {
		user := &umAPI.User{}
		if got := getVerifiedPhone(user); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("only email contact", func(t *testing.T) {
		user := &umAPI.User{
			ContactInfos: []*umAPI.ContactInfo{
				{Type: "email", Address: &umAPI.ContactInfo_Email{Email: "a@b.com"}, ConfirmedAt: 100},
			},
		}
		if got := getVerifiedPhone(user); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("unverified phone", func(t *testing.T) {
		user := &umAPI.User{
			ContactInfos: []*umAPI.ContactInfo{
				{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+391234567890"}, ConfirmedAt: 0},
			},
		}
		if got := getVerifiedPhone(user); got != "" {
			t.Errorf("expected empty for unverified phone, got %q", got)
		}
	})

	t.Run("verified phone", func(t *testing.T) {
		user := &umAPI.User{
			ContactInfos: []*umAPI.ContactInfo{
				{Type: "email", Address: &umAPI.ContactInfo_Email{Email: "a@b.com"}, ConfirmedAt: 100},
				{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+391234567890"}, ConfirmedAt: 200},
			},
		}
		if got := getVerifiedPhone(user); got != "+391234567890" {
			t.Errorf("expected +391234567890, got %q", got)
		}
	})
}

func TestPrepareOutgoingWhatsApp(t *testing.T) {
	templateWithWA := types.EmailTemplate{
		MessageType:          "weekly",
		WhatsAppTemplateName: "influenzanet_weekly_v1",
	}
	templateWithoutWA := types.EmailTemplate{
		MessageType: "weekly",
	}

	verifiedPhoneUser := func(channels []string) *umAPI.User {
		return &umAPI.User{
			Id:      "user1",
			Account: &umAPI.User_Account{PreferredLanguage: "it"},
			ContactInfos: []*umAPI.ContactInfo{
				{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+391234567890"}, ConfirmedAt: 200},
			},
			ContactPreferences: &umAPI.ContactPreferences{
				PreferredChannels: channels,
			},
		}
	}

	noPhoneUser := &umAPI.User{
		Id:                 "user2",
		Account:            &umAPI.User_Account{PreferredLanguage: "it"},
		ContactInfos:       []*umAPI.ContactInfo{},
		ContactPreferences: &umAPI.ContactPreferences{},
	}

	t.Run("WHATSAPP_ENABLED not set", func(t *testing.T) {
		os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when WHATSAPP_ENABLED is not true")
		}
	})

	t.Run("template without WhatsApp name", func(t *testing.T) {
		os.Setenv("WHATSAPP_ENABLED", "true")
		defer os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithoutWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when template has no WhatsApp name")
		}
	})

	t.Run("user without verified phone", func(t *testing.T) {
		os.Setenv("WHATSAPP_ENABLED", "true")
		defer os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(noPhoneUser, templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user has no verified phone")
		}
	})

	t.Run("user with channels=email only", func(t *testing.T) {
		os.Setenv("WHATSAPP_ENABLED", "true")
		defer os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user prefers email only")
		}
	})

	t.Run("user with channels=email,whatsapp - generates message", func(t *testing.T) {
		os.Setenv("WHATSAPP_ENABLED", "true")
		defer os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
		if got == nil {
			t.Fatal("expected non-nil OutgoingWhatsApp")
		}
		if got.ToPhoneNumber != "+391234567890" {
			t.Errorf("expected phone from ContactInfos, got %q", got.ToPhoneNumber)
		}
		if got.TemplateName != "influenzanet_weekly_v1" {
			t.Errorf("unexpected template name: %q", got.TemplateName)
		}
	})

	t.Run("fallback: empty channels + verified phone - generates message", func(t *testing.T) {
		os.Setenv("WHATSAPP_ENABLED", "true")
		defer os.Unsetenv("WHATSAPP_ENABLED")
		got := prepareOutgoingWhatsApp(verifiedPhoneUser(nil), templateWithWA, map[string]string{})
		if got == nil {
			t.Fatal("expected non-nil for fallback (empty channels + verified phone)")
		}
		if got.ToPhoneNumber != "+391234567890" {
			t.Errorf("expected phone from ContactInfos, got %q", got.ToPhoneNumber)
		}
	})
}
