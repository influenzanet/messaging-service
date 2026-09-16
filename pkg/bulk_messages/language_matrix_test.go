package bulk_messages

// Language matrix: which language every WhatsApp send path of the messaging-service picks,
// compared with the language the e-mail path of the same generator picks for the same user and
// the same template. These tests document today's behaviour and change no production code.
//
// Two layers:
//   - the rule layer compares resolveWhatsAppLang (bulk_messages.go:616) with
//     templates.GetTemplateTranslation (pkg/templates/templates.go:19) without a database;
//   - the generator layer runs GenerateForAllUsers and GenerateForStudyParticipants against a
//     real MongoDB and reads the language back out of the two queues, which is what the
//     message-scheduler will later hand to Meta.
//
// The generator layer needs MESSAGE_DB_CONNECTION_STR and is skipped otherwise. The tests swap
// the package-level whatsAppEnabled flag, so they must not run in parallel.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/templates"
	"github.com/influenzanet/messaging-service/pkg/types"
	studyMock "github.com/influenzanet/messaging-service/test/mocks/study-service"
	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/mock/gomock"
)

const (
	lmMsgType    = "language-matrix-test" // no dedicated handling in isSubscribed, so every user is in
	lmWATemplate = "language_matrix_tpl"
	lmStudy      = "languagematrixstudy"
	lmDBPrefix   = "LANGMATRIX_"
	lmNone       = "(empty)" // label for the user without a preferred language
)

// matrixUserLangs are the participant languages under test: the four the platform may maintain,
// Romansh as a language no Meta locale covers, and the empty value of a fresh account.
var matrixUserLangs = []string{"it", "en", "fr", "de", "rm", ""}

// lmTemplateSet is one template as the operator would maintain it in the e-mail templates
// collection: a set of translations plus the default language.
type lmTemplateSet struct {
	label        string
	translations []string
	defaultLang  string
	extra        bool // beyond the requested matrix, kept because the two rules diverge there
}

var lmTemplateSets = []lmTemplateSet{
	{label: "A: it | default it", translations: []string{"it"}, defaultLang: "it"},
	{label: "B: it,en | default it", translations: []string{"it", "en"}, defaultLang: "it"},
	{label: "C: it,en,fr,de | default it", translations: []string{"it", "en", "fr", "de"}, defaultLang: "it"},
	{label: "B': it,en | default en", translations: []string{"it", "en"}, defaultLang: "en"},
	{label: "D: it | default en (default not translated)", translations: []string{"it"}, defaultLang: "en", extra: true},
	{label: "E: it,rm | default it", translations: []string{"it", "rm"}, defaultLang: "it", extra: true},
}

// lmTemplate builds the template. Every translation carries its own language in the subject and
// in the body, so the language the e-mail path actually used is readable from the queued e-mail.
func (s lmTemplateSet) template(studyKey string) types.EmailTemplate {
	translations := make([]types.LocalizedTemplate, 0, len(s.translations))
	for _, lang := range s.translations {
		translations = append(translations, types.LocalizedTemplate{
			Lang:        lang,
			Subject:     "subj-" + lang,
			TemplateDef: base64.StdEncoding.EncodeToString([]byte("body-" + lang)),
		})
	}
	return types.EmailTemplate{
		MessageType:          lmMsgType,
		StudyKey:             studyKey,
		DefaultLanguage:      s.defaultLang,
		WhatsAppTemplateName: lmWATemplate,
		Translations:         translations,
	}
}

// rowLabel marks the sets added beyond the requested matrix, so the tables stay readable.
func (s lmTemplateSet) rowLabel() string {
	if s.extra {
		return s.label + " *(extra)*"
	}
	return s.label
}

func lmLangLabel(lang string) string {
	if lang == "" {
		return lmNone
	}
	return lang
}

// lmUserID is the id and the local part of the address of the participant of a given language.
func lmUserID(lang string) string {
	if lang == "" {
		return "u-none"
	}
	return "u-" + lang
}

