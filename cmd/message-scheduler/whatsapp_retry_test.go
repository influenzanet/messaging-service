package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const schedulerTestInstanceID = "i"

type fakeWhatsAppSender struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (s *fakeWhatsAppSender) SendTemplateMessage(context.Context, string, string, string, map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *fakeWhatsAppSender) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeWhatsAppSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type blockingWhatsAppSender struct {
	started chan struct{}
	release chan struct{}
	result  error
	once    sync.Once

	mu    sync.Mutex
	calls int
}

func (s *blockingWhatsAppSender) SendTemplateMessage(ctx context.Context, _ string, _ string, _ string, _ map[string]string) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })

	select {
	case <-s.release:
		return s.result
	case <-ctx.Done():
		return ctx.Err()
	}
}

type scriptedWhatsAppSender struct {
	mu     sync.Mutex
	errors []error
	calls  int
}

func (s *scriptedWhatsAppSender) SendTemplateMessage(context.Context, string, string, string, map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls
	s.calls++
	if call < len(s.errors) {
		return s.errors[call]
	}
	return nil
}

func (s *scriptedWhatsAppSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *blockingWhatsAppSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type schedulerTestDB struct {
	service  *messagedb.MessageDBService
	database *mongo.Database
	outgoing *mongo.Collection
	sent     *mongo.Collection
	// prefix names the isolated databases of this test, so that a second service can be
	// opened on the same data.
	prefix string
}

func newSchedulerTestDB(t *testing.T) *schedulerTestDB {
	t.Helper()
	uri := os.Getenv("F04_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("F04_TEST_MONGODB_URI is not configured")
	}

	prefix := "f04_scheduler_" + uuid.NewString() + "_"
	service := messagedb.NewMessageDBService(types.DBConfig{
		URI: uri,
		// The same value CI passes as DB_TIMEOUT. NewMessageDBService calls logger.Error.Fatal
		// when its ping does not answer inside this deadline, which kills the test binary
		// without naming a test, so a busy MongoDB must not be able to trip it. No test here
		// depends on the deadline: the database errors are injected as undecodable documents.
		Timeout:         30,
		IdleConnTimeout: 30,
		MaxPoolSize:     10,
		DBNamePrefix:    prefix,
	})
	database := service.DBClient.Database(prefix + schedulerTestInstanceID + "_messageDB")

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.Drop(ctx); err != nil {
			t.Errorf("drop isolated scheduler test database: %v", err)
		}
		if err := service.DBClient.Disconnect(ctx); err != nil {
			t.Errorf("disconnect scheduler test database: %v", err)
		}
	})

	return &schedulerTestDB{
		service:  service,
		database: database,
		outgoing: database.Collection("outgoing-whatsapp"),
		sent:     database.Collection("sent-whatsapp"),
		prefix:   prefix,
	}
}

func (db *schedulerTestDB) add(t *testing.T, count int, sendAttempt int) {
	t.Helper()
	for i := 0; i < count; i++ {
		_, err := db.service.AddToOutgoingWhatsApp(schedulerTestInstanceID, types.OutgoingWhatsApp{
			MessageType:     "test",
			ToPhoneNumber:   "+391234567890",
			TemplateName:    "test_template",
			Lang:            "it",
			ContentParams:   map[string]string{"name": "Mario"},
			LastSendAttempt: 0,
			SendAttempt:     sendAttempt,
		})
		if err != nil {
			t.Fatalf("add outgoing WhatsApp message: %v", err)
		}
	}
}

func (db *schedulerTestDB) expireLocks(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateMany(ctx, bson.M{}, bson.M{
		"$set": bson.M{"lastSendAttempt": time.Now().Add(-time.Hour).Unix()},
	}); err != nil {
		t.Fatalf("expire outgoing WhatsApp locks: %v", err)
	}
}

func (db *schedulerTestDB) counts(t *testing.T) (outgoing, sent int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	outgoing, err = db.outgoing.CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count outgoing WhatsApp messages: %v", err)
	}
	sent, err = db.sent.CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count sent WhatsApp messages: %v", err)
	}
	return outgoing, sent
}

