package bulk_messages

// A participant whose channel list is empty turned every notification off, and must get
// nothing from any bulk generator: no e-mail, no WhatsApp, and no login or unsubscribe token
// minted for a message nobody will read. In particular the e-mail fallback that reaches a
// WhatsApp-only participant when WhatsApp cannot be prepared must not reach them. The other
// participants of the same run must be served exactly as before. These tests drive each
// generator against a real MongoDB; they need MESSAGE_DB_CONNECTION_STR (see test/testenv)
// and must not run in parallel, because the log capture swaps package-level writers.

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.uber.org/mock/gomock"
)

type noChannelCase struct {
	id       string
	channels []string
	silent   bool // must receive nothing
}

// Every participant has a confirmed e-mail, a verified phone and every subscription on, so
// only the channel list decides what they get.
var noChannelUsers = []noChannelCase{
	{id: "nochannel", channels: []string{}, silent: true},
	{id: "nilchannels", channels: nil, silent: true},
	{id: "unknownchannel", channels: []string{"sms"}, silent: true},
	{id: "emailonly", channels: []string{"email"}},
	{id: "waonly", channels: []string{"whatsapp"}},
	{id: "both", channels: []string{"email", "whatsapp"}},
}

func silentNoChannelUsers() int {
	n := 0
	for _, c := range noChannelUsers {
		if c.silent {
			n++
		}
	}
	return n
}

func newNoChannelUser(c noChannelCase) *umAPI.User {
	return &umAPI.User{
		Id: c.id,
		Account: &umAPI.User_Account{
			Type:              "email",
			AccountId:         c.id + "@fallback.test",
			PreferredLanguage: "it",
		},
		Profiles: []*umAPI.Profile{{Id: "p_" + c.id, Alias: "alias"}},
		ContactInfos: []*umAPI.ContactInfo{
			{Id: "e_" + c.id, Type: "email", Address: &umAPI.ContactInfo_Email{Email: c.id + "@fallback.test"}, ConfirmedAt: 100},
			{Id: "ph_" + c.id, Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+3900" + c.id}, ConfirmedAt: 200},
		},
		ContactPreferences: &umAPI.ContactPreferences{
			PreferredChannels:      c.channels,
			SubscribedToWeekly:     true,
			SubscribedToNewsletter: true,
			SendNewsletterTo:       []string{"e_" + c.id},
		},
	}
}

type noChannelClients struct {
	api *types.APIClients
	// tokenUsers lists the user ID of every login (GenerateTempToken) and unsubscribe
	// (GetOrCreateTemptoken) token request.
	tokenUsers *[]string
	// consumed lists the profile of every DeleteMessagesFromParticipant call.
	consumed *[]string
}

func newNoChannelClients(t *testing.T, ctrl *gomock.Controller, participantMsgType string) noChannelClients {
	t.Helper()
	var mu sync.Mutex
	tokenUsers := &[]string{}
	consumed := &[]string{}
	recordToken := func(in *api_types.TempTokenInfo) {
		mu.Lock()
		defer mu.Unlock()
		*tokenUsers = append(*tokenUsers, in.UserId)
	}

	uc := userMock.NewMockUserManagementApiClient(ctrl)
	uc.EXPECT().StreamUsers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *umAPI.StreamUsersMsg, _ ...interface{}) (umAPI.UserManagementApi_StreamUsersClient, error) {
			stream := userMock.NewMockUserManagementApi_StreamUsersClient(ctrl)
			idx := 0
			stream.EXPECT().Recv().DoAndReturn(func() (*umAPI.User, error) {
				if idx >= len(noChannelUsers) {
					return nil, io.EOF
				}
				u := newNoChannelUser(noChannelUsers[idx])
				idx++
				return u, nil
			}).AnyTimes()
			return stream, nil
		}).AnyTimes()
	uc.EXPECT().GenerateTempToken(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *api_types.TempTokenInfo, _ ...interface{}) (*umAPI.TempToken, error) {
			recordToken(in)
			return &umAPI.TempToken{Token: "login-token"}, nil
		}).AnyTimes()
	uc.EXPECT().GetOrCreateTemptoken(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *api_types.TempTokenInfo, _ ...interface{}) (*umAPI.TempToken, error) {
			recordToken(in)
			return &umAPI.TempToken{Token: "unsubscribe-token"}, nil
		}).AnyTimes()

	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().HasParticipantStateWithCondition(gomock.Any(), gomock.Any()).
		Return(&studyAPI.ServiceStatus{}, nil).AnyTimes()
	sc.EXPECT().GetStudiesWithPendingParticipantMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.Studies{Studies: []*studyAPI.Study{{Key: fallbackStudy}}}, nil).AnyTimes()
	sc.EXPECT().GetStudiesForUser(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudiesForUser{Studies: []*studyAPI.StudyForUser{{Key: fallbackStudy}}}, nil).AnyTimes()
	sc.EXPECT().GetParticipantMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudyMessages{Messages: []*studyAPI.StudyMessage{
			{Id: "m1", Type: participantMsgType, StudyKey: fallbackStudy, ParticipantId: "pid"},
		}}, nil).AnyTimes()
	sc.EXPECT().DeleteMessagesFromParticipant(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *studyAPI.DeleteMessagesFromParticipantReq, _ ...interface{}) (*studyAPI.ServiceStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			*consumed = append(*consumed, req.ProfileId+":"+strings.Join(req.MessageIds, ","))
			return &studyAPI.ServiceStatus{}, nil
		}).AnyTimes()

	return noChannelClients{
		api:        &types.APIClients{UserManagementService: uc, StudyService: sc},
		tokenUsers: tokenUsers,
		consumed:   consumed,
	}
}

