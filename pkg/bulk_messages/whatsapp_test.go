package bulk_messages

import (
	"testing"

	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
)

func TestBuildLoginURL(t *testing.T) {
	t.Run("with studyKey - normal chars", func(t *testing.T) {
		got := buildLoginURL("https://app.example.com", "abc123token", "flu-2025")
		expected := "https://app.example.com/link/study-login?token=abc123token&study=flu-2025"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("without studyKey", func(t *testing.T) {
		got := buildLoginURL("https://app.example.com", "abc123token", "")
		expected := "https://app.example.com/link/login?token=abc123token"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("studyKey with special chars - escaped", func(t *testing.T) {
		got := buildLoginURL("https://app.example.com", "abc123", "test&x=1")
		expected := "https://app.example.com/link/study-login?token=abc123&study=test%26x%3D1"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("token with special chars - escaped", func(t *testing.T) {
		got := buildLoginURL("https://app.example.com", "tok=en&bad", "study1")
		expected := "https://app.example.com/link/study-login?token=tok%3Den%26bad&study=study1"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
}

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
	templateWithParams := types.EmailTemplate{
		MessageType:          "weekly",
		WhatsAppTemplateName: "influenzanet_weekly_v1",
		WhatsAppParams:       map[string]string{"study_name": "studyKey"},
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
		whatsAppEnabled = false
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when WHATSAPP_ENABLED is not true")
		}
	})

	t.Run("template without WhatsApp name", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithoutWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when template has no WhatsApp name")
		}
	})

	t.Run("user without verified phone", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(noPhoneUser, templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user has no verified phone")
		}
	})

	t.Run("user with channels=email only", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user prefers email only")
		}
	})

	t.Run("user with channels=email,whatsapp - generates message", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
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

	t.Run("missing WhatsApp param key in contentInfos - returns nil (G-6)", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		// Template expects "studyKey" in contentInfos, but it's missing
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{})
		if got != nil {
			t.Error("expected nil when a required WhatsApp param is missing from contentInfos")
		}
	})

	t.Run("WhatsApp param key present in contentInfos - generates message", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{"studyKey": "flu-2025"})
		if got == nil {
			t.Fatal("expected non-nil when all params are present")
		}
		if got.ContentParams["study_name"] != "flu-2025" {
			t.Errorf("expected param study_name=flu-2025, got %q", got.ContentParams["study_name"])
		}
	})

	t.Run("fallback: empty channels + verified phone - generates message", func(t *testing.T) {
		whatsAppEnabled = true
		defer func() { whatsAppEnabled = false }()
		got := prepareOutgoingWhatsApp(verifiedPhoneUser(nil), templateWithWA, map[string]string{})
		if got == nil {
			t.Fatal("expected non-nil for fallback (empty channels + verified phone)")
		}
		if got.ToPhoneNumber != "+391234567890" {
			t.Errorf("expected phone from ContactInfos, got %q", got.ToPhoneNumber)
		}
	})
}
