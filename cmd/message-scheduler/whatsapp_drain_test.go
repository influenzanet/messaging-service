package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// setClaimAge rewrites the claim of every queued message to the given age in seconds, so that
// the lock can be placed on either side of lastAttemptOlderThan without waiting for real time.
func (db *schedulerTestDB) setClaimAge(t *testing.T, ageSeconds int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateMany(ctx, bson.M{}, bson.M{
		"$set": bson.M{"lastSendAttempt": time.Now().Unix() - ageSeconds},
	}); err != nil {
		t.Fatalf("set claim age of outgoing WhatsApp messages: %v", err)
	}
}

// addUndecodable queues a document the driver cannot decode into types.OutgoingWhatsApp, which
// is how a database error other than ErrNoDocuments is injected into FetchOutgoingWhatsApp
// without disconnecting the client the test harness still has to close.
// lastSendAttempt is written explicitly: $lt never matches a missing field, so a document
// without it would simply never be fetched and the test would pass without proving anything.
func (db *schedulerTestDB) addUndecodable(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.InsertOne(ctx, bson.M{
		"messageType":     "test",
		"toPhoneNumber":   undecodablePhone,
		"templateName":    "test_template",
		"lang":            "it",
		"lastSendAttempt": int64(0),
		"sendAttempt":     "not-a-number",
	}); err != nil {
		t.Fatalf("queue an undecodable outgoing WhatsApp document: %v", err)
	}
}

const undecodablePhone = "+399999999999"

// countQueued counts the queued messages matching a filter. Checks on retry counters are
// scoped to the healthy recipients: the injected document carries a string sendAttempt, which
// would match any $ne query on that field.
func (db *schedulerTestDB) countQueued(t *testing.T, filter bson.M) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := db.outgoing.CountDocuments(ctx, filter)
	if err != nil {
		t.Fatalf("count outgoing WhatsApp messages matching %v: %v", filter, err)
	}
	return count
}

// The queue is drained until it is empty, not one batch per tick: a single tick fetches as
// many batches of outgoingBatchSize as it takes.
// Fails if the outer fetch loop of handleOutgoingWhatsAppForInstance stops after the first
// batch, or if the batch is no longer claimed in blocks of outgoingBatchSize.
func TestWhatsAppDrainLoopEmptiesTheQueueAcrossSeveralBatches(t *testing.T) {
	const queued = outgoingBatchSize*2 + 5
	db := newSchedulerTestDB(t)
	db.add(t, queued, 0)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != queued {
		t.Fatalf("drain loop sent %d messages in one tick, want the whole queue (%d)", sender.callCount(), queued)
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != queued {
		t.Fatalf("queue not drained: outgoing=%d sent=%d, want 0/%d", outgoing, sent, queued)
	}
}

// The five-attempt cap over the whole life of a message: four failures leave it queued with a
// moving counter, the fifth archives it in sent-whatsapp and removes it from the queue.
// Fails if maxWhatsAppSendAttempts changes, or if the boundary in recordFailedWhatsAppAttempt
// moves (msg.SendAttempt+1 >= max becoming > max archives on the sixth attempt instead).
func TestWhatsAppRetryCapArchivesOnTheFifthFailure(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	// A message-specific failure is charged to the message it happened on. A transient one
	// would be held as undecided and, with nothing behind it in the queue, never charged.
	sender := &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{
		StatusCode: http.StatusBadRequest,
		Code:       132012,
	}}

	for attempt := 1; attempt < maxWhatsAppSendAttempts; attempt++ {
		runWhatsAppHandler(db.service, sender, 60)
		if got := db.attemptsOf(t, poisonPhone); got != attempt {
			t.Fatalf("after failure %d the counter is %d, want %d", attempt, got, attempt)
		}
		if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
			t.Fatalf("failure %d archived the message before the cap: outgoing=%d sent=%d", attempt, outgoing, sent)
		}
		db.expireLocks(t)
	}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != maxWhatsAppSendAttempts {
		t.Fatalf("the message was sent %d times, want %d", sender.callCount(), maxWhatsAppSendAttempts)
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("the exhausted message was not archived exactly once: outgoing=%d sent=%d", outgoing, sent)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var archived struct {
		ToPhoneNumber string `bson:"toPhoneNumber"`
		SendAttempt   int    `bson:"sendAttempt"`
	}
	if err := db.sent.FindOne(ctx, bson.M{}).Decode(&archived); err != nil {
		t.Fatalf("read the archived WhatsApp message: %v", err)
	}
	if archived.ToPhoneNumber != poisonPhone {
		t.Fatalf("archived recipient = %q, want %q", archived.ToPhoneNumber, poisonPhone)
	}
	// The archive keeps the counter as it was read for the last attempt: the attempt that
	// exhausts the cap archives instead of incrementing.
	if archived.SendAttempt != maxWhatsAppSendAttempts-1 {
		t.Fatalf("archived sendAttempt = %d, want %d", archived.SendAttempt, maxWhatsAppSendAttempts-1)
	}
	if params, err := db.sent.CountDocuments(ctx, bson.M{"contentParams": bson.M{"$exists": true}}); err != nil {
		t.Fatalf("inspect the archived content parameters: %v", err)
	} else if params != 0 {
		t.Fatalf("the archive kept the content parameters of %d messages", params)
	}

	// Nothing is left to retry: a further tick must not reach the sender at all.
	db.expireLocks(t)
	runWhatsAppHandler(db.service, sender, 60)
	if sender.callCount() != maxWhatsAppSendAttempts {
		t.Fatalf("an archived message was sent again: %d calls, want %d", sender.callCount(), maxWhatsAppSendAttempts)
	}
}