func (db *schedulerTestDB) oneOutgoing(t *testing.T) types.OutgoingWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg types.OutgoingWhatsApp
	if err := db.outgoing.FindOne(ctx, bson.M{}).Decode(&msg); err != nil {
		t.Fatalf("read outgoing WhatsApp message: %v", err)
	}
	return msg
}

func runWhatsAppHandler(mdb *messagedb.MessageDBService, sender whatsAppSender, lockSeconds int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	handleOutgoingWhatsAppForInstance(mdb, schedulerTestInstanceID, sender, lockSeconds, defaultWhatsAppTemplateRetryDelay, &wg)
	wg.Wait()
}

func TestWhatsAppGlobalOutageDoesNotConsumeAttemptsAndRecovers(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, maxWhatsAppSendAttempts-1)

	sender := &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{
		StatusCode: http.StatusUnauthorized,
		Code:       190,
	}}
	for tick := 0; tick < maxWhatsAppSendAttempts+1; tick++ {
		runWhatsAppHandler(db.service, sender, 60)
		msg := db.oneOutgoing(t)
		if msg.SendAttempt != maxWhatsAppSendAttempts-1 {
			t.Fatalf("tick %d consumed an attempt during a global outage: got %d", tick+1, msg.SendAttempt)
		}
		db.expireLocks(t)
	}

	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("outage must leave message queued, got outgoing=%d sent=%d", outgoing, sent)
	}

	sender.setError(nil)
	runWhatsAppHandler(db.service, sender, 60)
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("message was not delivered after recovery and lease expiry: outgoing=%d sent=%d", outgoing, sent)
	}
}

func TestWhatsAppGlobalFailureStopsTheWholeTick(t *testing.T) {
	// Authentication and rate-limit conditions stop the tick at the first call (a refused
	// template does not: see whatsapp_template_test.go);
	// a transient failure is held once, so the tick stops at the second.
	for _, tt := range []struct {
		name  string
		err   error
		calls int
	}{
		{"HTTP 401", &waClient.WhatsAppSendError{StatusCode: http.StatusUnauthorized}, 1},
		{"HTTP 400 Meta 190", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 190}, 1},
		{"HTTP 403", &waClient.WhatsAppSendError{StatusCode: http.StatusForbidden}, 1},
		{"HTTP 400 Meta 33", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 33}, 1},
		{"HTTP 429", &waClient.WhatsAppSendError{StatusCode: http.StatusTooManyRequests}, 1},
		{"HTTP 400 Meta 130429", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 130429}, 1},
		{"HTTP 400 Meta 133010", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 133010}, 1},
		{"wrapped typed error", fmt.Errorf("send failed: %w", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 190}), 1},
		{"HTTP 500", &waClient.WhatsAppSendError{StatusCode: http.StatusInternalServerError}, 2},
		{"HTTP 400 Meta 131016", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131016}, 2},
		{"Meta transient flag", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 999, IsTransient: true}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.add(t, 2, 0)
			sender := &fakeWhatsAppSender{err: tt.err}

			runWhatsAppHandler(db.service, sender, 60)

			if sender.callCount() != tt.calls {
				t.Fatalf("global failure must open the per-tick circuit after %d call(s), got %d", tt.calls, sender.callCount())
			}
			if outgoing, sent := db.counts(t); outgoing != 2 || sent != 0 {
				t.Fatalf("global failure changed queue contents: outgoing=%d sent=%d", outgoing, sent)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if changed, err := db.outgoing.CountDocuments(ctx, bson.M{"sendAttempt": bson.M{"$ne": 0}}); err != nil {
				t.Fatalf("inspect retry counters: %v", err)
			} else if changed != 0 {
				t.Fatalf("global failure changed %d retry counters", changed)
			}
		})
	}
}