func noChannelTemplate(messageType string, studyKey string, withWhatsApp bool) types.EmailTemplate {
	tpl := types.EmailTemplate{
		MessageType:     messageType,
		StudyKey:        studyKey,
		DefaultLanguage: "it",
		Translations: []types.LocalizedTemplate{
			{Lang: "it", Subject: "subject", TemplateDef: base64.StdEncoding.EncodeToString([]byte("body"))},
		},
	}
	if withWhatsApp {
		tpl.WhatsAppTemplateName = fallbackWATemplate
	}
	return tpl
}

// noChannelLogs captures every level a per-user warning could go to, plus the info line
// with the counters.
type noChannelLogs struct {
	info    *bytes.Buffer
	warning *bytes.Buffer
	err     *bytes.Buffer
}

func captureNoChannelLogs(t *testing.T) *noChannelLogs {
	t.Helper()
	l := &noChannelLogs{info: &bytes.Buffer{}, warning: &bytes.Buffer{}, err: &bytes.Buffer{}}
	logger.Info.SetOutput(l.info)
	logger.Warning.SetOutput(l.warning)
	logger.Error.SetOutput(l.err)
	t.Cleanup(func() {
		logger.Info.SetOutput(os.Stdout)
		logger.Warning.SetOutput(os.Stderr)
		logger.Error.SetOutput(os.Stderr)
	})
	return l
}

var noChannelLineRe = regexp.MustCompile(`Skipped, no notification channel: (\d+)\.`)

// summary returns total, failed and skipped-for-no-channel as the generator logged them.
func (l *noChannelLogs) summary(t *testing.T) (total, failed, noChannel int) {
	t.Helper()
	info := l.info.String()
	g := counterLineRe.FindStringSubmatch(info)
	n := noChannelLineRe.FindStringSubmatch(info)
	if g == nil || n == nil {
		t.Fatalf("no summary line with both counters in the info log:\n%s", info)
	}
	total, _ = strconv.Atoi(g[1])
	failed, _ = strconv.Atoi(g[2])
	noChannel, _ = strconv.Atoi(n[1])
	return total, failed, noChannel
}

type noChannelGenerator struct {
	name string
	run  func(t *testing.T, clients noChannelClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool)
	// participantMsgType is the type GetParticipantMessages returns (participant messages only).
	participantMsgType string
	// tokens is how many tokens each served participant needs: the weekly reminder asks for a
	// login and an unsubscribe token, the newsletter for an unsubscribe token, the others for a
	// login token.
	tokens int
}