// FetchOutgoingWhatsApp must tell an exhausted queue apart from a database failure: the first
// is the normal end of the drain loop, the second has to reach the caller.
// Fails if the ErrNoDocuments branch is removed, or if a decode/database error is swallowed
// and reported as an empty batch.
func TestWhatsAppFetchPropagatesDatabaseErrorsButNotAnExhaustedQueue(t *testing.T) {
	db := newSchedulerTestDB(t)

	messages, err := db.service.FetchOutgoingWhatsApp(schedulerTestInstanceID, outgoingBatchSize, 60)
	if err != nil {
		t.Fatalf("an exhausted queue must not be an error, got %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("an exhausted queue returned %d messages", len(messages))
	}

	db.addUndecodable(t)
	messages, err = db.service.FetchOutgoingWhatsApp(schedulerTestInstanceID, outgoingBatchSize, 60)
	if err == nil {
		t.Fatalf("a database error was swallowed, got %d messages and no error", len(messages))
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatalf("a database error was reported as an exhausted queue: %v", err)
	}
}

// A database error found part-way through a batch drops the whole batch: the messages already
// claimed by that fetch are not sent on the strength of a read that failed, and nothing is
// archived or charged an attempt. They keep their claim and are retried once it expires.
// Fails if handleOutgoingWhatsAppForInstance stops breaking out of the fetch loop on error and
// processes the partial batch instead, or if FetchOutgoingWhatsApp stops reporting the error.
func TestWhatsAppFetchErrorDropsThePartiallyClaimedBatch(t *testing.T) {
	const healthy = "+391111111101"
	db := newSchedulerTestDB(t)
	// Natural order is insertion order: the fetch claims and decodes the healthy message, then
	// fails on the injected document and returns both the partial batch and the error.
	db.addTo(t, healthy, 0)
	db.addUndecodable(t)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 0 {
		t.Fatalf("a batch that could not be read to the end still sent %d messages", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 2 || sent != 0 {
		t.Fatalf("a failed read changed the queue: outgoing=%d sent=%d, want 2/0", outgoing, sent)
	}
	if claimed := db.countQueued(t, bson.M{"toPhoneNumber": healthy, "lastSendAttempt": bson.M{"$ne": int64(0)}}); claimed != 1 {
		t.Fatalf("the message of the dropped batch lost its claim, found %d claimed", claimed)
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": healthy, "sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("a failed read charged an attempt to %d healthy messages", charged)
	}
}

// A database error found while draining stops the loop where it is: the batch that was read in
// full stays sent, and the batch the error cut short is dropped whole, claimed but untouched.
// Fails if the error from FetchOutgoingWhatsApp is ignored and the partial second batch is
// sent, or if the tick charges the messages it could not read.
func TestWhatsAppFetchErrorStopsTheDrainMidTick(t *testing.T) {
	const trailing = 5
	const trailingPrefix = "^\\+3922222222"
	db := newSchedulerTestDB(t)
	// The first fetch reads a full batch, the second claims the trailing messages and then
	// fails on the injected document.
	for i := 0; i < outgoingBatchSize; i++ {
		db.addTo(t, fmt.Sprintf("+3911111111%02d", i), 0)
	}
	for i := 0; i < trailing; i++ {
		db.addTo(t, fmt.Sprintf("+3922222222%02d", i), 0)
	}
	db.addUndecodable(t)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != outgoingBatchSize {
		t.Fatalf("the tick sent %d messages, want exactly the batch it could read in full (%d)", sender.callCount(), outgoingBatchSize)
	}
	if outgoing, sent := db.counts(t); outgoing != trailing+1 || sent != outgoingBatchSize {
		t.Fatalf("queue after the interrupted drain: outgoing=%d sent=%d, want %d/%d", outgoing, sent, trailing+1, outgoingBatchSize)
	}
	if claimed := db.countQueued(t, bson.M{"toPhoneNumber": bson.M{"$regex": trailingPrefix}, "lastSendAttempt": bson.M{"$ne": int64(0)}}); claimed != trailing {
		t.Fatalf("the dropped batch kept %d claims, want %d", claimed, trailing)
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": bson.M{"$regex": trailingPrefix}, "sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("%d messages of the dropped batch were charged an attempt", charged)
	}
}

// A message claimed by a tick that never came back is not retried while its claim is fresh,
// and is picked up again once the claim is older than lastAttemptOlderThan. This is the whole
// recovery mechanism after a crash: nothing resets the claim, it only ages out.
// Fails if the olderThan filter in FetchOutgoingWhatsApp is dropped or inverted, or if the
// claim stops being written as a timestamp.
func TestWhatsAppClaimOfACrashedTickIsRetriedOnlyAfterItExpires(t *testing.T) {
	const lockSeconds = 60
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	sender := &fakeWhatsAppSender{}

	db.setClaimAge(t, lockSeconds-5)
	runWhatsAppHandler(db.service, sender, lockSeconds)

	if sender.callCount() != 0 {
		t.Fatalf("a message under an active claim was sent %d times", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 1 || sent != 0 {
		t.Fatalf("a message under an active claim left the queue: outgoing=%d sent=%d", outgoing, sent)
	}

	db.setClaimAge(t, lockSeconds+5)
	runWhatsAppHandler(db.service, sender, lockSeconds)

	if sender.callCount() != 1 {
		t.Fatalf("an expired claim was not picked up again: %d calls, want 1", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 1 {
		t.Fatalf("the re-claimed message was not delivered: outgoing=%d sent=%d", outgoing, sent)
	}
}
