package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

const (
	brokenTemplate  = "weekly_broken"
	healthyTemplate = "other_healthy"
)

// templateSelectiveWhatsAppSender fails every send of one template in one language with the
// same error and counts the calls made for each template and language.
type templateSelectiveWhatsAppSender struct {
	mu       sync.Mutex
	failName string
	failLang string
	err      error
	calls    map[string]int
}

func (s *templateSelectiveWhatsAppSender) SendTemplateMessage(_ context.Context, _ string, name string, lang string, _ map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[name+"|"+lang]++
	if name == s.failName && lang == s.failLang {
		return s.err
	}
	return nil
}

func (s *templateSelectiveWhatsAppSender) callsFor(name, lang string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name+"|"+lang]
}

func (s *templateSelectiveWhatsAppSender) heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = nil
}

func (db *schedulerTestDB) addWithTemplate(t *testing.T, phone, name, lang string, sendAttempt int) {
	t.Helper()
	if _, err := db.service.AddToOutgoingWhatsApp(schedulerTestInstanceID, types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: phone, TemplateName: name, Lang: lang,
		ContentParams: map[string]string{"button_0": "token"}, SendAttempt: sendAttempt,
	}); err != nil {
		t.Fatalf("add outgoing WhatsApp message: %v", err)
	}
}

func (db *schedulerTestDB) queuedWithTemplate(t *testing.T, name, lang string) []types.OutgoingWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := db.outgoing.Find(ctx, bson.M{"templateName": name, "lang": lang})
	if err != nil {
		t.Fatalf("read queued WhatsApp messages: %v", err)
	}
	var msgs []types.OutgoingWhatsApp
	if err := cur.All(ctx, &msgs); err != nil {
		t.Fatalf("decode queued WhatsApp messages: %v", err)
	}
	return msgs
}

func runWhatsAppHandlerWithTemplateDelay(db *schedulerTestDB, sender whatsAppSender, lockSeconds, templateRetryDelay int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, lockSeconds, templateRetryDelay, &wg)
	wg.Wait()
}

// A template Meta refuses (paused, disabled, marketing turned off) must not stop the queue:
// the messages of the other templates go out in the same tick, Meta is asked once about the
// broken template, and every message of that template pays one attempt and waits for the
// retry delay instead of being fetched again on every tick.
func TestWhatsAppBrokenTemplateDoesNotStallTheOtherTemplates(t *testing.T) {
	const lock, delay = 60, 3600
	for _, code := range []int{132015, 132016, 131063} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			db := newSchedulerTestDB(t)
			for i := 0; i < 3; i++ {
				db.addWithTemplate(t, fmt.Sprintf("+3911111110%02d", i), brokenTemplate, "it", 0)
			}
			for i := 0; i < 3; i++ {
				db.addWithTemplate(t, fmt.Sprintf("+3922222220%02d", i), healthyTemplate, "it", 0)
			}
			sender := &templateSelectiveWhatsAppSender{failName: brokenTemplate, failLang: "it",
				err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: code}}

			before := time.Now().Unix()
			runWhatsAppHandlerWithTemplateDelay(db, sender, lock, delay)

			if got := sender.callsFor(brokenTemplate, "it"); got != 1 {
				t.Fatalf("Meta must be asked once about the broken template in a tick, got %d calls", got)
			}
			if outgoing, sent := db.counts(t); outgoing != 3 || sent != 3 {
				t.Fatalf("expected the healthy template delivered and the broken one queued, got outgoing=%d sent=%d", outgoing, sent)
			}
			for _, msg := range db.queuedWithTemplate(t, brokenTemplate, "it") {
				if msg.SendAttempt != 1 {
					t.Fatalf("every message of the broken template must pay one attempt, %s has %d", msg.ToPhoneNumber, msg.SendAttempt)
				}
				// fetchable again once lastSendAttempt < now-lock, i.e. after the retry delay
				if resumesAt := msg.LastSendAttempt + lock; resumesAt < before+delay || resumesAt > time.Now().Unix()+delay {
					t.Fatalf("message %s resumes at %d, want about %d", msg.ToPhoneNumber, resumesAt, before+delay)
				}
			}
		})
	}
}

// The deferral is a wait, not a verdict: before the delay the broken template is not tried
// again, after it the messages go out as soon as Meta accepts the template.
func TestWhatsAppBrokenTemplateIsRetriedOnlyAfterTheDelay(t *testing.T) {
	db := newSchedulerTestDB(t)
	for i := 0; i < 3; i++ {
		db.addWithTemplate(t, fmt.Sprintf("+3911111110%02d", i), brokenTemplate, "it", 0)
	}
	sender := &templateSelectiveWhatsAppSender{failName: brokenTemplate, failLang: "it",
		err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132015}}

	const lock = 60
	runWhatsAppHandlerWithTemplateDelay(db, sender, lock, 3600)
	sender.heal()
	// An ordinary claim would have expired by now, the retry delay has not.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateMany(ctx, bson.M{}, bson.M{"$inc": bson.M{"lastSendAttempt": -(lock + 1)}}); err != nil {
		t.Fatalf("age the claims past the lock: %v", err)
	}
	runWhatsAppHandlerWithTemplateDelay(db, sender, lock, 3600)

	if got := sender.callsFor(brokenTemplate, "it"); got != 1 {
		t.Fatalf("a deferred template must not be tried before its delay, got %d calls", got)
	}
	if outgoing, _ := db.counts(t); outgoing != 3 {
		t.Fatalf("the deferred messages must still be queued before the delay, got %d", outgoing)
	}
	db.expireLocks(t) // the delay has passed
	runWhatsAppHandlerWithTemplateDelay(db, sender, lock, 3600)

	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 3 {
		t.Fatalf("after the delay the template works again and all must be delivered, got outgoing=%d sent=%d", outgoing, sent)
	}
}