var noChannelGenerators = []noChannelGenerator{
	{
		name:   "weekly reminder",
		tokens: 2,
		run: func(t *testing.T, clients noChannelClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			GenerateForAllUsers(clients.api, db, instanceID, noChannelTemplate(constants.EMAIL_TYPE_WEEKLY, "", withWhatsApp), true, "no channel test")
		},
	},
	{
		name:   "newsletter",
		tokens: 1,
		run: func(t *testing.T, clients noChannelClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			GenerateForAllUsers(clients.api, db, instanceID, noChannelTemplate(constants.EMAIL_TYPE_NEWSLETTER, "", withWhatsApp), true, "no channel test")
		},
	},
	{
		name:   "study message",
		tokens: 1,
		run: func(t *testing.T, clients noChannelClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			GenerateForStudyParticipants(clients.api, db, instanceID, noChannelTemplate(constants.EMAIL_TYPE_STUDY_REMINDER, fallbackStudy, withWhatsApp), nil, true, "no channel test")
		},
	},
	{
		name:               "participant message",
		participantMsgType: fallbackMsgType,
		tokens:             1,
		run: func(t *testing.T, clients noChannelClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
				Collection("email-templates").InsertOne(context.Background(), noChannelTemplate(fallbackMsgType, fallbackStudy, withWhatsApp)); err != nil {
				t.Fatalf("insert template: %v", err)
			}
			var wg sync.WaitGroup
			wg.Add(1)
			GenerateParticipantMessages(clients.api, db, instanceID, "no channel test", &wg)
			wg.Wait()
		},
	},
}

// The three ways WhatsApp can be out of reach for a WhatsApp-only participant. In the last
// two the e-mail fallback serves them, and must still skip participants without a channel.
var noChannelScenarios = []struct {
	name            string
	whatsAppEnabled bool
	withWATemplate  bool
}{
	{name: "whatsapp available", whatsAppEnabled: true, withWATemplate: true},
	{name: "no whatsapp template", whatsAppEnabled: true, withWATemplate: false},
	{name: "whatsapp switched off", whatsAppEnabled: false, withWATemplate: true},
}