func lmNewUser(lang string) *umAPI.User {
	id := lmUserID(lang)
	return &umAPI.User{
		Id: id,
		Account: &umAPI.User_Account{
			Type:              "email",
			AccountId:         id + "@language-test.test",
			PreferredLanguage: lang,
		},
		Profiles: []*umAPI.Profile{{Id: "p_" + id, Alias: "alias"}},
		ContactInfos: []*umAPI.ContactInfo{
			{Type: "phone", Address: &umAPI.ContactInfo_Phone{Phone: "+3900" + id}, ConfirmedAt: 200},
		},
		// Both channels, so the same run exercises the WhatsApp queue and the e-mail queue.
		ContactPreferences: &umAPI.ContactPreferences{
			PreferredChannels:      []string{"email", "whatsapp"},
			SubscribedToWeekly:     true,
			SubscribedToNewsletter: true,
		},
	}
}

// ---------------------------------------------------------------------------
// rule layer: resolveWhatsAppLang vs GetTemplateTranslation, no database
// ---------------------------------------------------------------------------

// lmEmailLang is the language the e-mail path ends up writing: the language of the translation
// GetTemplateTranslation returns. An empty value means no translation was found at all, which
// leaves the e-mail without a body and makes prepareOutgoingEmail fail.
func lmEmailLang(tpl types.EmailTemplate, userLang string) string {
	return templates.GetTemplateTranslation(tpl, userLang).Lang
}

func TestLanguageRuleMatrix(t *testing.T) {
	rows := make([]string, 0, len(lmTemplateSets))
	for _, set := range lmTemplateSets {
		tpl := set.template("")
		cells := make([]string, 0, len(matrixUserLangs))
		for _, userLang := range matrixUserLangs {
			user := &umAPI.User{Account: &umAPI.User_Account{PreferredLanguage: userLang}}
			wa := resolveWhatsAppLang(user, tpl)
			email := lmEmailLang(tpl, userLang)
			cells = append(cells, fmt.Sprintf("%s / %s", lmCell(wa), lmCell(email)))

			// The rule the code states: the participant's language when the template is
			// maintained in it, the template default otherwise.
			maintained := false
			for _, l := range set.translations {
				if l == userLang && userLang != "" {
					maintained = true
				}
			}
			want := set.defaultLang
			if maintained {
				want = userLang
			}
			if wa != want {
				t.Errorf("%s / user %q: WhatsApp language is %q, the documented rule gives %q",
					set.label, lmLangLabel(userLang), wa, want)
			}
		}
		rows = append(rows, "| "+set.rowLabel()+" | "+strings.Join(cells, " | ")+" |")
	}
	t.Logf("rule layer, cell = WhatsApp / e-mail\n%s", lmMarkdownTable(rows))
}

func lmCell(lang string) string {
	if lang == "" {
		return "none"
	}
	return lang
}

func lmMarkdownTable(rows []string) string {
	header := "| template translations | " + strings.Join(lmHeaderCells(), " | ") + " |"
	sep := "|---|" + strings.Repeat("---|", len(matrixUserLangs))
	return strings.Join(append([]string{header, sep}, rows...), "\n")
}

func lmHeaderCells() []string {
	out := make([]string, 0, len(matrixUserLangs))
	for _, l := range matrixUserLangs {
		out = append(out, "user "+lmLangLabel(l))
	}
	return out
}

// ---------------------------------------------------------------------------
// generator layer: what actually lands in the two queues
// ---------------------------------------------------------------------------

func lmDBService(t *testing.T) (*messagedb.MessageDBService, string, func()) {
	t.Helper()
	connStr := os.Getenv("MESSAGE_DB_CONNECTION_STR")
	if connStr == "" {
		t.Skip("MESSAGE_DB_CONNECTION_STR not set")
	}
	db := messagedb.NewMessageDBService(types.DBConfig{
		URI:             "mongodb://" + os.Getenv("MESSAGE_DB_USERNAME") + ":" + os.Getenv("MESSAGE_DB_PASSWORD") + "@" + connStr,
		DBNamePrefix:    lmDBPrefix,
		Timeout:         10,
		MaxPoolSize:     4,
		IdleConnTimeout: 10,
	})
	instanceID := "langmatrix-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	dbName := lmDBPrefix + instanceID + "_messageDB"
	return db, instanceID, func() {
		if err := db.DBClient.Database(dbName).Drop(context.Background()); err != nil {
			t.Errorf("cleanup of %s: %v", dbName, err)
		}
	}
}

