package messagedb

import (
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
)

// The queue size splits the queue into deferred messages (claimed until after now: a refused
// template's wait) and waiting ones, counts the waiting ones queued before the stale limit, and
// dates the oldest waiting one. A message a tick holds at now is waiting, not deferred, and a
// message queued exactly at the stale limit is not stale yet.
// Fails if deferred messages are counted as stale or dated as the oldest, if a held claim is
// read as a deferral, or if either boundary moves by one second.
func TestGetWhatsAppQueueDepth(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	now := time.Now().Unix()
	staleBefore := now - 3600

	for _, row := range []struct {
		addedAt, lastSendAttempt int64
	}{
		{now - 100, 0},              // waiting, recent
		{staleBefore, 0},            // waiting, exactly at the limit: not stale
		{staleBefore - 1, now - 10}, // held by a tick, one second past the limit: stale
		{now - 50, now},             // claimed at now: waiting
		{now - 9000, now + 3000},    // deferred, the oldest of all: neither stale nor oldest waiting
		{now - 200, now + 1},        // deferred by one second
	} {
		if _, err := testDBService.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
			MessageType: "weekly", ToPhoneNumber: "+393330000001", TemplateName: "t", Lang: "it",
			AddedAt: row.addedAt, LastSendAttempt: row.lastSendAttempt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	depth, err := testDBService.GetWhatsAppQueueDepth(instanceID, now, staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	want := WhatsAppQueueDepth{Queued: 6, Deferred: 2, Stale: 1, OldestWaitingAddedAt: staleBefore - 1}
	if depth != want {
		t.Fatalf("queue depth = %+v, want %+v", depth, want)
	}
	if depth.Waiting() != 4 {
		t.Fatalf("waiting = %d, want 4", depth.Waiting())
	}
}

// An empty queue, or one that has never existed, reads as zero everywhere, without an error.
func TestGetWhatsAppQueueDepthOfAnEmptyQueue(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	depth, err := testDBService.GetWhatsAppQueueDepth(instanceID, time.Now().Unix(), time.Now().Unix()-3600)
	if err != nil {
		t.Fatal(err)
	}
	if depth != (WhatsAppQueueDepth{}) || depth.Waiting() != 0 {
		t.Fatalf("queue depth of an empty queue = %+v", depth)
	}
}