// Nothing waits forever: a message whose template is still refused on its last allowed
// attempt is archived as failed with Meta's code, and the others move one attempt closer.
func TestWhatsAppBrokenTemplateArchivesAtTheAttemptCap(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addWithTemplate(t, "+391111111000", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111003", brokenTemplate, "it", maxWhatsAppSendAttempts-2)
	db.addWithTemplate(t, "+391111111004", brokenTemplate, "it", maxWhatsAppSendAttempts-1)
	sender := &templateSelectiveWhatsAppSender{failName: brokenTemplate, failLang: "it",
		err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132016}}

	runWhatsAppHandlerWithTemplateDelay(db, sender, 60, 3600)

	if got := db.attemptsOf(t, "+391111111000"); got != 1 {
		t.Fatalf("fresh message: attempts = %d, want 1", got)
	}
	if got := db.attemptsOf(t, "+391111111003"); got != maxWhatsAppSendAttempts-1 {
		t.Fatalf("message one short of the cap: attempts = %d, want %d and still queued", got, maxWhatsAppSendAttempts-1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var archived types.SentWhatsApp
	if err := db.sent.FindOne(ctx, bson.M{"toPhoneNumber": "+391111111004"}).Decode(&archived); err != nil {
		t.Fatalf("the message on its last attempt must be archived: %v", err)
	}
	if archived.Status != types.WhatsAppStatusFailed || archived.ErrorCode != 132016 {
		t.Fatalf("archived as %s with code %d, want %s with 132016", archived.Status, archived.ErrorCode, types.WhatsAppStatusFailed)
	}
	if outgoing, sent := db.counts(t); outgoing != 2 || sent != 1 {
		t.Fatalf("queue after the tick: outgoing=%d sent=%d, want 2/1", outgoing, sent)
	}
}

// Meta approves and pauses a template per language: the same template in another language
// keeps flowing.
func TestWhatsAppBrokenTemplateInOneLanguageKeepsTheOtherLanguageFlowing(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addWithTemplate(t, "+391111111000", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111001", brokenTemplate, "en", 0)
	db.addWithTemplate(t, "+391111111002", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111003", brokenTemplate, "en", 0)
	sender := &templateSelectiveWhatsAppSender{failName: brokenTemplate, failLang: "it",
		err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132015}}

	runWhatsAppHandlerWithTemplateDelay(db, sender, 60, 3600)

	if got := len(db.queuedWithTemplate(t, brokenTemplate, "en")); got != 0 {
		t.Fatalf("the English messages must be delivered, %d still queued", got)
	}
	if got := len(db.queuedWithTemplate(t, brokenTemplate, "it")); got != 2 {
		t.Fatalf("the Italian messages must wait, got %d queued", got)
	}
}

// A held transient failure of the same template is charged once, by the deferral, not twice.
func TestWhatsAppBrokenTemplateChargesAHeldFailureOfTheSameTemplateOnce(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addWithTemplate(t, "+391111111000", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111001", brokenTemplate, "it", 0)
	sender := &scriptedWhatsAppSender{errors: []error{
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132015},
	}}

	runWhatsAppHandlerWithTemplateDelay(db, sender, 60, 3600)

	for _, phone := range []string{"+391111111000", "+391111111001"} {
		if got := db.attemptsOf(t, phone); got != 1 {
			t.Fatalf("%s: attempts = %d, want 1", phone, got)
		}
	}
}

// barrierWhatsAppSender answers no call until `parties` calls are waiting, so that concurrent
// ticks have all claimed their batches before any of them handles the answer.
type barrierWhatsAppSender struct {
	parties int
	err     error

	mu      sync.Mutex
	waiting int
	open    chan struct{}
}

func (s *barrierWhatsAppSender) SendTemplateMessage(ctx context.Context, _ string, _ string, _ string, _ map[string]string) error {
	s.mu.Lock()
	s.waiting++
	if s.waiting == s.parties {
		close(s.open)
	}
	s.mu.Unlock()
	select {
	case <-s.open:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.err
}

// Two ticks can run at once. Each defers only what it claimed or what nobody holds, so every
// message of the broken template pays exactly one attempt.
func TestConcurrentWhatsAppTicksChargeABrokenTemplateOnce(t *testing.T) {
	db := newSchedulerTestDB(t)
	total := outgoingBatchSize + 5
	for i := 0; i < total; i++ {
		db.addWithTemplate(t, fmt.Sprintf("+391111111%03d", i), brokenTemplate, "it", 0)
	}
	sender := &barrierWhatsAppSender{parties: 2, open: make(chan struct{}),
		err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132015}}

	var wg sync.WaitGroup
	wg.Add(2)
	go handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, 60, 3600, &wg)
	go handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, 60, 3600, &wg)
	wg.Wait()

	msgs := db.queuedWithTemplate(t, brokenTemplate, "it")
	if len(msgs) != total {
		t.Fatalf("expected all %d messages still queued, got %d", total, len(msgs))
	}
	for _, msg := range msgs {
		if msg.SendAttempt != 1 {
			t.Fatalf("message %s paid %d attempts, want exactly 1", msg.ToPhoneNumber, msg.SendAttempt)
		}
	}
}

func TestParseWhatsAppTemplateRetryDelay(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", defaultWhatsAppTemplateRetryDelay, false},
		{"60", 60, false},
		{"3600", 3600, false},
		{"0", 0, true},
		{"-5", 0, true},
		{"1h", 0, true},
	} {
		got, err := parseWhatsAppTemplateRetryDelay(tt.in)
		if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
			t.Fatalf("parseWhatsAppTemplateRetryDelay(%q) = %d, %v; want %d, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
