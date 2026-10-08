package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/coneno/logger"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

func permanentFailure(code int) error {
	return &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: code}
}

// A message Meta refuses for a reason no retry can fix is sent once and archived on the spot,
// with Meta's code and the permanent class, and its attempt counter is not moved.
// Fails if the permanent class falls back to the five-attempt policy.
func TestWhatsAppPermanentFailureIsArchivedAtTheFirstAttempt(t *testing.T) {
	for _, code := range []int{100, 131008, 131009, 131026, 131051, 132000, 132001, 132005, 132007, 132012, 132018} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.addTo(t, poisonPhone, 0)
			sender := &fakeWhatsAppSender{err: permanentFailure(code)}

			runWhatsAppHandler(db.service, sender, 60)

			if sender.callCount() != 1 {
				t.Fatalf("Meta was called %d times, want 1", sender.callCount())
			}
			if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
				t.Fatalf("permanent failure not archived at once: outgoing=%d sent=%d", outgoing, sent)
			}
			row := db.archivedFor(t, poisonPhone)
			if row.Status != types.WhatsAppStatusFailed || row.ErrorClass != "permanent" || row.ErrorCode != code {
				t.Fatalf("archived row = %+v, want failed/permanent/%d", row, code)
			}
			if row.SendAttempt != 0 {
				t.Fatalf("archived sendAttempt = %d, want 0: the attempt counter must not move", row.SendAttempt)
			}
		})
	}
}

// A permanent failure never touches the messages behind it: they are sent in the same tick.
func TestWhatsAppPermanentFailureDoesNotStallTheQueue(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, "+391111111101", 0)
	db.addTo(t, "+391111111102", 0)
	sender := &selectiveWhatsAppSender{failPhone: poisonPhone, err: permanentFailure(132018)}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.calls != 3 {
		t.Fatalf("Meta was called %d times, want 3", sender.calls)
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 3 {
		t.Fatalf("queue after the tick: outgoing=%d sent=%d, want 0/3", outgoing, sent)
	}
	if row := db.archivedFor(t, poisonPhone); row.Status != types.WhatsAppStatusFailed || row.ErrorClass != "permanent" {
		t.Fatalf("poison row = %+v, want failed/permanent", row)
	}
	for _, phone := range []string{"+391111111101", "+391111111102"} {
		if row := db.archivedFor(t, phone); row.Status != types.WhatsAppStatusDelivered {
			t.Fatalf("message to %s archived as %q, want delivered", phone, row.Status)
		}
	}
}

// A permanent failure is an answer from Meta: an earlier held transient failure is decided by
// it like by any other answer, and charged one attempt.
func TestWhatsAppPermanentFailureDecidesAnEarlierTransientFailure(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, "+391111111101", 0)
	sender := &scriptedWhatsAppSender{errors: []error{
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
		permanentFailure(132001),
	}}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 2 {
		t.Fatalf("Meta was called %d times, want 2", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 1 {
		t.Fatalf("queue after the tick: outgoing=%d sent=%d, want 1/1", outgoing, sent)
	}
	if got := db.attemptsOf(t, poisonPhone); got != 1 {
		t.Fatalf("the held transient failure must be charged once, got %d attempts", got)
	}
	if row := db.archivedFor(t, "+391111111101"); row.Status != types.WhatsAppStatusFailed || row.ErrorClass != "permanent" || row.ErrorCode != 132001 {
		t.Fatalf("permanent row = %+v, want failed/permanent/132001", row)
	}
}

// A permanent failure is not read as an outage signal: it archives its own message, a transient
// failure behind it is held as any first one, and the tick goes on to the end.
func TestWhatsAppPermanentFailureDoesNotCountAsAnOutageSignal(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, "+391111111101", 0)
	db.addTo(t, "+391111111102", 0)
	sender := &scriptedWhatsAppSender{errors: []error{
		permanentFailure(132000),
		&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
		nil,
	}}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 3 {
		t.Fatalf("Meta was called %d times, want 3: the tick must go on", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 2 {
		t.Fatalf("queue after the tick: outgoing=%d sent=%d, want 1/2", outgoing, sent)
	}
}

// The log line says why the message is gone and never carries the recipient's number.
func TestWhatsAppPermanentFailureIsLoggedWithoutThePhoneNumber(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	logged := captureLog(t, logger.Error)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: permanentFailure(132001)}, 60)

	out := logged.String()
	if !strings.Contains(out, "archived at the first attempt") || !strings.Contains(out, "132001") || !strings.Contains(out, "permanent") {
		t.Fatalf("the log does not explain the immediate archive: %q", out)
	}
	if strings.Contains(out, poisonPhone) || strings.Contains(out, poisonPhone[1:]) {
		t.Fatalf("the log carries the phone number: %q", out)
	}
}

// Regression guard: a failure the classifier does not know keeps the bounded retry, so it is
// charged an attempt and stays queued instead of being archived at the first failure.
func TestWhatsAppUnclassifiedMetaCodeStillFollowsTheBoundedRetry(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	sender := &fakeWhatsAppSender{err: permanentFailure(999999)}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("unclassified failure archived at the first attempt: outgoing=%d sent=%d", outgoing, sent)
	}
	if got := db.attemptsOf(t, poisonPhone); got != 1 {
		t.Fatalf("unclassified failure attempts = %d, want 1", got)
	}
	var sendErr *waClient.WhatsAppSendError
	if !errors.As(sender.err, &sendErr) || sendErr.Class() != waClient.WhatsAppErrorUnknown {
		t.Fatalf("test premise broken: code 999999 must be unknown")
	}
}

// A permanent failure whose archive write is refused leaves the message queued, and it is charged
// an attempt: the attempt cap still bounds how often it can reach Meta again.
func TestWhatsAppPermanentFailureWithFailedArchiveIsCharged(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.blockArchive(t, poisonPhone)
	sender := &fakeWhatsAppSender{err: permanentFailure(132001)}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 1 {
		t.Fatalf("Meta was called %d times, want 1", sender.callCount())
	}
	if queued := db.countQueued(t, bson.M{"toPhoneNumber": poisonPhone}); queued != 1 {
		t.Fatalf("a message whose archive write failed left the queue: %d rows, want 1", queued)
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": poisonPhone, "sendAttempt": 1}); charged != 1 {
		t.Fatalf("a permanent failure that could not be archived was not charged an attempt")
	}
}
