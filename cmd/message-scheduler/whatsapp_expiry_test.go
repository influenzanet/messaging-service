package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// testMaxAge is the max age the scheduler tests below run with. The messages they queue are
// a minute on either side of it, so that the few seconds a tick takes cannot move one across.
const testMaxAge int64 = 3600

// addAged queues a message for phone that was added ageSeconds ago.
func (db *schedulerTestDB) addAged(t *testing.T, phone string, ageSeconds int64, sendAttempt int) types.OutgoingWhatsApp {
	t.Helper()
	msg, err := db.service.AddToOutgoingWhatsApp(schedulerTestInstanceID, types.OutgoingWhatsApp{
		MessageType: "test", ToPhoneNumber: phone, TemplateName: "test_template", Lang: "it",
		ContentParams: map[string]string{"name": "Mario"}, UserID: "user-" + phone,
		AddedAt: time.Now().Unix() - ageSeconds, SendAttempt: sendAttempt,
	})
	if err != nil {
		t.Fatalf("add outgoing WhatsApp message: %v", err)
	}
	return msg
}

// queuedFor counts the queued messages to phone.
func (db *schedulerTestDB) queuedFor(t *testing.T, phone string) int64 {
	t.Helper()
	return db.countQueued(t, bson.M{"toPhoneNumber": phone})
}

// archivedRowsFor counts the archive rows of phone.
func (db *schedulerTestDB) archivedRowsFor(t *testing.T, phone string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := db.sent.CountDocuments(ctx, bson.M{"toPhoneNumber": types.MaskPhone(phone)})
	if err != nil {
		t.Fatalf("count the archive rows of %s: %v", phone, err)
	}
	return n
}

func runWhatsAppHandlerWithMaxAge(db *schedulerTestDB, sender whatsAppSender, maxAge int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, whatsAppAlwaysOn, 60, defaultWhatsAppTemplateRetryDelay, maxAge, &wg)
	wg.Wait()
}

// A message exactly max age old may still go; one second more and it may not.
// Fails if the comparison is turned into >=, or measures the age the wrong way round.
func TestWhatsAppMessageExpiredAtTheBoundary(t *testing.T) {
	const addedAt, maxAge = 1_000_000, 3600
	for _, tt := range []struct {
		now  int64
		want bool
	}{
		{addedAt, false},
		{addedAt + maxAge - 1, false},
		{addedAt + maxAge, false},
		{addedAt + maxAge + 1, true},
		{addedAt + 10*maxAge, true},
	} {
		if got := whatsAppMessageExpired(addedAt, tt.now, maxAge); got != tt.want {
			t.Fatalf("whatsAppMessageExpired(added %d, now %d, max age %d) = %v, want %v", addedAt, tt.now, maxAge, got, tt.want)
		}
	}
}