func TestWhatsAppGlobalFailureDoesNotFetchBeyondClaimedBatch(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, outgoingBatchSize+5, 0)
	sender := &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 2 {
		t.Fatalf("global failure must stop the outer fetch loop after two consecutive calls, got %d calls", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != outgoingBatchSize+5 || sent != 0 {
		t.Fatalf("global failure changed queue contents: outgoing=%d sent=%d", outgoing, sent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := db.outgoing.CountDocuments(ctx, bson.M{"lastSendAttempt": bson.M{"$gt": 0}})
	if err != nil {
		t.Fatalf("inspect claimed batch: %v", err)
	}
	if locked != outgoingBatchSize {
		t.Fatalf("claimed documents = %d, want one batch (%d); outer loop fetched again", locked, outgoingBatchSize)
	}
	if changed, err := db.outgoing.CountDocuments(ctx, bson.M{"sendAttempt": bson.M{"$ne": 0}}); err != nil {
		t.Fatalf("inspect retry counters: %v", err)
	} else if changed != 0 {
		t.Fatalf("global failure changed %d retry counters across claimed/unclaimed documents", changed)
	}
}

func TestWhatsAppPayloadFailureKeepsExistingFiveAttemptPolicy(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)
	sender := &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{
		StatusCode: http.StatusBadRequest,
		Code:       132012,
	}}

	runWhatsAppHandler(db.service, sender, 60)

	msg := db.oneOutgoing(t)
	if msg.SendAttempt != 1 {
		t.Fatalf("payload failure attempt = %d, want 1", msg.SendAttempt)
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("payload failure changed the existing retry policy: outgoing=%d sent=%d", outgoing, sent)
	}
}

func TestWhatsAppRecipientThrottleDoesNotStopTheTickOrConsumeAttempts(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, outgoingBatchSize+5, 0)
	pairThrottle := &waClient.WhatsAppSendError{
		StatusCode:  http.StatusTooManyRequests,
		Code:        131056,
		IsTransient: true,
	}
	sender := &scriptedWhatsAppSender{errors: []error{pairThrottle}}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != outgoingBatchSize+5 {
		t.Fatalf("recipient-scoped throttle stopped the whole tick: got %d calls, want %d", sender.callCount(), outgoingBatchSize+5)
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != outgoingBatchSize+4 {
		t.Fatalf("recipient throttle changed queue contents: outgoing=%d sent=%d", outgoing, sent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if changed, err := db.outgoing.CountDocuments(ctx, bson.M{"sendAttempt": bson.M{"$ne": 0}}); err != nil {
		t.Fatalf("inspect recipient-throttled retry counters: %v", err)
	} else if changed != 0 {
		t.Fatalf("recipient throttle changed %d retry counters", changed)
	}
}

func TestWhatsAppUnknownFailureKeepsExistingFiveAttemptPolicy(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, maxWhatsAppSendAttempts-2)
	sender := &fakeWhatsAppSender{err: errors.New("unclassified local failure")}

	runWhatsAppHandler(db.service, sender, 60)
	msg := db.oneOutgoing(t)
	if msg.SendAttempt != maxWhatsAppSendAttempts-1 {
		t.Fatalf("unknown failure attempt = %d, want %d", msg.SendAttempt, maxWhatsAppSendAttempts-1)
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("unknown failure archived too early: outgoing=%d sent=%d", outgoing, sent)
	}

	db.expireLocks(t)
	runWhatsAppHandler(db.service, sender, 60)
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("unknown failure did not retain the five-attempt cap: outgoing=%d sent=%d", outgoing, sent)
	}
}

func TestWhatsAppSuccessfulSendIsArchivedOnce(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 1 {
		t.Fatalf("successful message sent %d times, want 1", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("successful send not moved exactly once: outgoing=%d sent=%d", outgoing, sent)
	}
}

func TestConcurrentWhatsAppTicksRespectActiveLease(t *testing.T) {
	for _, tt := range []struct {
		name           string
		initialAttempt int
		result         error
		wantOutgoing   int64
		wantSent       int64
		wantAttempt    int
	}{
		{name: "success", wantSent: 1},
		{name: "unknown increments once", result: errors.New("local failure"), wantOutgoing: 1, wantAttempt: 1},
		{name: "unknown archives once at cap", initialAttempt: maxWhatsAppSendAttempts - 1, result: errors.New("local failure"), wantSent: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.add(t, 1, tt.initialAttempt)
			sender := &blockingWhatsAppSender{
				started: make(chan struct{}),
				release: make(chan struct{}),
				result:  tt.result,
			}
			released := false
			defer func() {
				if !released {
					close(sender.release)
				}
			}()

			firstDone := make(chan struct{})
			go func() {
				runWhatsAppHandler(db.service, sender, 60)
				close(firstDone)
			}()

			select {
			case <-sender.started:
			case <-time.After(5 * time.Second):
				t.Fatal("first tick did not claim and start sending the message")
			}

			// The first sender is still blocked, so the document is present with an active
			// lease. A racing tick must see no eligible document and return without sending.
			secondDone := make(chan struct{})
			go func() {
				runWhatsAppHandler(db.service, sender, 60)
				close(secondDone)
			}()
			select {
			case <-secondDone:
			case <-time.After(5 * time.Second):
				t.Fatal("second tick did not return while the document was leased")
			}
			if sender.callCount() != 1 {
				t.Fatalf("racing tick sent the leased message again: got %d calls", sender.callCount())
			}

			close(sender.release)
			released = true
			select {
			case <-firstDone:
			case <-time.After(5 * time.Second):
				t.Fatal("first tick did not finish after sender release")
			}

			if outgoing, sent := db.counts(t); outgoing != tt.wantOutgoing || sent != tt.wantSent {
				t.Fatalf("concurrent ticks produced wrong state: outgoing=%d sent=%d, want %d/%d", outgoing, sent, tt.wantOutgoing, tt.wantSent)
			}
			if tt.wantOutgoing == 1 {
				if attempt := db.oneOutgoing(t).SendAttempt; attempt != tt.wantAttempt {
					t.Fatalf("concurrent ticks changed attempt %d times: got %d, want %d", attempt-tt.initialAttempt, attempt, tt.wantAttempt)
				}
			}
		})
	}
}

// selectiveWhatsAppSender fails only the messages addressed to failPhone.
type selectiveWhatsAppSender struct {
	mu        sync.Mutex
	failPhone string
	err       error
	calls     int
}

func (s *selectiveWhatsAppSender) SendTemplateMessage(_ context.Context, to string, _ string, _ string, _ map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if to == s.failPhone {
		return s.err
	}
	return nil
}

const poisonPhone = "+390000000000"

func (db *schedulerTestDB) addTo(t *testing.T, phone string, sendAttempt int) {
	t.Helper()
	db.addToInLang(t, phone, "it", sendAttempt)
}

func (db *schedulerTestDB) addToInLang(t *testing.T, phone string, lang string, sendAttempt int) {
	t.Helper()
	if _, err := db.service.AddToOutgoingWhatsApp(schedulerTestInstanceID, types.OutgoingWhatsApp{
		MessageType: "test", ToPhoneNumber: phone, TemplateName: "test_template", Lang: lang,
		ContentParams: map[string]string{"name": "Mario"}, SendAttempt: sendAttempt,
	}); err != nil {
		t.Fatalf("add outgoing WhatsApp message: %v", err)
	}
}

// langSelectiveWhatsAppSender fails every message sent in failLang.
type langSelectiveWhatsAppSender struct {
	mu       sync.Mutex
	failLang string
	err      error
	calls    int
}

func (s *langSelectiveWhatsAppSender) SendTemplateMessage(_ context.Context, _ string, _ string, lang string, _ map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if lang == s.failLang {
		return s.err
	}
	return nil
}

func (db *schedulerTestDB) attemptsOf(t *testing.T, phone string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg types.OutgoingWhatsApp
	if err := db.outgoing.FindOne(ctx, bson.M{"toPhoneNumber": phone}).Decode(&msg); err != nil {
		t.Fatalf("read outgoing WhatsApp message to %s: %v", phone, err)
	}
	return msg.SendAttempt
}

// A single message that keeps failing with a sender-side error is not an outage: the
// messages behind it must still go out, and the failure must count against it.
func TestWhatsAppPoisonMessageDoesNotStallTheQueue(t *testing.T) {
	transient := &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000}
	for _, tt := range []struct {
		name          string
		poisonAt      int // position of the poison message among six
		startAttempts int
		wantOutgoing  int64
		wantSent      int64
		wantAttempts  int // -1 when the poison must have been archived
	}{
		{"first of six, first failure", 0, 0, 1, 5, 1},
		{"third of six, first failure", 2, 0, 1, 5, 1},
		{"first of six, last allowed failure is archived", 0, maxWhatsAppSendAttempts - 1, 0, 6, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			for i := 0; i < 6; i++ {
				if i == tt.poisonAt {
					db.addTo(t, poisonPhone, tt.startAttempts)
				} else {
					db.addTo(t, fmt.Sprintf("+3911111111%02d", i), 0)
				}
			}
			sender := &selectiveWhatsAppSender{failPhone: poisonPhone, err: transient}

			runWhatsAppHandler(db.service, sender, 60)

			if outgoing, sent := db.counts(t); outgoing != tt.wantOutgoing || sent != tt.wantSent {
				t.Fatalf("queue after the tick: outgoing=%d sent=%d, want %d/%d", outgoing, sent, tt.wantOutgoing, tt.wantSent)
			}
			if tt.wantAttempts >= 0 {
				if got := db.attemptsOf(t, poisonPhone); got != tt.wantAttempts {
					t.Fatalf("poison message attempts = %d, want %d", got, tt.wantAttempts)
				}
			}
		})
	}
}

