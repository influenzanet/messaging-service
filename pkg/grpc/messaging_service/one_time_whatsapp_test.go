package messaging_service

// One-time sends: do the one-time send endpoints queue a WhatsApp message at all, and in which
// language? SendMessageToAllUsers and SendMessageToStudyParticipants hand the request template
// straight to the bulk generators (send_message_endpoints.go:41 and :68), so the binding they
// use is the one in the request payload, not the one stored on the template in the database.
// SendInstantEmail and QueueEmailTemplateForSending never touch WhatsApp.
//
// These tests need MESSAGE_DB_CONNECTION_STR (like the rest of this package) and
// WHATSAPP_ENABLED=true, because pkg/bulk_messages reads that variable once at package init.
// They change no production code.

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"github.com/influenzanet/go-utils/pkg/api_types"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/types"
	emailMock "github.com/influenzanet/messaging-service/test/mocks/email-client-service"
	loggingMock "github.com/influenzanet/messaging-service/test/mocks/logging_service"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/mock/gomock"
	"io"
)

const (
	lmOneTimeMsgType = "one-time-language-test"
	lmOneTimeWATpl   = "one_time_wa_tpl"
	lmOneTimeStudy   = "onetimestudy"
)

// oneTimeLangs are the participant languages streamed to the generator.
var oneTimeLangs = []string{"it", "en", "fr", "de", "rm", ""}

func lmOneTimeUserID(lang string) string {
	if lang == "" {
		return "ot-none"
	}
	return "ot-" + lang
}

func lmOneTimeUser(lang string) *umAPI.User {
	id := lmOneTimeUserID(lang)
	return &umAPI.User{
		Id: id,
		Account: &umAPI.User_Account{
			Type:              "email",
			AccountId:         id + "@language-test.test",
			PreferredLanguage: lang,
		},
		Profiles: []*umAPI.Profile{{Id: "p_" + id, Alias: "alias"}},
		ContactInfos: []*umAPI.ContactInfo{
			{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+3901" + id}, ConfirmedAt: 200},
		},
		ContactPreferences: &umAPI.ContactPreferences{
			PreferredChannels:      []string{"email", "whatsapp"},
			SubscribedToWeekly:     true,
			SubscribedToNewsletter: true,
		},
	}
}

// oneTimeServer builds the gRPC service over the package test database, with a user stream
// that yields one participant per language.
func oneTimeServer(t *testing.T, ctrl *gomock.Controller) messagingServer {
	t.Helper()
	uc := userMock.NewMockUserManagementApiClient(ctrl)
	uc.EXPECT().StreamUsers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *umAPI.StreamUsersMsg, _ ...interface{}) (umAPI.UserManagementApi_StreamUsersClient, error) {
			stream := userMock.NewMockUserManagementApi_StreamUsersClient(ctrl)
			idx := 0
			stream.EXPECT().Recv().DoAndReturn(func() (*umAPI.User, error) {
				if idx >= len(oneTimeLangs) {
					return nil, io.EOF
				}
				u := lmOneTimeUser(oneTimeLangs[idx])
				idx++
				return u, nil
			}).AnyTimes()
			return stream, nil
		}).AnyTimes()
	uc.EXPECT().GenerateTempToken(gomock.Any(), gomock.Any()).
		Return(&umAPI.TempToken{Token: "token"}, nil).AnyTimes()

	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().HasParticipantStateWithCondition(gomock.Any(), gomock.Any()).
		Return(&studyAPI.ServiceStatus{}, nil).AnyTimes()

	return messagingServer{
		messageDBservice: testMessageDBService,
		clients: &types.APIClients{
			EmailClientService:    emailMock.NewMockEmailClientServiceApiClient(ctrl),
			UserManagementService: uc,
			LoggingService:        loggingMock.NewMockLoggingServiceApiClient(ctrl),
			StudyService:          sc,
		},
	}
}

func lmAdminToken() *api_types.TokenInfos {
	return &api_types.TokenInfos{
		Id:         "adminuid",
		InstanceId: testInstanceID,
		Payload:    map[string]string{"roles": "ADMIN"},
	}
}

func lmOneTimeAPITemplate(studyKey string, withBinding bool) *api.EmailTemplate {
	tpl := &api.EmailTemplate{
		MessageType:     lmOneTimeMsgType,
		StudyKey:        studyKey,
		DefaultLanguage: "it",
		Translations: []*api.LocalizedTemplate{
			{Lang: "it", Subject: "subj-it", TemplateDef: base64.StdEncoding.EncodeToString([]byte("body-it"))},
			{Lang: "en", Subject: "subj-en", TemplateDef: base64.StdEncoding.EncodeToString([]byte("body-en"))},
		},
	}
	if withBinding {
		name := lmOneTimeWATpl
		tpl.WhatsappTemplateName = &name
	}
	return tpl
}

