package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// archivedWhatsApp is the archive row as an operator reads it: the outcome fields are read
// through bson tags rather than the Go type, so that a renamed or dropped field fails here.
type archivedWhatsApp struct {
	ToPhoneNumber string `bson:"toPhoneNumber"`
	SendAttempt   int    `bson:"sendAttempt"`
	Status        string `bson:"status"`
	ErrorCode     int    `bson:"errorCode"`
	ErrorClass    string `bson:"errorClass"`
	SentAt        int64  `bson:"sentAt"`
}

func (db *schedulerTestDB) archivedFor(t *testing.T, phone string) archivedWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row archivedWhatsApp
	if err := db.sent.FindOne(ctx, bson.M{"toPhoneNumber": phone}).Decode(&row); err != nil {
		t.Fatalf("read the archived WhatsApp message to %s: %v", phone, err)
	}
	return row
}

// A message Meta accepted is archived as delivered, with no error attached to it.
// Fails if the success path stops recording the outcome, or records it as a failure.
func TestWhatsAppDeliveredMessageIsArchivedWithItsOutcome(t *testing.T) {
	const recipient = "+391111111101"
	db := newSchedulerTestDB(t)
	db.addTo(t, recipient, 0)
	before := time.Now().Unix()

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{}, 60)

	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("delivered message not archived exactly once: outgoing=%d sent=%d", outgoing, sent)
	}
	row := db.archivedFor(t, recipient)
	if row.Status != types.WhatsAppStatusDelivered {
		t.Fatalf("archived status = %q, want %q", row.Status, types.WhatsAppStatusDelivered)
	}
	if row.ErrorCode != 0 {
		t.Fatalf("a delivered message was archived with Meta error code %d", row.ErrorCode)
	}
	if row.ErrorClass != "" {
		t.Fatalf("a delivered message was archived with error class %q", row.ErrorClass)
	}
	if row.SentAt < before {
		t.Fatalf("archived sentAt = %d, want the archive time (>= %d)", row.SentAt, before)
	}
}

// A message the scheduler gives up on at the attempt cap is archived as failed, and keeps the
// Meta code and the class of the failure that exhausted it — the two rows are otherwise
// identical to a delivered one, sendAttempt included.
// Fails if the failure path stops recording the outcome, or loses the last error.
func TestWhatsAppMessageArchivedAtTheCapKeepsTheLastFailure(t *testing.T) {
	const healthy = "+391111111101"
	db := newSchedulerTestDB(t)
	// A transient failure is held until the next message answers: the healthy message behind
	// the poison one decides it, so the poison message is charged its last attempt.
	db.addTo(t, poisonPhone, maxWhatsAppSendAttempts-1)
	db.addTo(t, healthy, 0)
	sender := &selectiveWhatsAppSender{
		failPhone: poisonPhone,
		err:       &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131000},
	}
	before := time.Now().Unix()

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 2 {
		t.Fatalf("queue after the cap: outgoing=%d sent=%d, want 0/2", outgoing, sent)
	}
	row := db.archivedFor(t, poisonPhone)
	if row.Status != types.WhatsAppStatusFailed {
		t.Fatalf("archived status = %q, want %q", row.Status, types.WhatsAppStatusFailed)
	}
	if row.ErrorCode != 131000 {
		t.Fatalf("archived Meta error code = %d, want 131000", row.ErrorCode)
	}
	// Compared against the literal, not against String(): the class name is stored in every
	// failed archive row, so renaming it has to fail here rather than quietly split the
	// archive into rows that no single query can select.
	if row.ErrorClass != "transient" {
		t.Fatalf("archived error class = %q, want %q", row.ErrorClass, "transient")
	}
	if row.SentAt < before {
		t.Fatalf("archived sentAt = %d, want the archive time (>= %d)", row.SentAt, before)
	}
	// The delivered neighbour must not inherit the failure of the message before it.
	if delivered := db.archivedFor(t, healthy); delivered.Status != types.WhatsAppStatusDelivered || delivered.ErrorCode != 0 {
		t.Fatalf("the delivered message was archived as %q with code %d", delivered.Status, delivered.ErrorCode)
	}
}