// lmClients streams one user per language under test.
func lmClients(t *testing.T, ctrl *gomock.Controller) *types.APIClients {
	t.Helper()
	uc := userMock.NewMockUserManagementApiClient(ctrl)
	uc.EXPECT().StreamUsers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *umAPI.StreamUsersMsg, _ ...interface{}) (umAPI.UserManagementApi_StreamUsersClient, error) {
			stream := userMock.NewMockUserManagementApi_StreamUsersClient(ctrl)
			idx := 0
			stream.EXPECT().Recv().DoAndReturn(func() (*umAPI.User, error) {
				if idx >= len(matrixUserLangs) {
					return nil, io.EOF
				}
				u := lmNewUser(matrixUserLangs[idx])
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

	return &types.APIClients{UserManagementService: uc, StudyService: sc}
}

// lmQueued is what the generator left in the two queues for one user.
type lmQueued struct {
	waLang     string
	emailLang  string // read back from the subject the e-mail carries
	emailExtra string // the value of {{.language}} the body rendered, when asked for
}

// lmReadQueues maps user id -> what was queued for that user.
func lmReadQueues(t *testing.T, db *messagedb.MessageDBService, instanceID string) map[string]lmQueued {
	t.Helper()
	dbName := db.DBNamePrefix + instanceID + "_messageDB"
	out := map[string]lmQueued{}

	cur, err := db.DBClient.Database(dbName).Collection("outgoing-emails").Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("read outgoing-emails: %v", err)
	}
	var emails []types.OutgoingEmail
	if err := cur.All(context.Background(), &emails); err != nil {
		t.Fatalf("decode outgoing-emails: %v", err)
	}
	for _, e := range emails {
		for _, to := range e.To {
			id := strings.TrimSuffix(to, "@language-test.test")
			row := out[id]
			row.emailLang = strings.TrimPrefix(e.Subject, "subj-")
			row.emailExtra = strings.TrimPrefix(e.Content, "body-")
			out[id] = row
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
		row := out[w.UserID]
		row.waLang = w.Lang
		out[w.UserID] = row
	}
	return out
}

// lmRunGenerator runs one generator over one template set and returns the matrix row.
func lmRunGenerator(t *testing.T, generator string, set lmTemplateSet) string {
	t.Helper()
	db, instanceID, cleanup := lmDBService(t)
	defer cleanup()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := lmClients(t, ctrl)

	switch generator {
	case "GenerateForAllUsers":
		GenerateForAllUsers(clients, db, instanceID, set.template(""), true, "language matrix")
	case "GenerateForStudyParticipants":
		GenerateForStudyParticipants(clients, db, instanceID, set.template(lmStudy), nil, true, "language matrix")
	default:
		t.Fatalf("unknown generator %q", generator)
	}

	queued := lmReadQueues(t, db, instanceID)
	cells := make([]string, 0, len(matrixUserLangs))
	for _, userLang := range matrixUserLangs {
		row := queued[lmUserID(userLang)]
		cells = append(cells, fmt.Sprintf("%s / %s", lmCell(row.waLang), lmCell(row.emailLang)))

		// Whatever the languages are, the two queues must agree on whether the user was
		// served at all: an e-mail that cannot be built stops the WhatsApp queueing too,
		// because prepareOutgoingEmail runs first and its error skips the user.
		if (row.waLang == "") != (row.emailLang == "") {
			t.Logf("%s / %s / user %q: only one channel was queued (whatsapp=%q email=%q)",
				generator, set.label, lmLangLabel(userLang), row.waLang, row.emailLang)
		}
	}
	return "| " + set.rowLabel() + " | " + strings.Join(cells, " | ") + " |"
}

func TestLanguageMatrixGenerateForAllUsers(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()

	rows := make([]string, 0, len(lmTemplateSets))
	for _, set := range lmTemplateSets {
		rows = append(rows, lmRunGenerator(t, "GenerateForAllUsers", set))
	}
	t.Logf("GenerateForAllUsers, cell = WhatsApp queued / e-mail sent\n%s", lmMarkdownTable(rows))
}

func TestLanguageMatrixGenerateForStudyParticipants(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()

	rows := make([]string, 0, len(lmTemplateSets))
	for _, set := range lmTemplateSets {
		rows = append(rows, lmRunGenerator(t, "GenerateForStudyParticipants", set))
	}
	t.Logf("GenerateForStudyParticipants, cell = WhatsApp queued / e-mail sent\n%s", lmMarkdownTable(rows))
}

// TestEmailLanguageContentVariable records a divergence inside the e-mail path itself: the
// translation is chosen by fallback, but the {{.language}} the body can render is set from the
// raw participant language (bulk_messages.go:667), so an Italian body can announce itself as
// German. The WhatsApp path has no equivalent variable.
func TestEmailLanguageContentVariable(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()
	db, instanceID, cleanup := lmDBService(t)
	defer cleanup()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	clients := lmClients(t, ctrl)

	tpl := types.EmailTemplate{
		MessageType:          lmMsgType,
		DefaultLanguage:      "it",
		WhatsAppTemplateName: lmWATemplate,
		Translations: []types.LocalizedTemplate{
			{Lang: "it", Subject: "subj-it", TemplateDef: base64.StdEncoding.EncodeToString([]byte("body-{{.language}}"))},
		},
	}
	GenerateForAllUsers(clients, db, instanceID, tpl, true, "language variable")

	queued := lmReadQueues(t, db, instanceID)
	ids := make([]string, 0, len(queued))
	for id := range queued {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t.Logf("%s: whatsapp lang=%q, e-mail subject lang=%q, body {{.language}}=%q",
			id, queued[id].waLang, queued[id].emailLang, queued[id].emailExtra)
	}

	de := queued[lmUserID("de")]
	if de.emailLang != "it" || de.emailExtra != "de" {
		t.Errorf("expected the German participant to get the Italian body announcing 'de', got subject lang %q and {{.language}} %q",
			de.emailLang, de.emailExtra)
	}
	if de.waLang != "it" {
		t.Errorf("expected the WhatsApp message in the default 'it', got %q", de.waLang)
	}
}

// TestAutoMessageDelegatesWithoutOwnLanguageLogic closes point 2 of the matrix: a scheduled
// campaign reaches the queue through handleAutoMessages (cmd/message-scheduler/main.go:328) ->
// GenerateAutoMessages (bulk_messages.go:67), which only dispatches on AutoMessage.Type and
// copies the study key. It holds no language logic, so the languages it queues are identical to
// the ones the generators produce when called directly.
func TestAutoMessageDelegatesWithoutOwnLanguageLogic(t *testing.T) {
	whatsAppEnabled = true
	defer func() { whatsAppEnabled = false }()

	set := lmTemplateSets[1] // it,en with default it: the set where the languages differ per user
	cases := []struct {
		autoType  string
		studyKey  string
		generator string
	}{
		{autoType: "all-users", generator: "GenerateForAllUsers"},
		{autoType: "study-participants", studyKey: lmStudy, generator: "GenerateForStudyParticipants"},
	}

	for _, c := range cases {
		t.Run(c.autoType, func(t *testing.T) {
			db, instanceID, cleanup := lmDBService(t)
			defer cleanup()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			clients := lmClients(t, ctrl)

			autoMessage := types.AutoMessage{
				Type:     c.autoType,
				StudyKey: c.studyKey,
				Template: set.template(""), // the study key is copied onto the template by the dispatcher
				Label:    "language matrix auto message",
			}
			var wg sync.WaitGroup
			wg.Add(1)
			GenerateAutoMessages(clients, db, instanceID, autoMessage, true, autoMessage.Label, &wg)
			wg.Wait()

			queued := lmReadQueues(t, db, instanceID)
			got := map[string]string{}
			for _, userLang := range matrixUserLangs {
				got[lmLangLabel(userLang)] = queued[lmUserID(userLang)].waLang
			}
			t.Logf("auto message %q (delegates to %s) queued: %v", c.autoType, c.generator, got)

			want := map[string]string{"it": "it", "en": "en", "fr": "it", "de": "it", "rm": "it", lmNone: "it"}
			for lang, expected := range want {
				if got[lang] != expected {
					t.Errorf("participant %q: expected %q, got %q", lang, expected, got[lang])
				}
			}
		})
	}
}
