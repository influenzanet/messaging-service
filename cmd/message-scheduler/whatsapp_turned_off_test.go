package main

// Turning WhatsApp off for an instance must stop a tick that is already draining its queue, not
// only the ticks that start afterwards: handleOutgoingWhatsAppForInstance asks whatsAppOn
// before every call to Meta. In production whatsAppOn is settings.Enabled(instanceID), whose
// answer is cached for a minute, which is where the bound on the stop comes from.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// whatsAppAlwaysOn is the whatsAppOn of the tests that are not about turning WhatsApp off.
func whatsAppAlwaysOn() bool { return true }

// whatsAppSwitch is a whatsAppOn an operator can flip while a tick runs.
type whatsAppSwitch struct{ off atomic.Bool }

func (s *whatsAppSwitch) on() bool { return !s.off.Load() }

// switchingWhatsAppSender answers like fakeWhatsAppSender, and turns WhatsApp off while it
// handles call number offAtCall (counted from 1), as if the operator did it during that send.
type switchingWhatsAppSender struct {
	fakeWhatsAppSender
	offAtCall int
	sw        *whatsAppSwitch
}

func (s *switchingWhatsAppSender) SendTemplateMessage(ctx context.Context, to, name, lang string, params map[string]string) (string, error) {
	wamid, err := s.fakeWhatsAppSender.SendTemplateMessage(ctx, to, name, lang, params)
	if s.callCount() == s.offAtCall {
		s.sw.off.Store(true)
	}
	return wamid, err
}

func runWhatsAppHandlerWithSwitch(db *schedulerTestDB, sender whatsAppSender, whatsAppOn func() bool, maxAge int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	handleOutgoingWhatsAppForInstance(db.service, schedulerTestInstanceID, sender, whatsAppOn, 60, defaultWhatsAppTemplateRetryDelay, maxAge, &wg)
	wg.Wait()
}

// Turned off during a send, the tick sends nothing more: neither the rest of the batch nor
// the batches after it. The messages left are not charged and stay queued for when WhatsApp is
// on again (or for the expiry).
// Fails if the check is dropped, or only made once per batch or once per tick.
func TestWhatsAppTurnedOffMidTickStopsBeforeTheNextSend(t *testing.T) {
	for _, tt := range []struct {
		name      string
		offAtCall int
	}{
		{"in the first batch", 1},
		{"in a later batch", outgoingBatchSize + 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const queued = outgoingBatchSize*2 + 5
			db := newSchedulerTestDB(t)
			db.add(t, queued, 0)
			sw := &whatsAppSwitch{}
			sender := &switchingWhatsAppSender{offAtCall: tt.offAtCall, sw: sw}
			warnings := captureWarningLog(t)

			runWhatsAppHandlerWithSwitch(db, sender, sw.on, defaultWhatsAppMaxAge)

			if got := sender.callCount(); got != tt.offAtCall {
				t.Fatalf("the tick sent %d messages, want %d: none after WhatsApp was turned off", got, tt.offAtCall)
			}
			if outgoing, sent := db.counts(t); outgoing != int64(queued-tt.offAtCall) || sent != int64(tt.offAtCall) {
				t.Fatalf("outgoing=%d sent=%d, want %d/%d", outgoing, sent, queued-tt.offAtCall, tt.offAtCall)
			}
			if charged := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
				t.Fatalf("%d messages left by the stop were charged an attempt", charged)
			}
			if !strings.Contains(warnings.String(), "WhatsApp was turned off for this instance") {
				t.Fatalf("the stop is not logged, warnings: %q", warnings.String())
			}
		})
	}
}

// Off from the start, the tick still finishes the rows that need no call to Meta and come
// before the first one it would send: an expired message is archived as expired, a message Meta
// already accepted is archived as delivered. The message that would be sent is left untouched.
// Fails if the check moves above the delivered or expiry branch, or lets the send through.
func TestWhatsAppTurnedOffStillFinishesRowsThatNeedNoSend(t *testing.T) {
	const expiredPhone, deliveredPhone, pendingPhone = "+391111111201", "+391111111202", "+391111111203"
	db := newSchedulerTestDB(t)
	// Claimed oldest first, so the pending message is the last of the batch.
	db.addAged(t, expiredPhone, testMaxAge+60, 0)
	db.addAged(t, deliveredPhone, 120, 0)
	db.addAged(t, pendingPhone, 60, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateOne(ctx, bson.M{"toPhoneNumber": deliveredPhone}, bson.M{
		"$set": bson.M{"delivered": true, "wamid": "wamid.before-off"},
	}); err != nil {
		t.Fatalf("mark the message as delivered: %v", err)
	}
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandlerWithSwitch(db, sender, func() bool { return false }, testMaxAge)

	if sender.callCount() != 0 {
		t.Fatalf("the tick called Meta %d times with WhatsApp off", sender.callCount())
	}
	if row := db.archivedFor(t, expiredPhone); row.Status != types.WhatsAppStatusExpired {
		t.Fatalf("the expired message was archived as %q, want %q", row.Status, types.WhatsAppStatusExpired)
	}
	if row := db.archivedFor(t, deliveredPhone); row.Status != types.WhatsAppStatusDelivered {
		t.Fatalf("the delivered message was archived as %q, want %q", row.Status, types.WhatsAppStatusDelivered)
	}
	if db.queuedFor(t, pendingPhone) != 1 || db.archivedRowsFor(t, pendingPhone) != 0 {
		t.Fatalf("the message that would have been sent left the queue")
	}
	if got := db.attemptsOf(t, pendingPhone); got != 0 {
		t.Fatalf("the message that would have been sent was charged: sendAttempt %d", got)
	}
}

// A transient failure is held until the next answer from Meta decides it. Turned off during
// that send, the tick stops before the next one, so the held failure stays undecided and is
// not charged, as after every other stop.
// Fails if the stop charges the held failure.
func TestWhatsAppTurnedOffLeavesAHeldFailureUncharged(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 2, 0)
	sw := &whatsAppSwitch{}
	sender := &switchingWhatsAppSender{
		fakeWhatsAppSender: fakeWhatsAppSender{err: &waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}},
		offAtCall:          1,
		sw:                 sw,
	}
	warnings := captureWarningLog(t)

	runWhatsAppHandlerWithSwitch(db, sender, sw.on, defaultWhatsAppMaxAge)

	if sender.callCount() != 1 {
		t.Fatalf("the tick called Meta %d times, want 1", sender.callCount())
	}
	if outgoing, sent := db.counts(t); outgoing != 2 || sent != 0 {
		t.Fatalf("outgoing=%d sent=%d, want both messages still queued", outgoing, sent)
	}
	if charged := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("the stop charged %d messages, want none", charged)
	}
	if !strings.Contains(warnings.String(), "left undecided by the stop") {
		t.Fatalf("the undecided held failure is not reported, warnings: %q", warnings.String())
	}
}
