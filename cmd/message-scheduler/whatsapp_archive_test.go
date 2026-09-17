package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// archiveBlockerType marks the row that holds the archive key of a recipient hostage.
const archiveBlockerType = "archive-blocker"

// blockArchive makes the archive write fail for one recipient: a unique index on the archive
// collection plus a row that already holds that recipient's key, so the insert is refused with
// a duplicate key error. This is the closest stand-in for a database that accepts the send but
// refuses the archive write.
func (db *schedulerTestDB) blockArchive(t *testing.T, phone string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.sent.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "toPhoneNumber", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		t.Fatalf("create the unique index that blocks the archive: %v", err)
	}
	if _, err := db.sent.InsertOne(ctx, bson.M{"messageType": archiveBlockerType, "toPhoneNumber": phone}); err != nil {
		t.Fatalf("insert the row that blocks the archive of %s: %v", phone, err)
	}
}

// releaseArchive removes the blocking row and leaves the unique index in place, so the write
// that succeeds afterwards is the archive write of the message itself.
func (db *schedulerTestDB) releaseArchive(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.sent.DeleteOne(ctx, bson.M{"messageType": archiveBlockerType}); err != nil {
		t.Fatalf("remove the row that blocks the archive: %v", err)
	}
}

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

// Meta accepted the message, the archive write failed: the row is still queued, and the next
// tick must finish the archive instead of posting the message to Meta a second time. Without
// the delivered marker the queued row is indistinguishable from one that was never sent, so it
// is re-sent on every lock expiry, for ever, with no attempt ever charged.
// Fails if the success path stops marking the message before archiving it, or if a marked
// message is sent again instead of being archived.
func TestWhatsAppDeliveredMessageIsNotResentWhenItsArchiveWriteFails(t *testing.T) {
	const recipient = "+391111111101"
	db := newSchedulerTestDB(t)
	db.addTo(t, recipient, 0)
	db.blockArchive(t, recipient)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 1 {
		t.Fatalf("the first tick sent the message %d times, want 1", sender.callCount())
	}
	if queued := db.countQueued(t, bson.M{"toPhoneNumber": recipient}); queued != 1 {
		t.Fatalf("a message whose archive write failed left the queue: %d rows, want 1", queued)
	}

	// The claim expires and the message is claimed again: it was delivered, so nothing may
	// reach Meta, however often this repeats.
	for tick := 2; tick <= 3; tick++ {
		db.expireLocks(t)
		runWhatsAppHandler(db.service, sender, 60)
		if sender.callCount() != 1 {
			t.Fatalf("tick %d sent a delivered message again: %d calls in total, want 1", tick, sender.callCount())
		}
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": recipient, "sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("a delivered message was charged a failed attempt")
	}

	// Once the archive accepts writes again, the repair finishes the job it could not finish.
	db.releaseArchive(t)
	db.expireLocks(t)
	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 1 {
		t.Fatalf("the repairing tick sent the message again: %d calls in total, want 1", sender.callCount())
	}
	if queued := db.countQueued(t, bson.M{"toPhoneNumber": recipient}); queued != 0 {
		t.Fatalf("the repaired message did not leave the queue: %d rows", queued)
	}
	if row := db.archivedFor(t, recipient); row.Status != types.WhatsAppStatusDelivered {
		t.Fatalf("the repaired message was archived as %q, want %q", row.Status, types.WhatsAppStatusDelivered)
	}
}
