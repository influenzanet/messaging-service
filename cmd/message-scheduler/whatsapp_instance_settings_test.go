package main

// The per-instance half of the WhatsApp activation design: handleOutgoingWhatsApp asks
// settings.Enabled(instanceID) before handing an instance to handleOutgoingWhatsAppForInstance,
// the function that actually talks to Meta. Enabled ignores instanceID today, so a disabled
// WhatsAppSettings must skip every instance the same way, and an enabled one must let every
// instance through. Meta's host is a hard-coded const (pkg/http/clients/whatsapp_client.go),
// so the "let through" half is only exercised with an empty queue: the per-instance function
// runs for real, but finds nothing to send and never builds a request. Whether a non-empty
// queue is actually delivered is already covered by the many handleOutgoingWhatsAppForInstance
// tests in this package, which inject a fake sender instead of a real client.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/internal/config"
	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// insertSchedulerTestInstance registers instanceID in gdb, the way GetAllInstances expects to
// find it.
func insertSchedulerTestInstance(t *testing.T, gdb *globaldb.GlobalDBService, instanceID string) {
	t.Helper()
	collection := gdb.DBClient.Database(gdb.DBNamePrefix + "global-infos").Collection("instances")
	if _, err := collection.InsertOne(context.Background(), bson.M{"instanceID": instanceID}); err != nil {
		t.Fatalf("insert instance %q: %v", instanceID, err)
	}
}

func TestHandleOutgoingWhatsAppSkipsEveryInstanceWhenSettingsAreDisabled(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	db.add(t, 1, 0)

	warnings := captureLog(t, logger.Warning)
	infos := captureLog(t, logger.Info)

	// wac is nil on purpose: if the skip did not hold, the first send attempt would panic on
	// a nil *waClient.WhatsAppClient receiver instead of silently reaching Meta, which is a
	// safer failure for a test than a real network call would be.
	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, config.WhatsAppSettings{})

	if !strings.Contains(warnings.String(), "WhatsApp is not enabled for this instance") {
		t.Errorf("expected the per-instance skip warning, got %q", warnings.String())
	}
	if strings.Contains(infos.String(), "Finished processing") {
		t.Errorf("expected handleOutgoingWhatsAppForInstance never to run, got %q", infos.String())
	}

	rows, err := db.service.FetchOutgoingWhatsApp(schedulerTestInstanceID, 10, 0)
	if err != nil {
		t.Fatalf("read the queue back: %v", err)
	}
	if len(rows) != 1 || rows[0].SendAttempt != 0 {
		t.Errorf("expected the message to stay queued and untouched, got %+v", rows)
	}
}

func TestHandleOutgoingWhatsAppProcessesInstancesWhenSettingsAreEnabled(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	// No message queued: the per-instance call below runs for real but has nothing to send,
	// so it never builds a request to Meta even though wac is nil.

	infos := captureLog(t, logger.Info)

	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, testSchedulerWhatsAppSettings(true))

	if !strings.Contains(infos.String(), "Finished processing 0 whatsapp messages") {
		t.Errorf("expected the instance to reach handleOutgoingWhatsAppForInstance, got %q", infos.String())
	}
}

// An instance that does not use WhatsApp is skipped on every tick. With nothing in its queue,
// the skip writes nothing above Debug: one warning per tick per such instance would bury the
// alerts.
// Fails if the skip logs a warning, an error or an info line about the instance when its queue
// is empty, or stops logging it at Debug.
func TestHandleOutgoingWhatsAppIsQuietForAnOffInstanceWithNothingQueued(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)

	warnings := captureLog(t, logger.Warning)
	errorsLog := captureErrorLog(t)
	infos := captureLog(t, logger.Info)
	debugs := captureLog(t, logger.Debug)

	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, config.WhatsAppSettings{})

	if warnings.String() != "" || errorsLog.String() != "" {
		t.Errorf("expected nothing above Debug, got warnings %q, errors %q", warnings.String(), errorsLog.String())
	}
	if strings.Contains(infos.String(), "not enabled for this instance") {
		t.Errorf("expected no info line about the skip, got %q", infos.String())
	}
	if !strings.Contains(debugs.String(), "["+schedulerTestInstanceID+"] WhatsApp is not enabled for this instance, nothing queued") {
		t.Errorf("expected the skip at Debug, got %q", debugs.String())
	}
}

// The check is per instance: of two instances where WhatsApp is off, only the one with
// messages waiting warns, and the other stays at Debug.
// Fails if the check reads another instance's queue, or one waiting queue makes every skipped
// instance warn.
func TestHandleOutgoingWhatsAppWarnsOnlyForTheOffInstanceWithMessagesWaiting(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	// One letter, like schedulerTestInstanceID: the test database prefix leaves no room for
	// more under MongoDB's 63-character limit on database names.
	const emptyInstanceID = "j"
	insertSchedulerTestInstance(t, gdb, emptyInstanceID)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.service.DBClient.Database(db.prefix + emptyInstanceID + "_messageDB").Drop(ctx); err != nil {
			t.Errorf("drop the database of %s: %v", emptyInstanceID, err)
		}
	})
	// The empty instance's database exists, with a sent message and nothing queued.
	if _, err := db.service.AddToSentWhatsApp(emptyInstanceID, types.OutgoingWhatsApp{
		MessageType: "test", ToPhoneNumber: "+391234567890", TemplateName: "test_template", Lang: "it",
	}, deliveredWhatsApp()); err != nil {
		t.Fatalf("archive a message for %s: %v", emptyInstanceID, err)
	}
	db.add(t, 1, 0)

	warnings := captureLog(t, logger.Warning)
	debugs := captureLog(t, logger.Debug)

	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, config.WhatsAppSettings{})

	if !strings.Contains(warnings.String(), "["+schedulerTestInstanceID+"] WhatsApp is not enabled for this instance, its outgoing messages stay queued") {
		t.Errorf("expected a warning for the instance with a message waiting, got %q", warnings.String())
	}
	if strings.Contains(warnings.String(), emptyInstanceID) {
		t.Errorf("expected no warning for the instance with nothing queued, got %q", warnings.String())
	}
	if !strings.Contains(debugs.String(), "["+emptyInstanceID+"] WhatsApp is not enabled for this instance, nothing queued") {
		t.Errorf("expected the empty instance at Debug, got %q", debugs.String())
	}
}

// A queue that cannot be read could hold messages that are never sent, so the skip warns with
// the error instead of falling silent.
// Fails if a failed read is taken for an empty queue.
func TestHandleOutgoingWhatsAppWarnsWhenAnOffInstanceQueueCannotBeRead(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	// A user with no right on the message database: the queue check fails.
	service := db.serviceWithPrivileges(t, "no_count", bson.A{bson.M{
		"resource": bson.M{"db": "scheduler_unrelated", "collection": ""},
		"actions":  bson.A{"find"},
	}})

	warnings := captureLog(t, logger.Warning)
	debugs := captureLog(t, logger.Debug)

	handleOutgoingWhatsApp(service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, config.WhatsAppSettings{})

	if !strings.Contains(warnings.String(), "["+schedulerTestInstanceID+"] WhatsApp is not enabled for this instance, and its outgoing-whatsapp queue cannot be read: ") {
		t.Errorf("expected a warning about the unreadable queue, got %q", warnings.String())
	}
	if strings.Contains(debugs.String(), "nothing queued") {
		t.Errorf("expected the failed read not to be reported as an empty queue, got %q", debugs.String())
	}
}