// A sender-side failure on the last message of the tick cannot be told apart from an
// outage, so it is left as it is: no attempt consumed, lock released on expiry.
func TestWhatsAppLoneSenderSideFailureIsLeftUntouched(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, "+391111111100", 0)
	db.addTo(t, poisonPhone, 0)
	sender := &selectiveWhatsAppSender{failPhone: poisonPhone, err: &waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 1 {
		t.Fatalf("expected the healthy message delivered and the other still queued, got outgoing=%d sent=%d", outgoing, sent)
	}
	if got := db.attemptsOf(t, poisonPhone); got != 0 {
		t.Fatalf("an undecided sender-side failure must not consume an attempt, got %d", got)
	}
}

// A recipient-level throttle in between says nothing about the sender, so the earlier
// undecided failure waits for the next message that does.
func TestWhatsAppRecipientThrottleDoesNotDecideAnEarlierFailure(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, "+391111111101", 0)
	db.addTo(t, "+391111111102", 0)
	sender := &scriptedWhatsAppSender{errors: []error{
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131056},
		nil,
	}}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 2 || sent != 1 {
		t.Fatalf("expected one delivery, got outgoing=%d sent=%d", outgoing, sent)
	}
	if got := db.attemptsOf(t, poisonPhone); got != 1 {
		t.Fatalf("the failure before the throttle must be charged once the third message succeeds, got %d attempts", got)
	}
}