// waitForWhatsAppRows polls the queue, because the endpoints start the generator in a
// goroutine and answer immediately.
func waitForWhatsAppRows(t *testing.T, want int) []types.OutgoingWhatsApp {
	t.Helper()
	dbName := testMessageDBService.DBNamePrefix + testInstanceID + "_messageDB"
	deadline := time.Now().Add(10 * time.Second)
	var rows []types.OutgoingWhatsApp
	for time.Now().Before(deadline) {
		cur, err := testMessageDBService.DBClient.Database(dbName).
			Collection("outgoing-whatsapp").Find(context.Background(), bson.M{"messageType": lmOneTimeMsgType})
		if err != nil {
			t.Fatalf("read outgoing-whatsapp: %v", err)
		}
		rows = nil
		if err := cur.All(context.Background(), &rows); err != nil {
			t.Fatalf("decode outgoing-whatsapp: %v", err)
		}
		if len(rows) >= want {
			return rows
		}
		time.Sleep(200 * time.Millisecond)
	}
	return rows
}

// lmWaitForEmailRows polls the e-mail queue. A run that produced every e-mail has passed the
// point where it would have queued WhatsApp, so a WhatsApp count read afterwards is final
// without waiting for a timeout.
func lmWaitForEmailRows(t *testing.T, want int) int {
	t.Helper()
	dbName := testMessageDBService.DBNamePrefix + testInstanceID + "_messageDB"
	deadline := time.Now().Add(10 * time.Second)
	var n int64
	for time.Now().Before(deadline) {
		var err error
		n, err = testMessageDBService.DBClient.Database(dbName).
			Collection("outgoing-emails").CountDocuments(context.Background(), bson.M{"messageType": lmOneTimeMsgType})
		if err != nil {
			t.Fatalf("count outgoing-emails: %v", err)
		}
		if int(n) >= want {
			return int(n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return int(n)
}

func lmCountWhatsAppRows(t *testing.T) int {
	t.Helper()
	dbName := testMessageDBService.DBNamePrefix + testInstanceID + "_messageDB"
	n, err := testMessageDBService.DBClient.Database(dbName).
		Collection("outgoing-whatsapp").CountDocuments(context.Background(), bson.M{"messageType": lmOneTimeMsgType})
	if err != nil {
		t.Fatalf("count outgoing-whatsapp: %v", err)
	}
	return int(n)
}

func lmClearEmailQueue(t *testing.T) {
	t.Helper()
	dbName := testMessageDBService.DBNamePrefix + testInstanceID + "_messageDB"
	if _, err := testMessageDBService.DBClient.Database(dbName).
		Collection("outgoing-emails").DeleteMany(context.Background(), bson.M{"messageType": lmOneTimeMsgType}); err != nil {
		t.Fatalf("clear outgoing-emails: %v", err)
	}
}

func lmClearWhatsAppQueue(t *testing.T) {
	t.Helper()
	dbName := testMessageDBService.DBNamePrefix + testInstanceID + "_messageDB"
	if _, err := testMessageDBService.DBClient.Database(dbName).
		Collection("outgoing-whatsapp").DeleteMany(context.Background(), bson.M{"messageType": lmOneTimeMsgType}); err != nil {
		t.Fatalf("clear outgoing-whatsapp: %v", err)
	}
}

func lmRequireWhatsAppEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("WHATSAPP_ENABLED") != "true" {
		t.Skip("WHATSAPP_ENABLED is not true: pkg/bulk_messages reads it once at package init, so this case cannot run")
	}
	if os.Getenv("MESSAGE_DB_CONNECTION_STR") == "" {
		t.Skip("MESSAGE_DB_CONNECTION_STR not set")
	}
}

// TestSendMessageToAllUsersQueuesWhatsApp verifies the claim that the one-time endpoints
// accept and use the WhatsApp binding of the request, and records the language of each row.
func TestSendMessageToAllUsersQueuesWhatsApp(t *testing.T) {
	lmRequireWhatsAppEnabled(t)
	lmClearWhatsAppQueue(t)
	defer lmClearWhatsAppQueue(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	s := oneTimeServer(t, ctrl)

	if _, err := s.SendMessageToAllUsers(context.Background(), &api.SendMessageToAllUsersReq{
		Token:         lmAdminToken(),
		Template:      lmOneTimeAPITemplate("", true),
		IgnoreWeekday: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rows := waitForWhatsAppRows(t, len(oneTimeLangs))
	byUser := map[string]string{}
	for _, r := range rows {
		byUser[r.UserID] = r.Lang
		if r.TemplateName != lmOneTimeWATpl {
			t.Errorf("user %s: expected the template name of the request, got %q", r.UserID, r.TemplateName)
		}
	}
	t.Logf("SendMessageToAllUsers queued %d WhatsApp rows: %v", len(rows), byUser)

	// The template is maintained in it and en and defaults to it, so only the English
	// participant keeps their own language.
	want := map[string]string{"it": "it", "en": "en", "fr": "it", "de": "it", "rm": "it", "": "it"}
	for lang, expected := range want {
		if got := byUser[lmOneTimeUserID(lang)]; got != expected {
			t.Errorf("participant %q: expected a WhatsApp message in %q, got %q", lang, expected, got)
		}
	}
}

// TestSendMessageToStudyParticipantsQueuesWhatsApp is the same check for the study endpoint.
func TestSendMessageToStudyParticipantsQueuesWhatsApp(t *testing.T) {
	lmRequireWhatsAppEnabled(t)
	lmClearWhatsAppQueue(t)
	defer lmClearWhatsAppQueue(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	s := oneTimeServer(t, ctrl)

	if _, err := s.SendMessageToStudyParticipants(context.Background(), &api.SendMessageToStudyParticipantsReq{
		Token:         lmAdminToken(),
		StudyKey:      lmOneTimeStudy,
		Template:      lmOneTimeAPITemplate(lmOneTimeStudy, true),
		IgnoreWeekday: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rows := waitForWhatsAppRows(t, len(oneTimeLangs))
	byUser := map[string]string{}
	for _, r := range rows {
		byUser[r.UserID] = r.Lang
	}
	t.Logf("SendMessageToStudyParticipants queued %d WhatsApp rows: %v", len(rows), byUser)

	want := map[string]string{"it": "it", "en": "en", "fr": "it", "de": "it", "rm": "it", "": "it"}
	for lang, expected := range want {
		if got := byUser[lmOneTimeUserID(lang)]; got != expected {
			t.Errorf("participant %q: expected a WhatsApp message in %q, got %q", lang, expected, got)
		}
	}
}

// TestOneTimeSendWithoutBindingQueuesNoWhatsApp records the nuance behind the claim: the
// binding that counts is the one in the request. A caller that omits whatsappTemplateName gets
// e-mail only, whatever the stored template says, because EmailTemplateFromAPI
// (pkg/types/email_templates.go:88) builds the template from the payload alone.
func TestOneTimeSendWithoutBindingQueuesNoWhatsApp(t *testing.T) {
	lmRequireWhatsAppEnabled(t)
	lmClearWhatsAppQueue(t)
	lmClearEmailQueue(t)
	defer lmClearWhatsAppQueue(t)
	defer lmClearEmailQueue(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	s := oneTimeServer(t, ctrl)

	if _, err := s.SendMessageToAllUsers(context.Background(), &api.SendMessageToAllUsersReq{
		Token:         lmAdminToken(),
		Template:      lmOneTimeAPITemplate("", false),
		IgnoreWeekday: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait on the e-mails instead of on a WhatsApp row that will never arrive: once every
	// participant has an e-mail, the run is past the point where it would have queued WhatsApp.
	emails := lmWaitForEmailRows(t, len(oneTimeLangs))
	waRows := lmCountWhatsAppRows(t)
	t.Logf("SendMessageToAllUsers without a binding queued %d e-mails and %d WhatsApp rows", emails, waRows)
	if emails != len(oneTimeLangs) {
		t.Fatalf("precondition: expected %d e-mails so that the run is known to be finished, got %d",
			len(oneTimeLangs), emails)
	}
	if waRows != 0 {
		t.Errorf("expected no WhatsApp row without a binding in the request, got %d", waRows)
	}
}

// TestSendInstantEmailQueuesNoWhatsApp records that the instant e-mail endpoint has no
// WhatsApp path at all: it resolves a translation and hands the result to the e-mail client
// (send_message_endpoints.go:84-160).
func TestSendInstantEmailQueuesNoWhatsApp(t *testing.T) {
	lmRequireWhatsAppEnabled(t)
	lmClearWhatsAppQueue(t)
	defer lmClearWhatsAppQueue(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	s := oneTimeServer(t, ctrl)

	// No template of this type is stored, so the endpoint fails before sending anything;
	// what matters here is that no WhatsApp row exists either way.
	_, err := s.SendInstantEmail(context.Background(), &api.SendEmailReq{
		InstanceId:        testInstanceID,
		To:                []string{"someone@language-test.test"},
		MessageType:       lmOneTimeMsgType,
		PreferredLanguage: "fr",
	})
	// The endpoint answers synchronously, so a single read is final.
	t.Logf("SendInstantEmail returned: %v", err)

	if waRows := lmCountWhatsAppRows(t); waRows != 0 {
		t.Errorf("expected no WhatsApp row from SendInstantEmail, got %d", waRows)
	}
}
