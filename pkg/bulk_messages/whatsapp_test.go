package bulk_messages

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/internal/config"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
		DefaultLanguage:      "it",
		Translations:         []types.LocalizedTemplate{{Lang: "it"}, {Lang: "en"}},
		WhatsAppTemplateName: "influenzanet_weekly_v1",
	}
	templateWithoutWA := types.EmailTemplate{
		MessageType:     "weekly",
		DefaultLanguage: "it",
	}
	templateWithParams := types.EmailTemplate{
		MessageType:          "weekly",
		DefaultLanguage:      "it",
		Translations:         []types.LocalizedTemplate{{Lang: "it"}},
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
		configureWhatsAppForTest(t, false)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when WHATSAPP_ENABLED is not true")
		}
	})

	// This is the coherence fix: a gRPC server with WHATSAPP_ENABLED=true but no Meta
	// credentials must not queue WhatsApp messages that no process could ever deliver.
	t.Run("WHATSAPP_ENABLED true but no token configured", func(t *testing.T) {
		previous := configuredWhatsAppSettings()
		t.Cleanup(func() { Configure(previous) })
		Configure(config.NewWhatsAppSettings(func(key string) string {
			switch key {
			case "WHATSAPP_ENABLED":
				return "true"
			case "WHATSAPP_PHONE_NUMBER_ID":
				return "test-phone-id"
			}
			return ""
		}))
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when the switch is on but WHATSAPP_TOKEN is missing")
		}
	})

	t.Run("template without WhatsApp name", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithoutWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when template has no WhatsApp name")
		}
	})

	t.Run("user without verified phone", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", noPhoneUser, templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user has no verified phone")
		}
	})

	t.Run("user with channels=email only", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email"}), templateWithWA, map[string]string{})
		if got != nil {
			t.Error("expected nil when user prefers email only")
		}
	})

	t.Run("user with channels=email,whatsapp - generates message", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithWA, map[string]string{})
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

	t.Run("missing WhatsApp param key in contentInfos - returns nil", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		// Template expects "studyKey" in contentInfos, but it's missing
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{})
		if got != nil {
			t.Error("expected nil when a required WhatsApp param is missing from contentInfos")
		}
	})

	t.Run("WhatsApp param key present in contentInfos - generates message", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{"studyKey": "flu-2025"})
		if got == nil {
			t.Fatal("expected non-nil when all params are present")
		}
		if got.ContentParams["study_name"] != "flu-2025" {
			t.Errorf("expected param study_name=flu-2025, got %q", got.ContentParams["study_name"])
		}
	})

	// A whatsappParams value that starts with "=" is a literal, not a variable name, and
	// everything after the first "=" is the literal (so "=" alone is an empty literal and
	// "==x" is the literal "=x"). No existing contentKey starts with "=".
	t.Run("literal value is sent as-is, without looking it up in contentInfos", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		template := types.EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			Translations:         []types.LocalizedTemplate{{Lang: "it"}},
			WhatsAppTemplateName: "influenzanet_weekly_v1",
			WhatsAppParams:       map[string]string{"season": "=Stagione 2026/27"},
		}
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), template, map[string]string{})
		if got == nil {
			t.Fatal("expected a message: a literal must not need a contentInfos entry")
		}
		if got.ContentParams["season"] != "Stagione 2026/27" {
			t.Errorf("expected literal %q, got %q", "Stagione 2026/27", got.ContentParams["season"])
		}
	})

	t.Run("bare = is an empty literal", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		template := types.EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			Translations:         []types.LocalizedTemplate{{Lang: "it"}},
			WhatsAppTemplateName: "influenzanet_weekly_v1",
			WhatsAppParams:       map[string]string{"note": "="},
		}
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), template, map[string]string{})
		if got == nil {
			t.Fatal("expected a message")
		}
		if v, ok := got.ContentParams["note"]; !ok || v != "" {
			t.Errorf("expected empty literal, got %q (present=%v)", v, ok)
		}
	})

	t.Run("== escapes to a literal =", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		template := types.EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			Translations:         []types.LocalizedTemplate{{Lang: "it"}},
			WhatsAppTemplateName: "influenzanet_weekly_v1",
			WhatsAppParams:       map[string]string{"code": "==x"},
		}
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), template, map[string]string{})
		if got == nil {
			t.Fatal("expected a message")
		}
		if got.ContentParams["code"] != "=x" {
			t.Errorf("expected literal %q, got %q", "=x", got.ContentParams["code"])
		}
	})

	t.Run("variable value is still resolved from contentInfos, unchanged", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{"studyKey": "flu-2025"})
		if got == nil {
			t.Fatal("expected non-nil when all params are present")
		}
		if got.ContentParams["study_name"] != "flu-2025" {
			t.Errorf("expected param study_name=flu-2025, got %q", got.ContentParams["study_name"])
		}
	})

	t.Run("invalid template params drop the message, same as a missing variable", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		// "button_10" is not a valid button key: queue-time validation must catch this
		// before the message is ever written to the outgoing queue.
		template := types.EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			Translations:         []types.LocalizedTemplate{{Lang: "it"}},
			WhatsAppTemplateName: "influenzanet_weekly_v1",
			WhatsAppParams:       map[string]string{"button_10": "=x"},
		}
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), template, map[string]string{})
		if got != nil {
			t.Errorf("expected nil for invalid template params, got a message to %q", got.ToPhoneNumber)
		}
	})

	t.Run("missing variable still drops the message", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"email", "whatsapp"}), templateWithParams, map[string]string{})
		if got != nil {
			t.Error("expected nil when a required WhatsApp variable is missing from contentInfos")
		}
	})

	t.Run("user with no preferred channel - returns nil even with a verified phone", func(t *testing.T) {
		// An empty list means the participant never opted in to WhatsApp, or a
		// preference was lost: neither is a reason to send a paid message.
		configureWhatsAppForTest(t, true)
		for name, channels := range map[string][]string{"nil": nil, "empty": {}} {
			if got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser(channels), templateWithWA, map[string]string{}); got != nil {
				t.Errorf("%s channels: expected nil, got a message to %q", name, got.ToPhoneNumber)
			}
		}
	})

	t.Run("language: user language kept when the template is maintained in it", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		user := verifiedPhoneUser([]string{"whatsapp"})
		user.Account.PreferredLanguage = "en"
		got := prepareOutgoingWhatsApp("italy", user, templateWithWA, map[string]string{})
		if got == nil || got.Lang != "en" {
			t.Fatalf("expected a message in 'en', got %v", got)
		}
	})

	t.Run("language: unmaintained user language falls back to the template default", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		user := verifiedPhoneUser([]string{"whatsapp"})
		user.Account.PreferredLanguage = "de"
		got := prepareOutgoingWhatsApp("italy", user, templateWithWA, map[string]string{})
		if got == nil || got.Lang != "it" {
			t.Fatalf("expected a message in the default 'it', got %v", got)
		}
	})

	t.Run("language: template without any language yields no message", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		bare := types.EmailTemplate{MessageType: "weekly", WhatsAppTemplateName: "influenzanet_weekly_v1"}
		if got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), bare, map[string]string{}); got != nil {
			t.Errorf("expected nil so that the e-mail is sent instead, got a message in %q", got.Lang)
		}
	})

	t.Run("user with channels=whatsapp only - generates message", func(t *testing.T) {
		configureWhatsAppForTest(t, true)
		got := prepareOutgoingWhatsApp("italy", verifiedPhoneUser([]string{"whatsapp"}), templateWithWA, map[string]string{})
		if got == nil {
			t.Fatal("expected a message when whatsapp is the only preferred channel")
		}
		if got.ToPhoneNumber != "+391234567890" {
			t.Errorf("expected phone from ContactInfos, got %q", got.ToPhoneNumber)
		}
	})
}

