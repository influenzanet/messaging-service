package bulk_messages

// The messages of a subscription, the newsletter and the weekly reminder, carry the
// participant's unsubscribe token: the e-mail can link to the unsubscribe page, and so can a
// URL button of its WhatsApp copy. Other message types mint no such token. These tests drive
// the generators against a real MongoDB (MESSAGE_DB_CONNECTION_STR, see test/testenv) and must
// not run in parallel, because the WhatsApp settings are package-level.

import (
	"context"
	"encoding/base64"
	"io"
	"sync"
	"testing"

	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/mock/gomock"
)

const unsubscribeTestToken = "the-unsubscribe-token"

// unsubscribeClients streams one participant on both channels and records every unsubscribe
// token request.
func unsubscribeClients(t *testing.T, ctrl *gomock.Controller) (*types.APIClients, *[]*api_types.TempTokenInfo) {
	t.Helper()
	var mu sync.Mutex
	requests := &[]*api_types.TempTokenInfo{}
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
				return newNoChannelUser(noChannelCase{id: "both", channels: []string{"email", "whatsapp"}}), nil
			}).AnyTimes()
			return stream, nil
		}).AnyTimes()
	uc.EXPECT().GenerateTempToken(gomock.Any(), gomock.Any()).
		Return(&umAPI.TempToken{Token: "login-token"}, nil).AnyTimes()
	uc.EXPECT().GetOrCreateTemptoken(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *api_types.TempTokenInfo, _ ...interface{}) (*umAPI.TempToken, error) {
			mu.Lock()
			defer mu.Unlock()
			*requests = append(*requests, in)
			return &umAPI.TempToken{Token: unsubscribeTestToken}, nil
		}).AnyTimes()

	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().HasParticipantStateWithCondition(gomock.Any(), gomock.Any()).
		Return(&studyAPI.ServiceStatus{}, nil).AnyTimes()
	return &types.APIClients{UserManagementService: uc, StudyService: sc}, requests
}

// unsubscribeTemplate writes the token into the e-mail body and, when withButton, into a URL
// button of the WhatsApp copy.
func unsubscribeTemplate(messageType string, withButton bool) types.EmailTemplate {
	tpl := types.EmailTemplate{
		MessageType:          messageType,
		StudyKey:             fallbackStudy,
		DefaultLanguage:      "it",
		WhatsAppTemplateName: fallbackWATemplate,
		Translations: []types.LocalizedTemplate{{
			Lang: "it", Subject: "subject",
			TemplateDef: base64.StdEncoding.EncodeToString([]byte("token={{.unsubscribeToken}}")),
		}},
	}
	if withButton {
		tpl.WhatsAppParams = map[string]string{"button_1": "unsubscribeToken"}
	}
	return tpl
}

type queuedMessages struct {
	emails   []types.OutgoingEmail
	whatsapp []types.OutgoingWhatsApp
}

func readQueues(t *testing.T, db *messagedb.MessageDBService, instanceID string) queuedMessages {
	t.Helper()
	dbName := db.DBNamePrefix + instanceID + "_messageDB"
	var out queuedMessages
	cur, err := db.DBClient.Database(dbName).Collection("outgoing-emails").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(context.Background(), &out.emails); err != nil {
		t.Fatal(err)
	}
	cur, err = db.DBClient.Database(dbName).Collection("outgoing-whatsapp").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(context.Background(), &out.whatsapp); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSubscriptionMessagesCarryTheUnsubscribeToken(t *testing.T) {
	generators := []struct {
		name string
		run  func(clients *types.APIClients, db *messagedb.MessageDBService, instanceID string, tpl types.EmailTemplate)
	}{
		{"all users", func(clients *types.APIClients, db *messagedb.MessageDBService, instanceID string, tpl types.EmailTemplate) {
			GenerateForAllUsers(clients, db, instanceID, tpl, true, "unsubscribe token test")
		}},
		{"study participants", func(clients *types.APIClients, db *messagedb.MessageDBService, instanceID string, tpl types.EmailTemplate) {
			GenerateForStudyParticipants(clients, db, instanceID, tpl, nil, true, "unsubscribe token test")
		}},
	}
	for _, g := range generators {
		for _, messageType := range []string{constants.EMAIL_TYPE_WEEKLY, constants.EMAIL_TYPE_NEWSLETTER} {
			t.Run(g.name+", "+messageType, func(t *testing.T) {
				configureWhatsAppForTest(t, true)
				db, instanceID, cleanup := fallbackDBService(t)
				defer cleanup()
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				clients, requests := unsubscribeClients(t, ctrl)

				g.run(clients, db, instanceID, unsubscribeTemplate(messageType, true))

				q := readQueues(t, db, instanceID)
				if len(q.emails) != 1 || q.emails[0].Content != "token="+unsubscribeTestToken {
					t.Errorf("expected one e-mail with the token, got %+v", q.emails)
				}
				if len(q.whatsapp) != 1 || q.whatsapp[0].ContentParams["button_1"] != unsubscribeTestToken {
					t.Errorf("expected one WhatsApp message with the token in its button, got %+v", q.whatsapp)
				}
				if len(*requests) != 1 {
					t.Fatalf("expected one unsubscribe token request, got %d", len(*requests))
				}
				if r := (*requests)[0]; r.UserId != "both" || r.InstanceId != instanceID || r.Purpose != constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER {
					t.Errorf("unexpected token request %+v", r)
				}
			})
		}
	}

	for _, messageType := range []string{constants.EMAIL_TYPE_STUDY_REMINDER, fallbackMsgType} {
		t.Run("no token for "+messageType, func(t *testing.T) {
			configureWhatsAppForTest(t, true)
			db, instanceID, cleanup := fallbackDBService(t)
			defer cleanup()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			clients, requests := unsubscribeClients(t, ctrl)

			GenerateForStudyParticipants(clients, db, instanceID, unsubscribeTemplate(messageType, false), nil, true, "unsubscribe token test")

			q := readQueues(t, db, instanceID)
			if len(q.emails) != 1 || q.emails[0].Content != "token=" {
				t.Errorf("expected one e-mail without a token, got %+v", q.emails)
			}
			if len(q.whatsapp) != 1 {
				t.Errorf("expected one WhatsApp message, got %d", len(q.whatsapp))
			}
			if len(*requests) != 0 {
				t.Errorf("an unsubscribe token was requested: %+v", *requests)
			}
		})
	}
}

// The newsletter still goes to the addresses the participant chose for it, and the weekly
// reminder to the account address.
func TestUnsubscribeTokenKeepsTheRecipients(t *testing.T) {
	cases := []struct {
		messageType string
		want        string
	}{
		{constants.EMAIL_TYPE_NEWSLETTER, "both@fallback.test"},
		{constants.EMAIL_TYPE_WEEKLY, "account@fallback.test"},
	}
	for _, c := range cases {
		t.Run(c.messageType, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			clients, _ := unsubscribeClients(t, ctrl)
			user := newNoChannelUser(noChannelCase{id: "both", channels: []string{"email"}})
			user.Account.AccountId = "account@fallback.test"
			out, err := prepareOutgoingEmail(user, clients, nil, "italy", unsubscribeTemplate(c.messageType, false), map[string]string{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.To) != 1 || out.To[0] != c.want {
				t.Errorf("got recipients %v, want [%s]", out.To, c.want)
			}
		})
	}
}
