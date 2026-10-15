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

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/internal/config"
	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
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
	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, config.WhatsAppSettings{})

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

	handleOutgoingWhatsApp(db.service, gdb, nil, 60, defaultWhatsAppTemplateRetryDelay, testSchedulerWhatsAppSettings(true))

	if !strings.Contains(infos.String(), "Finished processing 0 whatsapp messages") {
		t.Errorf("expected the instance to reach handleOutgoingWhatsAppForInstance, got %q", infos.String())
	}
}
