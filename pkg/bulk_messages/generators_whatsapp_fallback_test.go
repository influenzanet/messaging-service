package bulk_messages

// The three bulk generators decide on the e-mail from the value saveOutgoingWhatsApp returns.
// These tests drive each generator against a real MongoDB where the outgoing-whatsapp
// collection rejects every write, and check the user-visible outcome: the e-mail goes out, the
// run counts a success only when something was queued, and a participant message is consumed
// only after at least one channel took it. They need MESSAGE_DB_CONNECTION_STR and are skipped
// otherwise. The log capture swaps a package-level writer, so they must not run in parallel.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/mock/gomock"
)

const (
	fallbackMsgType    = "fallback-test-type" // not one of the message types with dedicated handling
	fallbackWATemplate = "fallback_test_wa_tpl"
	fallbackStudy      = "fallbackteststudy"
	fallbackDBPrefix   = "TEST_FALLBACK_"
)

// The two users the stream yields. Both have a verified phone and whatsapp among their
// channels, so both reach the WhatsApp queue write.
type fallbackUser struct {
	id       string
	channels []string
}

var fallbackUsers = []fallbackUser{
	{id: "waonly", channels: []string{"whatsapp"}},
	{id: "emailwa", channels: []string{"email", "whatsapp"}},
}

func newFallbackUser(c fallbackUser) *umAPI.User {
	return &umAPI.User{
		Id: c.id,
		Account: &umAPI.User_Account{
			Type:              "email",
			AccountId:         c.id + "@fallback.test",
			PreferredLanguage: "it",
		},
		Profiles: []*umAPI.Profile{{Id: "p_" + c.id, Alias: "alias"}},
		ContactInfos: []*umAPI.ContactInfo{
			{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+3900" + c.id}, ConfirmedAt: 200},
		},
		ContactPreferences: &umAPI.ContactPreferences{
			PreferredChannels:      c.channels,
			SubscribedToWeekly:     true,
			SubscribedToNewsletter: true,
		},
	}
}

func fallbackTemplate(studyKey string) types.EmailTemplate {
	return types.EmailTemplate{
		MessageType:          fallbackMsgType,
		StudyKey:             studyKey,
		DefaultLanguage:      "it",
		WhatsAppTemplateName: fallbackWATemplate,
		Translations: []types.LocalizedTemplate{
			{Lang: "it", Subject: "subject", TemplateDef: base64.StdEncoding.EncodeToString([]byte("body"))},
		},
	}
}

// rejectEverything is a document validator no document can satisfy.
var rejectEverything = bson.M{"neverPresentField": bson.M{"$exists": true}}

func fallbackDBService(t *testing.T, failingCollections ...string) (*messagedb.MessageDBService, string, func()) {
	t.Helper()
	connStr := os.Getenv("MESSAGE_DB_CONNECTION_STR")
	if connStr == "" {
		t.Skip("MESSAGE_DB_CONNECTION_STR not set")
	}
	db := messagedb.NewMessageDBService(types.DBConfig{
		URI:             "mongodb://" + os.Getenv("MESSAGE_DB_USERNAME") + ":" + os.Getenv("MESSAGE_DB_PASSWORD") + "@" + connStr,
		DBNamePrefix:    fallbackDBPrefix,
		Timeout:         10,
		MaxPoolSize:     4,
		IdleConnTimeout: 10,
	})
	instanceID := "fallback-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	dbName := fallbackDBPrefix + instanceID + "_messageDB"
	for _, coll := range failingCollections {
		if err := db.DBClient.Database(dbName).CreateCollection(
			context.Background(), coll,
			options.CreateCollection().SetValidator(rejectEverything),
		); err != nil {
			t.Fatalf("could not create the rejecting collection %q: %v", coll, err)
		}
	}
	return db, instanceID, func() {
		if err := db.DBClient.Database(dbName).Drop(context.Background()); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}
}

type queueRows struct {
	emails   map[string]int // recipient local part -> rows
	whatsapp map[string]int // userId -> rows
	emailsN  int
	waN      int
}

func countQueues(t *testing.T, db *messagedb.MessageDBService, instanceID string) queueRows {
	t.Helper()
	dbName := db.DBNamePrefix + instanceID + "_messageDB"
	out := queueRows{emails: map[string]int{}, whatsapp: map[string]int{}}

	cur, err := db.DBClient.Database(dbName).Collection("outgoing-emails").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("read outgoing-emails: %v", err)
	}
	var emails []types.OutgoingEmail
	if err := cur.All(context.Background(), &emails); err != nil {
		t.Fatalf("decode outgoing-emails: %v", err)
	}
	for _, e := range emails {
		out.emailsN++
		for _, to := range e.To {
			out.emails[strings.TrimSuffix(to, "@fallback.test")]++
		}
	}

	cur, err = db.DBClient.Database(dbName).Collection("outgoing-whatsapp").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("read outgoing-whatsapp: %v", err)
	}
	var was []types.OutgoingWhatsApp
	if err := cur.All(context.Background(), &was); err != nil {
		t.Fatalf("decode outgoing-whatsapp: %v", err)
	}
	for _, w := range was {
		out.waN++
		out.whatsapp[w.UserID]++
	}
	return out
}