func TestParticipantWithoutChannelGetsNothing(t *testing.T) {
	for _, g := range noChannelGenerators {
		for _, sc := range noChannelScenarios {
			g, sc := g, sc
			t.Run(g.name+"/"+sc.name, func(t *testing.T) {
				configureWhatsAppForTest(t, sc.whatsAppEnabled)
				db, instanceID, cleanup := fallbackDBService(t)
				defer cleanup()
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				clients := newNoChannelClients(t, ctrl, g.participantMsgType)
				logs := captureNoChannelLogs(t)

				g.run(t, clients, db, instanceID, sc.withWATemplate)

				rows := countQueues(t, db, instanceID)
				t.Logf("emails=%v whatsapp=%v tokens=%v consumed=%v", rows.emails, rows.whatsapp, *clients.tokenUsers, *clients.consumed)

				whatsAppQueued := sc.whatsAppEnabled && sc.withWATemplate
				for _, c := range noChannelUsers {
					wantEmail, wantWA := 0, 0
					switch {
					case c.silent:
					case c.id == "emailonly":
						wantEmail = 1
					case c.id == "waonly" && whatsAppQueued:
						wantWA = 1
					case c.id == "waonly":
						wantEmail = 1 // the fallback the silent participants must not get
					case c.id == "both" && whatsAppQueued:
						wantEmail, wantWA = 1, 1
					case c.id == "both":
						wantEmail = 1
					}
					if rows.emails[c.id] != wantEmail || rows.whatsapp[c.id] != wantWA {
						t.Errorf("%s: expected %d e-mail and %d WhatsApp, got %d / %d",
							c.id, wantEmail, wantWA, rows.emails[c.id], rows.whatsapp[c.id])
					}
				}

				silent := map[string]bool{}
				for _, c := range noChannelUsers {
					silent[c.id] = c.silent
				}
				servedTokens := 0
				for _, id := range *clients.tokenUsers {
					if silent[id] {
						t.Errorf("a token was requested for %s, who has no channel", id)
					} else {
						servedTokens++
					}
				}
				// The served participants' tokens are all there, so the check above is not
				// vacuous.
				if want := g.tokens * (len(noChannelUsers) - silentNoChannelUsers()); servedTokens != want {
					t.Errorf("expected %d token(s) per served participant (%d), got %d",
						g.tokens, want, servedTokens)
				}

				total, failed, noChannel := logs.summary(t)
				if total != len(noChannelUsers)-silentNoChannelUsers() || failed != 0 {
					t.Errorf("expected total=%d failed=0, got total=%d failed=%d",
						len(noChannelUsers)-silentNoChannelUsers(), total, failed)
				}
				if noChannel != silentNoChannelUsers() {
					t.Errorf("expected %d skipped for no channel, got %d", silentNoChannelUsers(), noChannel)
				}

				// Skipping is counted, never logged per participant.
				for _, c := range noChannelUsers {
					if !c.silent {
						continue
					}
					for level, out := range map[string]string{"warning": logs.warning.String(), "error": logs.err.String()} {
						if strings.Contains(out, c.id) {
							t.Errorf("%s log mentions %s:\n%s", level, c.id, out)
						}
					}
				}

				// A participant message is consumed for everyone, silent participants
				// included, so it is not fetched again on every run.
				if g.participantMsgType != "" {
					want := map[string]bool{}
					for _, c := range noChannelUsers {
						want["p_"+c.id+":m1"] = true
					}
					got := map[string]bool{}
					for _, d := range *clients.consumed {
						got[d] = true
					}
					if len(*clients.consumed) != len(want) || len(got) != len(want) {
						t.Errorf("expected each participant's message consumed once, got %v", *clients.consumed)
					}
					for k := range want {
						if !got[k] {
							t.Errorf("message not consumed: %s", k)
						}
					}
				}
			})
		}
	}
}

// Researcher notifications go to addresses, not to participants: the recipient has no channel
// list at all, and the notification must still be sent.
func TestResearcherNotificationIgnoresChannels(t *testing.T) {
	configureWhatsAppForTest(t, true)
	db, instanceID, cleanup := fallbackDBService(t)
	defer cleanup()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logs := captureNoChannelLogs(t)

	if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
		Collection("email-templates").InsertOne(context.Background(), noChannelTemplate(fallbackMsgType, fallbackStudy, true)); err != nil {
		t.Fatalf("insert template: %v", err)
	}

	sc := studyMock.NewMockStudyServiceApiClient(ctrl)
	sc.EXPECT().GetResearcherMessages(gomock.Any(), gomock.Any()).
		Return(&studyAPI.StudyMessages{Messages: []*studyAPI.StudyMessage{
			{Id: "r1", Type: fallbackMsgType, StudyKey: fallbackStudy, ParticipantId: "pid", SendTo: []string{"researcher@fallback.test"}},
		}}, nil)
	sc.EXPECT().DeleteResearcherMessages(gomock.Any(), gomock.Any()).Return(&studyAPI.ServiceStatus{}, nil)
	clients := &types.APIClients{UserManagementService: userMock.NewMockUserManagementApiClient(ctrl), StudyService: sc}

	var wg sync.WaitGroup
	wg.Add(1)
	GenerateResearcherNotificationMessages(clients, db, instanceID, "no channel test", &wg)
	wg.Wait()

	rows := countQueues(t, db, instanceID)
	if rows.emails["researcher"] != 1 || rows.emailsN != 1 || rows.waN != 0 {
		t.Errorf("expected exactly 1 e-mail to the researcher and no WhatsApp, got emails=%v whatsapp=%v", rows.emails, rows.whatsapp)
	}
	if !strings.Contains(logs.info.String(), "Generated 1 (0 failed)") {
		t.Errorf("expected one generated notification in the summary, got:\n%s", logs.info.String())
	}
}