// A message service whose client was never connected: every write fails at once, which is
// the shape of any outage of the queue database.
func disconnectedMessageDBService(t *testing.T) *messagedb.MessageDBService {
	t.Helper()
	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:1"))
	if err != nil {
		t.Fatalf("unexpected error creating the client: %v", err)
	}
	return &messagedb.MessageDBService{DBClient: client, DBNamePrefix: "TEST_"}
}

func TestSaveOutgoingWhatsAppWhenQueueingFails(t *testing.T) {
	configureWhatsAppForTest(t, true)

	user := &umAPI.User{
		Id:      "user1",
		Account: &umAPI.User_Account{PreferredLanguage: "it"},
		ContactInfos: []*umAPI.ContactInfo{
			{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+391234567890"}, ConfirmedAt: 200},
		},
		ContactPreferences: &umAPI.ContactPreferences{PreferredChannels: []string{"whatsapp"}},
	}
	template := types.EmailTemplate{MessageType: "weekly", DefaultLanguage: "it", WhatsAppTemplateName: "influenzanet_weekly_v1"}

	if prepared := prepareOutgoingWhatsApp("italy", user, template, map[string]string{}); prepared == nil {
		t.Fatal("precondition: the message must be preparable, so that only the queue write can fail")
	}

	// The generators fall back to e-mail exactly when this returns nil; a message that
	// was never written to the queue must therefore not be reported as queued.
	got := saveOutgoingWhatsApp(disconnectedMessageDBService(t), "italy", user, template, map[string]string{})
	if got != nil {
		t.Errorf("expected nil when the WhatsApp queue write fails, got a message to %q", got.ToPhoneNumber)
	}
}

func TestSaveOutgoingWhatsAppQueuesTheMessage(t *testing.T) {
	connStr := os.Getenv("MESSAGE_DB_CONNECTION_STR")
	if connStr == "" {
		t.Skip("MESSAGE_DB_CONNECTION_STR not set: this case needs a reachable MongoDB")
	}
	configureWhatsAppForTest(t, true)

	dbService := messagedb.NewMessageDBService(types.DBConfig{
		URI:             "mongodb://" + os.Getenv("MESSAGE_DB_USERNAME") + ":" + os.Getenv("MESSAGE_DB_PASSWORD") + "@" + connStr,
		DBNamePrefix:    "TEST_BULK_",
		Timeout:         5,
		MaxPoolSize:     2,
		IdleConnTimeout: 5,
	})
	instanceID := "bulk-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	defer func() {
		if err := dbService.DBClient.Database("TEST_BULK_" + instanceID + "_messageDB").Drop(context.Background()); err != nil {
			t.Errorf("unexpected error during cleanup: %v", err)
		}
	}()

	user := &umAPI.User{
		Id:      "user1",
		Account: &umAPI.User_Account{PreferredLanguage: "it"},
		ContactInfos: []*umAPI.ContactInfo{
			{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+391234567890"}, ConfirmedAt: 200},
		},
		ContactPreferences: &umAPI.ContactPreferences{PreferredChannels: []string{"whatsapp"}},
	}
	template := types.EmailTemplate{MessageType: "weekly", DefaultLanguage: "it", WhatsAppTemplateName: "influenzanet_weekly_v1"}

	got := saveOutgoingWhatsApp(dbService, instanceID, user, template, map[string]string{})
	if got == nil {
		t.Fatal("expected the queued message when the write succeeds")
	}
	queued, err := dbService.FetchOutgoingWhatsApp(instanceID, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error reading the queue: %v", err)
	}
	if len(queued) != 1 || queued[0].ToPhoneNumber != "+391234567890" {
		t.Errorf("expected exactly one queued message to +391234567890, got %v", queued)
	}
}

func TestResolveWhatsAppLang(t *testing.T) {
	template := types.EmailTemplate{
		MessageType:     "weekly",
		DefaultLanguage: "it",
		Translations:    []types.LocalizedTemplate{{Lang: "it"}, {Lang: "en"}},
	}
	userWith := func(lang string) *umAPI.User {
		return &umAPI.User{Account: &umAPI.User_Account{PreferredLanguage: lang}}
	}
	cases := []struct {
		name     string
		user     *umAPI.User
		template types.EmailTemplate
		want     string
	}{
		{"maintained language is kept", userWith("en"), template, "en"},
		{"unmaintained language falls back to the default", userWith("de"), template, "it"},
		{"empty language falls back to the default", userWith(""), template, "it"},
		{"no account falls back to the default", &umAPI.User{}, template, "it"},
		{"no translations and no default gives nothing", userWith("it"), types.EmailTemplate{MessageType: "weekly"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveWhatsAppLang(c.user, c.template); got != c.want {
				t.Errorf("expected %q, got %q", c.want, got)
			}
		})
	}
}
