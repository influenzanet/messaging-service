package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// A schedule that has reached its end date is deleted, and the run goes on with the other
// schedules of the instance and with the other instances: one ended schedule must not stop
// every schedule after it from advancing. The schedules use a deprecated type, which the
// generator only logs, so the run needs no other service.
// Fails if the end of one schedule ends the whole run, or the run stops waiting for its
// generators.
func TestAutoMessagesAnEndedScheduleDoesNotStopTheOthers(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := newSchedulerTestGlobalDB(t)
	// One letter, like schedulerTestInstanceID: the test database prefix leaves no room for
	// more under MongoDB's 63-character limit on database names.
	const otherInstanceID = "j"
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	insertSchedulerTestInstance(t, gdb, otherInstanceID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.service.DBClient.Database(db.prefix + otherInstanceID + "_messageDB").Drop(ctx); err != nil {
			t.Errorf("drop the database of %s: %v", otherInstanceID, err)
		}
	})

	now := time.Now().Unix()
	const period = 3600
	save := func(instanceID, label string, until int64) types.AutoMessage {
		t.Helper()
		saved, err := db.service.SaveAutoMessage(instanceID, types.AutoMessage{
			Type:     "scheduled-participant-messages",
			Label:    label,
			NextTime: now - 10,
			Period:   period,
			Until:    until,
		}, false)
		if err != nil {
			t.Fatalf("save the schedule %s: %v", label, err)
		}
		return saved
	}
	// Due now; its next run would fall after its end date.
	ended := save(schedulerTestInstanceID, "ended", now)
	sameInstance := save(schedulerTestInstanceID, "same instance", 0)
	otherInstance := save(otherInstanceID, "other instance", 0)

	infos := captureLog(t, logger.Info)

	handleAutoMessages(db.service, gdb, &types.APIClients{})

	nextTimeOf := func(instanceID string, msg types.AutoMessage) (int64, bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var stored types.AutoMessage
		err := db.service.DBClient.Database(db.prefix+instanceID+"_messageDB").Collection("auto-messages").
			FindOne(ctx, bson.M{"_id": msg.ID}).Decode(&stored)
		if err != nil {
			return 0, false
		}
		return stored.NextTime, true
	}

	if _, found := nextTimeOf(schedulerTestInstanceID, ended); found {
		t.Error("the ended schedule was not deleted")
	}
	for _, c := range []struct {
		instanceID string
		msg        types.AutoMessage
	}{{schedulerTestInstanceID, sameInstance}, {otherInstanceID, otherInstance}} {
		next, found := nextTimeOf(c.instanceID, c.msg)
		if !found {
			t.Fatalf("the schedule %q disappeared", c.msg.Label)
		}
		if next != now-10+period {
			t.Errorf("the schedule %q was not advanced: nextTime %d, want %d", c.msg.Label, next, now-10+period)
		}
	}
	if !strings.Contains(infos.String(), "finished: fetching and sending scheduled auto messages") {
		t.Errorf("the run did not finish after waiting for its generators: %q", infos.String())
	}
}