// An unknown template is a per-language condition: the messages in that language pay for
// it one attempt at a time, and the messages in the other language keep flowing.
func TestWhatsAppUnknownTemplateLanguageDoesNotStallTheOtherLanguage(t *testing.T) {
	db := newSchedulerTestDB(t)
	for i := 0; i < 6; i++ {
		lang := "it"
		if i%2 == 0 {
			lang = "en"
		}
		db.addToInLang(t, fmt.Sprintf("+3911111111%02d", i), lang, 0)
	}
	// the template is approved in Italian only: every English send is rejected with 132001
	sender := &langSelectiveWhatsAppSender{failLang: "en", err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132001}}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 3 || sent != 3 {
		t.Fatalf("expected the Italian half delivered, got outgoing=%d sent=%d", outgoing, sent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stuck, err := db.outgoing.CountDocuments(ctx, bson.M{"lang": "en", "sendAttempt": 1}); err != nil {
		t.Fatalf("inspect retry counters: %v", err)
	} else if stuck != 3 {
		t.Fatalf("expected the three English messages charged one attempt each, got %d", stuck)
	}
	if delivered, err := db.sent.CountDocuments(ctx, bson.M{"lang": "it"}); err != nil {
		t.Fatalf("inspect the archive: %v", err)
	} else if delivered != 3 {
		t.Fatalf("expected the three Italian messages archived as delivered, got %d", delivered)
	}
}

// A failure of the message's own (invalid parameters) decides an earlier held failure just
// as a success does: the held message was at fault.
func TestWhatsAppMessageSpecificFailureDecidesAnEarlierFailure(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, "+391111111101", 0)
	sender := &scriptedWhatsAppSender{errors: []error{
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132012},
	}}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 2 || sent != 0 {
		t.Fatalf("expected both messages still queued, got outgoing=%d sent=%d", outgoing, sent)
	}
	if got := db.attemptsOf(t, poisonPhone); got != 1 {
		t.Fatalf("the held failure must be charged once the next message fails on its own, got %d", got)
	}
	if got := db.attemptsOf(t, "+391111111101"); got != 1 {
		t.Fatalf("the message-specific failure must be charged as before, got %d", got)
	}
}