// Unset means one day, the decided default; anything set must be a positive number of seconds,
// so a typo cannot silently turn the expiry off or expire every message.
func TestParseWhatsAppMaxAge(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 86400, false},
		{"1", 1, false},
		{"172800", 172800, false},
		{"0", 0, true},
		{"-86400", 0, true},
		{"24h", 0, true},
		{"86400.5", 0, true},
	} {
		got, err := parseWhatsAppMaxAge(tt.in)
		if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
			t.Fatalf("parseWhatsAppMaxAge(%q) = %d, %v; want %d, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

// The max age has to outlast every try of a refused template: five tries, retry delay apart.
// The deployment values (one day, three hours) and the defaults pass; a max age at or below
// the deferral window is reported, naming both settings an operator can change.
// Fails if the check compares against fewer tries than the cap, or lets the boundary through.
func TestWhatsAppMaxAgeProblem(t *testing.T) {
	for _, tt := range []struct {
		maxAge, retryDelay int64
		wantProblem        bool
	}{
		{86400, 10800, false},
		{defaultWhatsAppMaxAge, defaultWhatsAppTemplateRetryDelay, false},
		{int64(maxWhatsAppSendAttempts)*10800 + 1, 10800, false},
		{int64(maxWhatsAppSendAttempts) * 10800, 10800, true},
		{int64(maxWhatsAppSendAttempts-1)*10800 + 1, 10800, true},
		{3600, 3600, true},
	} {
		problem := whatsAppMaxAgeProblem(tt.maxAge, tt.retryDelay)
		if (problem != "") != tt.wantProblem {
			t.Fatalf("whatsAppMaxAgeProblem(%d, %d) = %q, want a problem: %v", tt.maxAge, tt.retryDelay, problem, tt.wantProblem)
		}
		if tt.wantProblem && (!strings.Contains(problem, "MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE") || !strings.Contains(problem, "MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY")) {
			t.Fatalf("the problem does not name both settings: %q", problem)
		}
	}
}

// The runner says once, at startup, that the max age cuts refused templates short, and says
// nothing about it when the max age is long enough.
// Fails if the runner stops checking, or checks against the wrong values.
func TestOutgoingWhatsAppRunnerWarnsWhenTheMaxAgeIsShorterThanTheTemplateDeferral(t *testing.T) {
	gdb := newSchedulerTestGlobalDB(t)
	db := newSchedulerTestDB(t)
	client := waClient.NewWhatsAppClient("test-token", "test-phone-id", "")
	warnings := captureWarningLog(t)
	infos := captureLog(t, logger.Info)

	for _, tt := range []struct {
		name        string
		maxAge      int64
		wantWarning bool
	}{
		{"long enough", defaultWhatsAppMaxAge, false},
		{"too short", 2 * defaultWhatsAppTemplateRetryDelay, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ticksBefore := strings.Count(infos.String(), "fetching and sending outgoing whatsapp messages...")
			// A period of one hour: the runner starts one tick and then sleeps past the test.
			go runnerForOutgoingWhatsApp(db.service, gdb, client, 3600, true, defaultWhatsAppTemplateRetryDelay, tt.maxAge, testSchedulerWhatsAppSettings(true))

			// The warning is written before the first tick starts, so once the tick has
			// started the warning is either there or not coming.
			deadline := time.Now().Add(5 * time.Second)
			for strings.Count(infos.String(), "fetching and sending outgoing whatsapp messages...") == ticksBefore {
				if time.Now().After(deadline) {
					t.Fatal("the runner did not start its first tick")
				}
				time.Sleep(10 * time.Millisecond)
			}
			warned := strings.Contains(warnings.String(), "MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE")
			if warned != tt.wantWarning {
				t.Fatalf("max age %d s with retry delay %d s: warned %v, want %v; warnings: %s", tt.maxAge, defaultWhatsAppTemplateRetryDelay, warned, tt.wantWarning, warnings.String())
			}
		})
	}
}

// A message that waited longer than the max age is archived as expired and never reaches
// Meta, while a message just inside the max age goes out as usual. The expired row records
// no error, keeps its attempt count, and like every archive row holds neither the number nor
// the content parameters. The tick reports how many it expired.
// Fails if the age check is skipped or comes after the send, if expired messages are archived
// with another status, or if the check catches messages that are still young enough.
func TestWhatsAppMessageOlderThanMaxAgeIsArchivedAsExpiredWithoutSending(t *testing.T) {
	const stale, fresh = "+391111111101", "+391111111102"
	db := newSchedulerTestDB(t)
	db.addAged(t, stale, testMaxAge+60, 2)
	db.addAged(t, fresh, testMaxAge-60, 0)
	sender := &perPhoneWhatsAppSender{}
	errorsLog := captureErrorLog(t)
	before := time.Now().Unix()

	runWhatsAppHandlerWithMaxAge(db, sender, testMaxAge)

	if calls := sender.calls[stale]; calls != 0 {
		t.Fatalf("the expired message was sent to Meta %d time(s)", calls)
	}
	if calls := sender.calls[fresh]; calls != 1 {
		t.Fatalf("the message inside the max age was sent %d time(s), want 1", calls)
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 2 {
		t.Fatalf("both messages must leave the queue with one archive row each: outgoing=%d sent=%d", outgoing, sent)
	}

	row := db.archivedFor(t, stale)
	if row.Status != types.WhatsAppStatusExpired {
		t.Fatalf("archived status = %q, want %q", row.Status, types.WhatsAppStatusExpired)
	}
	if row.ErrorCode != 0 || row.ErrorClass != "" {
		t.Fatalf("an expired message was archived with an error: code %d, class %q", row.ErrorCode, row.ErrorClass)
	}
	if row.SendAttempt != 2 {
		t.Fatalf("archived sendAttempt = %d, want the 2 attempts it had", row.SendAttempt)
	}
	if row.SentAt < before {
		t.Fatalf("archived sentAt = %d, want the archive time (>= %d)", row.SentAt, before)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var raw bson.M
	if err := db.sent.FindOne(ctx, bson.M{"status": types.WhatsAppStatusExpired}).Decode(&raw); err != nil {
		t.Fatalf("read the expired archive row: %v", err)
	}
	if _, has := raw["contentParams"]; has {
		t.Fatalf("the expired archive row keeps the content parameters: %v", raw)
	}
	if got := db.archivedFor(t, fresh).Status; got != types.WhatsAppStatusDelivered {
		t.Fatalf("the message inside the max age was archived as %q, want %q", got, types.WhatsAppStatusDelivered)
	}

	if !strings.Contains(errorsLog.String(), "WA_ALERT expired instance=i count=1 ") {
		t.Fatalf("the tick did not report the expired message; errors: %s", errorsLog.String())
	}
}

// sendHookWhatsAppSender runs onSend before passing each send to the sender it wraps, once per
// recipient, so that a test can change the queue while a tick is under way.
type sendHookWhatsAppSender struct {
	whatsAppSender
	onSend func(to string)
}

func (s *sendHookWhatsAppSender) SendTemplateMessage(ctx context.Context, to, template, lang string, params map[string]string) (string, error) {
	s.onSend(to)
	return s.whatsAppSender.SendTemplateMessage(ctx, to, template, lang, params)
}

// Nothing reaches Meta when an expired message is dropped, so it must not decide a transient
// failure held before it: the held message stays undecided and is not charged. The queue hands
// out the oldest messages first, so an expired message comes after a held one only when it
// becomes free during the tick, as when another tick's claim on it runs out; the sender queues
// it while the held message is being sent, and the next fetch of the tick picks it up.
// Fails if expiring a message is treated like an answer from Meta.
func TestWhatsAppExpiredMessageDoesNotDecideAHeldFailure(t *testing.T) {
	const stale = "+391111111101"
	db := newSchedulerTestDB(t)
	// Fails transiently and is held. On its last attempt, so a charge would archive it as
	// failed.
	db.addAged(t, poisonPhone, 0, maxWhatsAppSendAttempts-1)
	var once sync.Once
	sender := &sendHookWhatsAppSender{
		whatsAppSender: &perPhoneWhatsAppSender{script: map[string][]error{
			poisonPhone: {&waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}},
		}},
		onSend: func(to string) {
			if to == poisonPhone {
				once.Do(func() { db.addAged(t, stale, testMaxAge+60, 0) })
			}
		},
	}

	runWhatsAppHandlerWithMaxAge(db, sender, testMaxAge)

	if got := db.archivedFor(t, stale).Status; got != types.WhatsAppStatusExpired {
		t.Fatalf("the stale message was archived as %q, want %q", got, types.WhatsAppStatusExpired)
	}
	if queued := db.queuedFor(t, poisonPhone); queued != 1 {
		t.Fatalf("the held message left the queue: %d rows queued, want 1", queued)
	}
	if attempts := db.attemptsOf(t, poisonPhone); attempts != maxWhatsAppSendAttempts-1 {
		t.Fatalf("the held message was charged by the expiry behind it: %d attempts, want %d", attempts, maxWhatsAppSendAttempts-1)
	}
	if archived := db.archivedRowsFor(t, poisonPhone); archived != 0 {
		t.Fatalf("the held message was archived: %d rows", archived)
	}
}

// A message Meta has already accepted is finished as delivered whatever its age: calling it
// expired would misreport a message the participant did receive.
// Fails if the age check runs before the delivered marker is honoured.
func TestWhatsAppDeliveredMessageOlderThanMaxAgeIsArchivedAsDelivered(t *testing.T) {
	const phone = "+391111111101"
	db := newSchedulerTestDB(t)
	msg := db.addAged(t, phone, testMaxAge+60, 0)
	if err := db.service.MarkOutgoingWhatsAppDelivered(schedulerTestInstanceID, msg.ID.Hex(), ""); err != nil {
		t.Fatalf("mark the message delivered: %v", err)
	}
	sender := &perPhoneWhatsAppSender{}

	runWhatsAppHandlerWithMaxAge(db, sender, testMaxAge)

	if calls := sender.calls[phone]; calls != 0 {
		t.Fatalf("a delivered message was sent again %d time(s)", calls)
	}
	if got := db.archivedFor(t, phone).Status; got != types.WhatsAppStatusDelivered {
		t.Fatalf("a delivered message older than the max age was archived as %q, want %q", got, types.WhatsAppStatusDelivered)
	}
	if queued := db.queuedFor(t, phone); queued != 0 {
		t.Fatalf("the delivered message is still queued: %d rows", queued)
	}
}

// An archive that refuses the write does not bring an expired message back: it is out of the
// queue and never sent, the failure is logged, and the tick goes on with the next message.
// Fails if the expiry archives before it deletes and keeps the row queued on failure, or if
// the failure stops the tick.
func TestWhatsAppExpiredMessageIsNotSentWhenItsArchiveWriteFails(t *testing.T) {
	const stale, fresh = "+391111111101", "+391111111102"
	db := newSchedulerTestDB(t)
	db.addAged(t, stale, testMaxAge+60, 0)
	db.addAged(t, fresh, 0, 0)
	db.blockArchive(t, stale)
	sender := &perPhoneWhatsAppSender{}
	errorsLog := captureErrorLog(t)

	runWhatsAppHandlerWithMaxAge(db, sender, testMaxAge)
	// A second tick finds nothing of it either.
	db.expireLocks(t)
	runWhatsAppHandlerWithMaxAge(db, sender, testMaxAge)

	if calls := sender.calls[stale]; calls != 0 {
		t.Fatalf("the expired message was sent %d time(s) after its archive write failed", calls)
	}
	if queued := db.queuedFor(t, stale); queued != 0 {
		t.Fatalf("the expired message is still queued after its archive write failed: %d rows", queued)
	}
	if !strings.Contains(errorsLog.String(), "Error archiving expired whatsapp ('test')") {
		t.Fatalf("the failed archive write was not logged; errors: %s", errorsLog.String())
	}
	if calls := sender.calls[fresh]; calls != 1 {
		t.Fatalf("the message behind the failed archive write was sent %d time(s), want 1", calls)
	}
}

// A message the participant's cancellation took out of the queue between the fetch and the
// expiry is left to the cancellation: one archive row, saying cancelled, not two.
// Fails if the expiry archives a message it did not take out of the queue itself.
func TestWhatsAppExpiryLeavesAMessageCancelledMeanwhileToTheCancellation(t *testing.T) {
	const phone = "+391111111101"
	db := newSchedulerTestDB(t)
	msg := db.addAged(t, phone, testMaxAge+60, 0)
	if n, err := db.service.CancelOutgoingWhatsAppForUser(schedulerTestInstanceID, msg.UserID, nil); err != nil || n != 1 {
		t.Fatalf("cancel the participant's messages: %d, %v", n, err)
	}

	if expireWhatsApp(db.service, schedulerTestInstanceID, msg) {
		t.Fatal("the expiry reports a message it did not take out of the queue")
	}

	if archived := db.archivedRowsFor(t, phone); archived != 1 {
		t.Fatalf("the message has %d archive rows, want 1", archived)
	}
	if got := db.archivedFor(t, phone).Status; got != types.WhatsAppStatusCancelled {
		t.Fatalf("archived status = %q, want %q", got, types.WhatsAppStatusCancelled)
	}
}

// When the queue delete itself fails, the message stays queued under the tick's claim and is
// not archived: a later tick expires it. Archiving it anyway would leave a queued copy that a
// later tick archives a second time. The failing delete comes from a database user that may
// read and write the queue and the archive, but not delete.
// Fails if the expiry archives without having removed the queued row.
func TestWhatsAppExpiryKeepsTheMessageQueuedWhenTheDeleteFails(t *testing.T) {
	const phone = "+391111111101"
	db := newSchedulerTestDB(t)
	msg := db.addAged(t, phone, testMaxAge+60, 0)
	noDelete := db.serviceThatCannotDelete(t)
	errorsLog := captureErrorLog(t)

	if expireWhatsApp(noDelete, schedulerTestInstanceID, msg) {
		t.Fatal("the expiry reports a message it could not take out of the queue")
	}

	if queued := db.queuedFor(t, phone); queued != 1 {
		t.Fatalf("the message left the queue although the delete failed: %d rows", queued)
	}
	if archived := db.archivedRowsFor(t, phone); archived != 0 {
		t.Fatalf("the message was archived although the delete failed: %d rows", archived)
	}
	if !strings.Contains(errorsLog.String(), "Error removing expired whatsapp ('test')") {
		t.Fatalf("the failed delete was not logged; errors: %s", errorsLog.String())
	}

	// A later tick, with a database that lets it delete, does expire it.
	runWhatsAppHandlerWithMaxAge(db, &perPhoneWhatsAppSender{}, testMaxAge)
	if got := db.archivedFor(t, phone).Status; got != types.WhatsAppStatusExpired {
		t.Fatalf("archived status = %q, want %q", got, types.WhatsAppStatusExpired)
	}
}

// serviceThatCannotDelete opens a second database service on the same data, logged in as a
// user that may find, insert and update in this test's message database but not delete: a
// delete fails there while every other write succeeds.
func (db *schedulerTestDB) serviceThatCannotDelete(t *testing.T) *messagedb.MessageDBService {
	t.Helper()
	return db.serviceWithPrivileges(t, "no_delete", bson.A{bson.M{
		"resource": bson.M{"db": db.database.Name(), "collection": ""},
		"actions":  bson.A{"find", "insert", "update"},
	}})
}

// serviceWithPrivileges opens a second database service on the same data, logged in as a user
// that holds only the given privileges. kind tells the users of one test apart.
func (db *schedulerTestDB) serviceWithPrivileges(t *testing.T, kind string, privileges bson.A) *messagedb.MessageDBService {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin := db.service.DBClient.Database("admin")
	name := "scheduler_" + kind + "_" + strings.ReplaceAll(db.prefix, "-", "")
	const password = "test-password"
	if err := admin.RunCommand(ctx, bson.D{
		{Key: "createRole", Value: name},
		{Key: "privileges", Value: privileges},
		{Key: "roles", Value: bson.A{}},
	}).Err(); err != nil {
		t.Fatalf("create the role %s: %v", name, err)
	}
	if err := admin.RunCommand(ctx, bson.D{
		{Key: "createUser", Value: name},
		{Key: "pwd", Value: password},
		{Key: "roles", Value: bson.A{name}},
	}).Err(); err != nil {
		t.Fatalf("create the user %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := admin.RunCommand(ctx, bson.D{{Key: "dropUser", Value: name}}).Err(); err != nil {
			t.Errorf("drop the user %s: %v", name, err)
		}
		if err := admin.RunCommand(ctx, bson.D{{Key: "dropRole", Value: name}}).Err(); err != nil {
			t.Errorf("drop the role %s: %v", name, err)
		}
	})

	parsed, err := url.Parse(os.Getenv("F04_TEST_MONGODB_URI"))
	if err != nil {
		t.Fatalf("parse F04_TEST_MONGODB_URI: %v", err)
	}
	parsed.User = url.UserPassword(name, password)
	query := parsed.Query()
	query.Set("authSource", "admin")
	parsed.RawQuery = query.Encode()
	service := messagedb.NewMessageDBService(types.DBConfig{
		URI:             parsed.String(),
		Timeout:         30,
		IdleConnTimeout: 30,
		MaxPoolSize:     10,
		DBNamePrefix:    db.prefix,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.DBClient.Disconnect(ctx); err != nil {
			t.Errorf("disconnect the database service of %s: %v", name, err)
		}
	})
	return service
}

// The scheduler reads its max age from MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE, and falls back to
// one day when the variable is not set.
// Fails if initConfig reads another variable or drops the value on its way into the config.
func TestInitConfigReadsTheWhatsAppMaxAge(t *testing.T) {
	for name, value := range map[string]string{
		"MESSAGE_SCHEDULER_INTERVAL_HIGH_PRIO":               "60",
		"MESSAGE_SCHEDULER_INTERVAL_LOW_PRIO":                "60",
		"MESSAGE_SCHEDULER_INTERVAL_AUTO_MESSAGE":            "60",
		"MESSAGE_SCHEDULER_INTERVAL_PARTICIPANT_MESSAGE":     "60",
		"MESSAGE_SCHEDULER_INTERVAL_RESEARCHER_NOTIFICATION": "60",
		"MESSAGE_DB_CONNECTION_STR":                          "localhost:27017",
		"MESSAGE_DB_USERNAME":                                "user",
		"MESSAGE_DB_PASSWORD":                                "password",
		"GLOBAL_DB_CONNECTION_STR":                           "localhost:27017",
		"GLOBAL_DB_USERNAME":                                 "user",
		"GLOBAL_DB_PASSWORD":                                 "password",
		"DB_TIMEOUT":                                         "30",
		"DB_IDLE_CONN_TIMEOUT":                               "45",
		"DB_MAX_POOL_SIZE":                                   "8",
	} {
		t.Setenv(name, value)
	}

	t.Setenv("MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE", "7200")
	if got := initConfig().WhatsAppMaxAge; got != 7200 {
		t.Fatalf("max age with MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE=7200: %d s", got)
	}
	t.Setenv("MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE", "")
	if got := initConfig().WhatsAppMaxAge; got != defaultWhatsAppMaxAge {
		t.Fatalf("max age with MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE unset: %d s, want %d", got, defaultWhatsAppMaxAge)
	}
}

// The max age the runner is given is the one its ticks apply: a message two minutes old is
// expired by a runner configured for one minute, which the default of one day would send.
// Fails if the runner or the per-instance dispatch passes on another value than the one it got.
// Under such a fault the runner would try to send through a client with a dummy token; Meta
// refuses it, or the network does, and the message stays queued, so the test fails without
// anything being delivered.
func TestOutgoingWhatsAppRunnerExpiresWithTheMaxAgeItIsGiven(t *testing.T) {
	const phone = "+391111111101"
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	db := newSchedulerTestDB(t)
	db.addAged(t, phone, 120, 0)
	client := waClient.NewWhatsAppClient("test-token", "test-phone-id", "")

	// A period of one hour: the runner starts one tick and then sleeps past the test.
	go runnerForOutgoingWhatsApp(db.service, gdb, client, 3600, true, defaultWhatsAppTemplateRetryDelay, 60, testSchedulerWhatsAppSettings(true))

	deadline := time.Now().Add(10 * time.Second)
	for db.archivedRowsFor(t, phone) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the runner's tick did not expire a message older than the max age it was given")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := db.archivedFor(t, phone).Status; got != types.WhatsAppStatusExpired {
		t.Fatalf("archived status = %q, want %q", got, types.WhatsAppStatusExpired)
	}
}
