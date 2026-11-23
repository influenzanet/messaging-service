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
	if err := testDBService.collectionRefSentWhatsApp(instanceID).FindOne(ctx, bson.M{"wamidHash": types.WAMIDHash(wamid)}).Decode(&row); err != nil {
		t.Fatalf("read the archived row: %v", err)
	}
	return row
}

type deliveryStatus struct {
	status string
	at     int64
	code   int
}

const (
	recorded   = types.DeliveryStatusRecorded
	unchanged  = types.DeliveryStatusUnchanged
	notTracked = types.DeliveryStatusNotTracked
)

// Meta's statuses can arrive in any order; the row keeps the highest one with its own time,
// failed being final. The outcome of the queue (status "delivered") is never touched. Each call
// says whether it changed the row, which is what the webhook logs.
// Fails if a later-ranked status is overwritten, if a repeated one moves the time, if the
// error code is lost or written on a status that is not a failure, if a call reports a change
// it did not make (or the other way round), or if it does not name the row's account.
func TestRecordWhatsAppDeliveryStatusKeepsTheHighest(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	for i, c := range []struct {
		name     string
		statuses []deliveryStatus
		results  []types.DeliveryStatusResult
		want     deliveryStatus
	}{
		{"in order", []deliveryStatus{{"sent", 10, 0}, {"delivered", 11, 0}, {"read", 12, 0}},
			[]types.DeliveryStatusResult{recorded, recorded, recorded}, deliveryStatus{"read", 12, 0}},
		{"read before the others", []deliveryStatus{{"read", 12, 0}, {"delivered", 11, 0}, {"sent", 10, 0}},
			[]types.DeliveryStatusResult{recorded, unchanged, unchanged}, deliveryStatus{"read", 12, 0}},
		{"the same status again keeps its first time", []deliveryStatus{{"delivered", 11, 0}, {"delivered", 15, 0}},
			[]types.DeliveryStatusResult{recorded, unchanged}, deliveryStatus{"delivered", 11, 0}},
		{"failed after delivered", []deliveryStatus{{"delivered", 11, 0}, {"failed", 13, 131026}},
			[]types.DeliveryStatusResult{recorded, recorded}, deliveryStatus{"failed", 13, 131026}},
		{"failed is final", []deliveryStatus{{"failed", 13, 131049}, {"read", 14, 0}, {"sent", 10, 0}},
			[]types.DeliveryStatusResult{recorded, unchanged, unchanged}, deliveryStatus{"failed", 13, 131049}},
		{"a status not ranked writes nothing", []deliveryStatus{{"played", 20, 0}},
			[]types.DeliveryStatusResult{notTracked}, deliveryStatus{}},
		{"an error code on a status that is not a failure is not kept", []deliveryStatus{{"sent", 10, 131026}},
			[]types.DeliveryStatusResult{recorded}, deliveryStatus{"sent", 10, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wamid := fmt.Sprintf("wamid.order%d", i)
			archiveWithWAMID(t, instanceID, wamid, "user-1")
			for j, s := range c.statuses {
				userID, result, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, wamid, s.status, s.at, s.code)
				if err != nil {
					t.Fatal(err)
				}
				if result != c.results[j] {
					t.Fatalf("%s: result %d, want %d", s.status, result, c.results[j])
				}
				wantUser := "user-1"
				if result == notTracked {
					wantUser = ""
				}
				if userID != wantUser || result.Known() != (wantUser != "") {
					t.Fatalf("%s: user=%q known=%v, want the row of user-1 whether or not it changed", s.status, userID, result.Known())
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
		userID, result, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, wamid, "failed", 10, 131026)
		if err != nil || result != types.DeliveryStatusUnknownMessage || result.Known() || userID != "" {
			t.Errorf("wamid %q: result=%d user=%q err=%v, want an unknown message", wamid, result, userID, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := testDBService.collectionRefSentWhatsApp(instanceID).CountDocuments(ctx, bson.M{"metaStatus": bson.M{"$exists": true}})
	if err != nil || n != 0 {
		t.Errorf("%d rows got a status (err %v), want none", n, err)
	}
}

// Rows archived before the id was hashed carry the raw wamid and no hash. They are not
// migrated: their statuses are no longer matched and the retention index removes them.
// Fails if such a row is matched on its raw id (the lookup would have to keep reading the
// field that holds the number), changed, or makes the call fail.
func TestRecordWhatsAppDeliveryStatusIgnoresRowsWithTheRawID(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent := testDBService.collectionRefSentWhatsApp(instanceID)
	if _, err := sent.InsertOne(ctx, bson.M{"userId": "user-old", "status": types.WhatsAppStatusDelivered, "wamid": "wamid.old"}); err != nil {
		t.Fatal(err)
	}
	userID, result, err := testDBService.RecordWhatsAppDeliveryStatus(instanceID, "wamid.old", "read", 10, 0)
	if err != nil || result != types.DeliveryStatusUnknownMessage || userID != "" {
		t.Fatalf("result=%d user=%q err=%v, want an unknown message", result, userID, err)
	}
	var row bson.M
	if err := sent.FindOne(ctx, bson.M{"wamid": "wamid.old"}).Decode(&row); err != nil {
		t.Fatal(err)
	}
	if _, present := row["metaStatus"]; present {
		t.Errorf("the old row got a status: %v", row["metaStatus"])
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