// perPhoneWhatsAppSender scripts the outcome of each call per recipient and can delay one of
// them long enough for the locks to expire.
type perPhoneWhatsAppSender struct {
	mu        sync.Mutex
	script    map[string][]error
	calls     map[string]int
	slowPhone string
	delay     time.Duration
}

func (s *perPhoneWhatsAppSender) SendTemplateMessage(_ context.Context, to string, _ string, _ string, _ map[string]string) error {
	if to == s.slowPhone {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	call := s.calls[to]
	s.calls[to]++
	if outcomes := s.script[to]; call < len(outcomes) {
		return outcomes[call]
	}
	return nil
}

// A held failure whose claim has expired is not charged, and the message it belongs to is not
// archived as failed: charging the stale copy would bury a message that is still deliverable.
// The tick that lost the claim stops as soon as the fetch hands that message back, so the
// delivery happens on the tick after it, which is where the archive row comes from.
func TestWhatsAppHeldFailureIsNotChargedAfterItsClaimExpired(t *testing.T) {
	const other = "+391111111101"
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, maxWhatsAppSendAttempts-1)
	db.addTo(t, other, 0)
	sender := &perPhoneWhatsAppSender{
		script: map[string][]error{
			// first call fails transiently, the re-claimed second call succeeds
			poisonPhone: {&waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}, nil},
			// slow enough to outlive the 2 s lock; a recipient limit decides nothing and
			// charges nothing, so each tick ends on the fetch that hands these rows back
			other: {&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131056}, &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131056}},
		},
		slowPhone: other,
		delay:     3200 * time.Millisecond, // locks are compared in whole seconds
	}

	// The first tick loses the claims while the slow message is in flight and stops when the
	// fetch hands the same messages back: nothing of the held failure may be charged.
	runWhatsAppHandler(db.service, sender, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stale, err := db.sent.CountDocuments(ctx, bson.M{"toPhoneNumber": poisonPhone}); err != nil {
		t.Fatalf("inspect the archive: %v", err)
	} else if stale != 0 {
		t.Fatalf("the held failure was charged and archived by the tick that lost its claim")
	}
	if attempts := db.attemptsOf(t, poisonPhone); attempts != maxWhatsAppSendAttempts-1 {
		t.Fatalf("the held failure was charged an attempt: %d, want %d", attempts, maxWhatsAppSendAttempts-1)
	}

	// The tick after it delivers the message the first one held.
	db.expireLocks(t)
	runWhatsAppHandler(db.service, sender, 2)

	archived, err := db.sent.CountDocuments(ctx, bson.M{"toPhoneNumber": poisonPhone})
	if err != nil {
		t.Fatalf("inspect the archive: %v", err)
	}
	if archived != 1 {
		t.Fatalf("the delivered message must be archived exactly once, found %d archive rows", archived)
	}
	if queued, err := db.outgoing.CountDocuments(ctx, bson.M{"toPhoneNumber": poisonPhone}); err != nil {
		t.Fatalf("inspect the queue: %v", err)
	} else if queued != 0 {
		t.Fatalf("the delivered message must have left the queue, found %d rows", queued)
	}
}
