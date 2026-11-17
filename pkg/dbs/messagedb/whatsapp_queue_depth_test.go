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

// The check behind the skipped-instance warning: it sees any message in this instance's queue,
// a deferred one too, and nothing queued for another instance.
// Fails if it reads the wrong instance, ignores deferred messages, or reports an empty or
// missing queue as holding messages.
func TestHasOutgoingWhatsApp(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	otherInstanceID := freshWhatsAppInstance(t)

	has, err := testDBService.HasOutgoingWhatsApp(instanceID)
	if err != nil || has {
		t.Fatalf("a queue that does not exist: has = %v, err = %v", has, err)
	}

	if _, err := testDBService.AddToOutgoingWhatsApp(otherInstanceID, types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: "+393330000002", TemplateName: "t", Lang: "it",
	}); err != nil {
		t.Fatal(err)
	}
	has, err = testDBService.HasOutgoingWhatsApp(instanceID)
	if err != nil || has {
		t.Fatalf("a message of another instance only: has = %v, err = %v", has, err)
	}

	// Deferred until after now, the way a refused template holds its messages.
	if _, err := testDBService.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: "+393330000003", TemplateName: "t", Lang: "it",
		LastSendAttempt: time.Now().Unix() + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	has, err = testDBService.HasOutgoingWhatsApp(instanceID)
	if err != nil || !has {
		t.Fatalf("one deferred message: has = %v, err = %v", has, err)
	}
}
