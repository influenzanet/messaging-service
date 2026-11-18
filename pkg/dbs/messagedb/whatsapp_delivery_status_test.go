package messagedb

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

func archiveWithWAMID(t *testing.T, instanceID, wamid, userID string) {
	t.Helper()
	msg := types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: "+393331234567", TemplateName: "t", Lang: "it",
		UserID: userID, WAMID: wamid,
	}
	if _, err := testDBService.AddToSentWhatsApp(instanceID, msg, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}); err != nil {
		t.Fatal(err)
	}
}

func rawArchived(t *testing.T, instanceID, wamid string) bson.M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row bson.M
	if err := testDBService.collectionRefSentWhatsApp(instanceID).FindOne(ctx, bson.M{"wamid": wamid}).Decode(&row); err != nil {
		t.Fatalf("read the archived row: %v", err)
	}
	return row
}

type deliveryStatus struct {
	status string
	at     int64
	code   int
}

// Meta's statuses can arrive in any order; the row keeps the highest one with its own time,
// failed being final. The outcome of the queue (status "delivered") is never touched.
// Fails if a later-ranked status is overwritten, if a repeated one moves the time, or if the
// error code is lost or written on a status that is not a failure.
func TestRecordWhatsAppDeliveryStatusKeepsTheHighest(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	for i, c := range []struct {
		name     string
		statuses []deliveryStatus
		want     deliveryStatus
	}{
		{"in order", []deliveryStatus{{"sent", 10, 0}, {"delivered", 11, 0}, {"read", 12, 0}}, deliveryStatus{"read", 12, 0}},
		{"read before the others", []deliveryStatus{{"read", 12, 0}, {"delivered", 11, 0}, {"sent", 10, 0}}, deliveryStatus{"read", 12, 0}},
		{"the same status again keeps its first time", []deliveryStatus{{"delivered", 11, 0}, {"delivered", 15, 0}}, deliveryStatus{"delivered", 11, 0}},
		{"failed after delivered", []deliveryStatus{{"delivered", 11, 0}, {"failed", 13, 131026}}, deliveryStatus{"failed", 13, 131026}},
		{"failed is final", []deliveryStatus{{"failed", 13, 131049}, {"read", 14, 0}, {"sent", 10, 0}}, deliveryStatus{"failed", 13, 131049}},
		{"a status not ranked writes nothing", []deliveryStatus{{"played", 20, 0}}, deliveryStatus{}},
		{"an error code on a status that is not a failure is not kept", []deliveryStatus{{"sent", 10, 131026}}, deliveryStatus{"sent", 10, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wamid := fmt.Sprintf("wamid.order%d", i)
			archiveWithWAMID(t, instanceID, wamid, "user-1")
			for _, s := range c.statuses {
				userID, found, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, wamid, s.status, s.at, s.code)
				if err != nil {
					t.Fatal(err)
				}
				if types.WhatsAppDeliveryStatusRank(s.status) > 0 && (!found || userID != "user-1") {
					t.Fatalf("%s: found=%v user=%q, want the row of user-1 whether or not it changed", s.status, found, userID)
				}
			}
			row := rawArchived(t, instanceID, wamid)
			if c.want.status == "" {
				for _, field := range []string{"metaStatus", "metaStatusAt", "metaErrorCode"} {
					if _, present := row[field]; present {
						t.Errorf("%s written: %v", field, row[field])
					}
				}
				return
			}
			code, _ := row["metaErrorCode"].(int32)
			if row["metaStatus"] != c.want.status || row["metaStatusAt"] != c.want.at || int(code) != c.want.code {
				t.Errorf("row: metaStatus=%v metaStatusAt=%v metaErrorCode=%v, want %+v", row["metaStatus"], row["metaStatusAt"], row["metaErrorCode"], c.want)
			}
			if c.want.code == 0 {
				if _, present := row["metaErrorCode"]; present {
					t.Errorf("metaErrorCode written on a %s row", c.want.status)
				}
			}
			if row["status"] != types.WhatsAppStatusDelivered {
				t.Errorf("the queue outcome changed to %v", row["status"])
			}
		})
	}
}

func TestRecordWhatsAppDeliveryStatusUnknownMessage(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	// A row without any wamid: an empty id must not reach it.
	if _, err := testDBService.AddToSentWhatsApp(instanceID, types.OutgoingWhatsApp{UserID: "user-2", TemplateName: "t"},
		types.WhatsAppSendOutcome{Status: types.WhatsAppStatusFailed}); err != nil {
		t.Fatal(err)
	}
	for _, wamid := range []string{"wamid.unknown", ""} {
		userID, found, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, wamid, "failed", 10, 131026)
		if err != nil || found || userID != "" {
			t.Errorf("wamid %q: found=%v user=%q err=%v, want nothing found", wamid, found, userID, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := testDBService.collectionRefSentWhatsApp(instanceID).CountDocuments(ctx, bson.M{"metaStatus": bson.M{"$exists": true}})
	if err != nil || n != 0 {
		t.Errorf("%d rows got a status (err %v), want none", n, err)
	}
}

// Statuses of one message processed at the same time: the highest is what stays, whatever
// the order the writes land in.
func TestRecordWhatsAppDeliveryStatusConcurrent(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	for round := 0; round < 10; round++ {
		wamid := fmt.Sprintf("wamid.concurrent%d", round)
		archiveWithWAMID(t, instanceID, wamid, "user-3")
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, s := range []string{"read", "sent", "delivered", "sent", "delivered", "read"} {
			wg.Add(1)
			go func(i int, s string) {
				defer wg.Done()
				<-start
				if _, _, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, wamid, s, int64(100+i), 0); err != nil {
					t.Error(err)
				}
			}(i, s)
		}
		close(start)
		wg.Wait()
		if got := rawArchived(t, instanceID, wamid)["metaStatus"]; got != "read" {
			t.Fatalf("round %d: metaStatus=%v, want read", round, got)
		}
	}
}
