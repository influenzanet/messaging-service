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
}

func newSchedulerTestDB(t *testing.T) *schedulerTestDB {
	t.Helper()
	uri := os.Getenv("F04_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("F04_TEST_MONGODB_URI is not configured")
	}

	prefix := "f04_scheduler_" + uuid.NewString() + "_"
	service := messagedb.NewMessageDBService(types.DBConfig{
		URI:             uri,
		Timeout:         5,
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
	handleOutgoingWhatsAppForInstance(mdb, schedulerTestInstanceID, sender, lockSeconds, &wg)
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
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"HTTP 401", &waClient.WhatsAppSendError{StatusCode: http.StatusUnauthorized}},
		{"HTTP 400 Meta 190", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 190}},
		{"HTTP 403", &waClient.WhatsAppSendError{StatusCode: http.StatusForbidden}},
		{"HTTP 429", &waClient.WhatsAppSendError{StatusCode: http.StatusTooManyRequests}},
		{"HTTP 400 Meta 130429", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 130429}},
		{"HTTP 500", &waClient.WhatsAppSendError{StatusCode: http.StatusInternalServerError}},
		{"HTTP 400 Meta 131016", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131016}},
		{"HTTP 400 Meta 133010", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 133010}},
		{"Meta transient flag", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 999, IsTransient: true}},
		{"wrapped typed error", fmt.Errorf("send failed: %w", &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 190})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.add(t, 2, 0)
			sender := &fakeWhatsAppSender{err: tt.err}

			runWhatsAppHandler(db.service, sender, 60)

			if sender.callCount() != 1 {
				t.Fatalf("global failure must open the per-tick circuit after one call, got %d calls", sender.callCount())
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

	if sender.callCount() != 1 {
		t.Fatalf("global failure must stop the outer fetch loop, got %d calls", sender.callCount())
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