type capturedLogs struct {
	info *bytes.Buffer
	err  *bytes.Buffer
}

func captureLogs(t *testing.T) *capturedLogs {
	t.Helper()
	l := &capturedLogs{info: &bytes.Buffer{}, err: &bytes.Buffer{}}
	logger.Info.SetOutput(l.info)
	logger.Error.SetOutput(l.err)
	t.Cleanup(func() {
		logger.Info.SetOutput(os.Stdout)
		logger.Error.SetOutput(os.Stderr)
	})
	return l
}

var counterLineRe = regexp.MustCompile(`Generated (\d+) \((\d+) failed\)`)

// counters returns total and failed as the generator logged them.
func (l *capturedLogs) counters(t *testing.T) (total int, failed int) {
	t.Helper()
	m := counterLineRe.FindStringSubmatch(l.info.String())
	if m == nil {
		t.Fatalf("no counter line in the info log:\n%s", l.info.String())
	}
	total, _ = strconv.Atoi(m[1])
	failed, _ = strconv.Atoi(m[2])
	return total, failed
}

func (l *capturedLogs) fallbackLines() int {
	return strings.Count(l.err.String(), "falling back to e-mail")
}

type fallbackClients struct {
	api     *types.APIClients
	deleted *[]string // "<profileId>:<messageIds joined>" per DeleteMessagesFromParticipant call
}

func newFallbackClients(t *testing.T, ctrl *gomock.Controller) fallbackClients {
	t.Helper()
	deleted := &[]string{}

	uc := userMock.NewMockUserManagementApiClient(ctrl)
	uc.EXPECT().StreamUsers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *umAPI.StreamUsersMsg, _ ...interface{}) (umAPI.UserManagementApi_StreamUsersClient, error) {
			stream := userMock.NewMockUserManagementApi_StreamUsersClient(ctrl)
			idx := 0
			stream.EXPECT().Recv().DoAndReturn(func() (*umAPI.User, error) {
				if idx >= len(fallbackUsers) {
					return nil, io.EOF
				}
				u := newFallbackUser(fallbackUsers[idx])
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
	sc.EXPECT().GetStudiesWithPendingParticipantMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.Studies{Studies: []*studyAPI.Study{{Key: fallbackStudy}}}, nil).AnyTimes()
	sc.EXPECT().GetStudiesForUser(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudiesForUser{Studies: []*studyAPI.StudyForUser{{Key: fallbackStudy}}}, nil).AnyTimes()
	sc.EXPECT().GetParticipantMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudyMessages{Messages: []*studyAPI.StudyMessage{
			{Id: "m1", Type: fallbackMsgType, StudyKey: fallbackStudy, ParticipantId: "pid"},
		}}, nil).AnyTimes()
	sc.EXPECT().DeleteMessagesFromParticipant(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *studyAPI.DeleteMessagesFromParticipantReq, _ ...interface{}) (*studyAPI.ServiceStatus, error) {
			*deleted = append(*deleted, req.ProfileId+":"+strings.Join(req.MessageIds, ","))
			return &studyAPI.ServiceStatus{}, nil
		}).AnyTimes()

	return fallbackClients{api: &types.APIClients{UserManagementService: uc, StudyService: sc}, deleted: deleted}
}

func TestWhatsAppQueueRejectsWhileEmailQueueAccepts(t *testing.T) {
	db, instanceID, cleanup := fallbackDBService(t, "outgoing-whatsapp")
	defer cleanup()

	_, err := db.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
		MessageType: fallbackMsgType, ToPhoneNumber: "+390000", TemplateName: fallbackWATemplate, UserID: "waonly",
	})
	if err == nil {
		t.Fatal("expected the WhatsApp queue write to fail against the rejecting collection")
	}
	var we mongo.WriteException
	if !errors.As(err, &we) {
		t.Fatalf("expected a mongo.WriteException, got %T: %v", err, err)
	}
	codes := []int{}
	for _, e := range we.WriteErrors {
		codes = append(codes, e.Code)
	}
	t.Logf("WhatsApp write error: codes=%v msg=%v", codes, err)
	if len(codes) != 1 || codes[0] != 121 {
		t.Errorf("expected a single DocumentValidationFailure (121), got %v", codes)
	}

	if _, err := db.AddToOutgoingEmails(instanceID, types.OutgoingEmail{
		MessageType: fallbackMsgType, To: []string{"waonly@fallback.test"}, Subject: "s", Content: "c",
	}); err != nil {
		t.Fatalf("expected the e-mail queue write to succeed, got %v", err)
	}

	rows := countQueues(t, db, instanceID)
	if rows.emailsN != 1 || rows.waN != 0 {
		t.Errorf("expected 1 e-mail row and 0 WhatsApp rows, got %d / %d", rows.emailsN, rows.waN)
	}
}

