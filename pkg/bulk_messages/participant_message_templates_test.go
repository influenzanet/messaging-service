package bulk_messages

import (
	"context"
	"encoding/base64"
	"io"
	"sync"
	"testing"

	"github.com/influenzanet/messaging-service/pkg/types"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/mock/gomock"
)

// Templates are stored per message type and study. A participant in two studies that both
// send a message of the same type gets each study's own template in one run: its subject, and
// its WhatsApp binding, which only one of the two studies has.
// Fails if the run reuses the template of whichever study came first for the other one.
func TestParticipantMessagesUseEachStudysOwnTemplate(t *testing.T) {
	configureWhatsAppForTest(t, true)
	db, instanceID, cleanup := fallbackDBService(t)
	defer cleanup()

	const withWhatsApp, emailOnly = "studywithwhatsapp", "studyemailonly"
	subjects := map[string]string{withWhatsApp: "subject of the WhatsApp study", emailOnly: "subject of the e-mail study"}
	for _, studyKey := range []string{withWhatsApp, emailOnly} {
		tpl := types.EmailTemplate{
			MessageType:     fallbackMsgType,
			StudyKey:        studyKey,
			DefaultLanguage: "it",
			Translations: []types.LocalizedTemplate{
				{Lang: "it", Subject: subjects[studyKey], TemplateDef: base64.StdEncoding.EncodeToString([]byte("body"))},
			},
		}
		if studyKey == withWhatsApp {
			tpl.WhatsAppTemplateName = fallbackWATemplate
		}
		if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
			Collection("email-templates").InsertOne(context.Background(), tpl); err != nil {
			t.Fatalf("insert the template of %s: %v", studyKey, err)
		}
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	user := newNoChannelUser(noChannelCase{id: "both", channels: []string{"email", "whatsapp"}})

	uc := userMock.NewMockUserManagementApiClient(ctrl)
	uc.EXPECT().StreamUsers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *umAPI.StreamUsersMsg, _ ...interface{}) (umAPI.UserManagementApi_StreamUsersClient, error) {
			stream := userMock.NewMockUserManagementApi_StreamUsersClient(ctrl)
			sent := false
			stream.EXPECT().Recv().DoAndReturn(func() (*umAPI.User, error) {
				if sent {
					return nil, io.EOF
				}
				sent = true
				return user, nil
			}).AnyTimes()
			return stream, nil
		}).AnyTimes()
	uc.EXPECT().GenerateTempToken(gomock.Any(), gomock.Any()).Return(&umAPI.TempToken{Token: "login-token"}, nil).AnyTimes()
	uc.EXPECT().GetOrCreateTemptoken(gomock.Any(), gomock.Any()).Return(&umAPI.TempToken{Token: "unsubscribe-token"}, nil).AnyTimes()

	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().GetStudiesWithPendingParticipantMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.Studies{Studies: []*studyAPI.Study{{Key: withWhatsApp}, {Key: emailOnly}}}, nil).AnyTimes()
	sc.EXPECT().GetStudiesForUser(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudiesForUser{Studies: []*studyAPI.StudyForUser{{Key: withWhatsApp}, {Key: emailOnly}}}, nil).AnyTimes()
	sc.EXPECT().GetParticipantMessages(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *studyAPI.GetParticipantMessagesReq, _ ...interface{}) (*studyAPI.StudyMessages, error) {
			return &studyAPI.StudyMessages{Messages: []*studyAPI.StudyMessage{
				{Id: "m_" + req.StudyKey, Type: fallbackMsgType, StudyKey: req.StudyKey, ParticipantId: "pid"},
			}}, nil
		}).AnyTimes()
	sc.EXPECT().DeleteMessagesFromParticipant(gomock.Any(), gomock.Any()).Return(&studyAPI.ServiceStatus{}, nil).AnyTimes()

	var wg sync.WaitGroup
	wg.Add(1)
	GenerateParticipantMessages(&types.APIClients{UserManagementService: uc, StudyService: sc}, db, instanceID, "template cache test", &wg)
	wg.Wait()

	dbName := db.DBNamePrefix + instanceID + "_messageDB"
	cur, err := db.DBClient.Database(dbName).Collection("outgoing-emails").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("read outgoing-emails: %v", err)
	}
	var emails []types.OutgoingEmail
	if err := cur.All(context.Background(), &emails); err != nil {
		t.Fatalf("decode outgoing-emails: %v", err)
	}
	gotSubjects := map[string]int{}
	for _, e := range emails {
		gotSubjects[e.Subject]++
	}
	for _, subject := range subjects {
		if gotSubjects[subject] != 1 {
			t.Errorf("expected one e-mail with the subject %q, got the subjects %v", subject, gotSubjects)
		}
	}

	rows := countQueues(t, db, instanceID)
	if rows.waN != 1 {
		t.Errorf("expected one WhatsApp message, for the study bound to WhatsApp only, got %d", rows.waN)
	}
}

// Researcher notifications look their template up by message type and study too. Of three
// notifications, two share a type across studies, and two have a type and a study key that
// read the same once glued together ("alert"+"study" and "alerts"+"tudy"): each must still
// get its own template.
// Fails if the run keys its templates on the type alone, or on the type and study key joined
// into one string.
func TestResearcherNotificationsUseEachTemplateOfTheirTypeAndStudy(t *testing.T) {
	db, instanceID, cleanup := fallbackDBService(t)
	defer cleanup()

	notifications := []struct{ messageType, studyKey, subject string }{
		{"alert", "study", "alert of study"},
		{"alerts", "tudy", "alerts of tudy"},
		{"alert", "other", "alert of other"},
	}
	var messages []*studyAPI.StudyMessage
	for i, n := range notifications {
		if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
			Collection("email-templates").InsertOne(context.Background(), types.EmailTemplate{
			MessageType:     n.messageType,
			StudyKey:        n.studyKey,
			DefaultLanguage: "en",
			Translations: []types.LocalizedTemplate{
				{Lang: "en", Subject: n.subject, TemplateDef: base64.StdEncoding.EncodeToString([]byte("body"))},
			},
		}); err != nil {
			t.Fatalf("insert the template %q: %v", n.subject, err)
		}
		messages = append(messages, &studyAPI.StudyMessage{
			Id: "r" + string(rune('1'+i)), Type: n.messageType, StudyKey: n.studyKey, SendTo: []string{"researcher@fallback.test"},
		})
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().GetResearcherMessages(gomock.Any(), gomock.Any()).Return(&studyAPI.StudyMessages{Messages: messages}, nil)
	sc.EXPECT().DeleteResearcherMessages(gomock.Any(), gomock.Any()).Return(&studyAPI.ServiceStatus{}, nil).Times(len(notifications))
	clients := &types.APIClients{UserManagementService: userMock.NewMockUserManagementApiClient(ctrl), StudyService: sc}

	var wg sync.WaitGroup
	wg.Add(1)
	GenerateResearcherNotificationMessages(clients, db, instanceID, "template cache test", &wg)
	wg.Wait()

	cur, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").Collection("outgoing-emails").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("read outgoing-emails: %v", err)
	}
	var emails []types.OutgoingEmail
	if err := cur.All(context.Background(), &emails); err != nil {
		t.Fatalf("decode outgoing-emails: %v", err)
	}
	gotSubjects := map[string]int{}
	for _, e := range emails {
		gotSubjects[e.Subject]++
	}
	for _, n := range notifications {
		if gotSubjects[n.subject] != 1 {
			t.Errorf("expected one e-mail with the subject %q, got the subjects %v", n.subject, gotSubjects)
		}
	}
}