// assertFallbackTaken checks the outcome for users whose WhatsApp queue write failed:
// exactly one e-mail each, no WhatsApp row, every user counted as a success, and one
// fallback line in the log per failed write.
func assertFallbackTaken(t *testing.T, label string, rows queueRows, logs *capturedLogs) {
	t.Helper()
	t.Logf("%s: emails=%v whatsapp=%v", label, rows.emails, rows.whatsapp)
	for _, c := range fallbackUsers {
		if rows.emails[c.id] != 1 {
			t.Errorf("%s: user %s: expected exactly 1 outgoing-emails row, got %d", label, c.id, rows.emails[c.id])
		}
		if rows.whatsapp[c.id] != 0 {
			t.Errorf("%s: user %s: expected 0 outgoing-whatsapp rows, got %d", label, c.id, rows.whatsapp[c.id])
		}
	}
	if rows.emailsN != len(fallbackUsers) || rows.waN != 0 {
		t.Errorf("%s: expected %d e-mail rows and 0 WhatsApp rows in total, got %d / %d",
			label, len(fallbackUsers), rows.emailsN, rows.waN)
	}
	total, failed := logs.counters(t)
	t.Logf("%s: counters total=%d failed=%d, fallback log lines=%d", label, total, failed, logs.fallbackLines())
	if total != len(fallbackUsers) || failed != 0 {
		t.Errorf("%s: expected total=%d failed=0, got total=%d failed=%d", label, len(fallbackUsers), total, failed)
	}
	if logs.fallbackLines() != len(fallbackUsers) {
		t.Errorf("%s: expected one 'falling back to e-mail' line per failed write (%d), got %d",
			label, len(fallbackUsers), logs.fallbackLines())
	}
}

func TestGenerateForAllUsersFallsBackToEmailWhenWhatsAppQueueFails(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()
	db, instanceID, cleanup := fallbackDBService(t, "outgoing-whatsapp")
	defer cleanup()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := newFallbackClients(t, ctrl)
	logs := captureLogs(t)

	GenerateForAllUsers(clients.api, db, instanceID, fallbackTemplate(""), true, "fallback test")

	assertFallbackTaken(t, "GenerateForAllUsers", countQueues(t, db, instanceID), logs)
}

func TestGenerateForStudyParticipantsFallsBackToEmailWhenWhatsAppQueueFails(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()
	db, instanceID, cleanup := fallbackDBService(t, "outgoing-whatsapp")
	defer cleanup()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := newFallbackClients(t, ctrl)
	logs := captureLogs(t)

	GenerateForStudyParticipants(clients.api, db, instanceID, fallbackTemplate(fallbackStudy), nil, true, "fallback test")

	assertFallbackTaken(t, "GenerateForStudyParticipants", countQueues(t, db, instanceID), logs)
}

func insertFallbackTemplate(t *testing.T, db *messagedb.MessageDBService, instanceID string) {
	t.Helper()
	if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
		Collection("email-templates").InsertOne(context.Background(), fallbackTemplate(fallbackStudy)); err != nil {
		t.Fatalf("insert template: %v", err)
	}
}

func TestGenerateParticipantMessagesFallsBackToEmailWhenWhatsAppQueueFails(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()
	db, instanceID, cleanup := fallbackDBService(t, "outgoing-whatsapp")
	defer cleanup()
	insertFallbackTemplate(t, db, instanceID)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := newFallbackClients(t, ctrl)
	logs := captureLogs(t)

	var wg sync.WaitGroup
	wg.Add(1)
	GenerateParticipantMessages(clients.api, db, instanceID, "fallback test", &wg)
	wg.Wait()

	assertFallbackTaken(t, "GenerateParticipantMessages", countQueues(t, db, instanceID), logs)

	// the e-mail went out, so the source message is consumed
	t.Logf("GenerateParticipantMessages: DeleteMessagesFromParticipant calls=%v", *clients.deleted)
	if len(*clients.deleted) != len(fallbackUsers) {
		t.Errorf("expected the source message to be deleted once per user, got %v", *clients.deleted)
	}
}

func TestParticipantMessageSurvivesWhenBothQueuesFail(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()
	db, instanceID, cleanup := fallbackDBService(t, "outgoing-whatsapp", "outgoing-emails")
	defer cleanup()
	insertFallbackTemplate(t, db, instanceID)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := newFallbackClients(t, ctrl)
	logs := captureLogs(t)

	var wg sync.WaitGroup
	wg.Add(1)
	GenerateParticipantMessages(clients.api, db, instanceID, "fallback test", &wg)
	wg.Wait()

	rows := countQueues(t, db, instanceID)
	total, failed := logs.counters(t)
	t.Logf("both queues failing: emails=%d whatsapp=%d total=%d failed=%d deleted=%v",
		rows.emailsN, rows.waN, total, failed, *clients.deleted)

	if rows.emailsN != 0 || rows.waN != 0 {
		t.Errorf("expected nothing queued, got emails=%d whatsapp=%d", rows.emailsN, rows.waN)
	}
	if total != len(fallbackUsers) || failed != len(fallbackUsers) {
		t.Errorf("expected every message counted as failed, got total=%d failed=%d", total, failed)
	}
	if len(*clients.deleted) != 0 {
		t.Errorf("the source message must not be deleted when nothing was queued, got %v", *clients.deleted)
	}
}
