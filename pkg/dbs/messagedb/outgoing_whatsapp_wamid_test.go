package messagedb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func rawQueuedWhatsApp(t *testing.T, instanceID string, id primitive.ObjectID) bson.M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row bson.M
	if err := testDBService.collectionRefOutgoingWhatsApp(instanceID).FindOne(ctx, bson.M{"_id": id}).Decode(&row); err != nil {
		t.Fatalf("read the queued row: %v", err)
	}
	return row
}

// The delivered mark stores Meta's id next to the flag, so that a message whose archive write
// fails keeps its id for the tick that finishes the archive. With no id, the mark writes no
// wamid field, and a mark on a row that has left the queue is reported as such.
// Fails if the id is dropped, if an empty wamid is written, or if a mark on a missing row is
// taken for a success.
func TestMarkOutgoingWhatsAppDeliveredRecordsTheID(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	queue := func() types.OutgoingWhatsApp {
		msg, err := testDBService.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
			MessageType: "weekly", ToPhoneNumber: "+393331234567", TemplateName: "t", Lang: "it",
		})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	t.Run("with an id", func(t *testing.T) {
		msg := queue()
		if err := testDBService.MarkOutgoingWhatsAppDelivered(instanceID, msg.ID.Hex(), "wamid.A"); err != nil {
			t.Fatal(err)
		}
		row := rawQueuedWhatsApp(t, instanceID, msg.ID)
		if row["delivered"] != true || row["wamid"] != "wamid.A" {
			t.Fatalf("marked row: delivered=%v wamid=%v, want true and wamid.A", row["delivered"], row["wamid"])
		}
	})

	t.Run("without an id", func(t *testing.T) {
		msg := queue()
		if err := testDBService.MarkOutgoingWhatsAppDelivered(instanceID, msg.ID.Hex(), ""); err != nil {
			t.Fatal(err)
		}
		row := rawQueuedWhatsApp(t, instanceID, msg.ID)
		if row["delivered"] != true {
			t.Fatalf("marked row: delivered=%v, want true", row["delivered"])
		}
		if wamid, ok := row["wamid"]; ok {
			t.Fatalf("a mark without an id wrote the wamid field %q", wamid)
		}
	})

	t.Run("row no longer queued", func(t *testing.T) {
		err := testDBService.MarkOutgoingWhatsAppDelivered(instanceID, primitive.NewObjectID().Hex(), "wamid.B")
		if !errors.Is(err, ErrOutgoingWhatsAppNotFound) {
			t.Fatalf("mark of a missing row: %v, want ErrOutgoingWhatsAppNotFound", err)
		}
	})
}

// The archive keeps Meta's id while it strips the number and the parameters: the id is what
// the delivery statuses are matched on, and it carries no personal data.
// Fails if the minimisation of the archive row drops the wamid with the other fields.
func TestSentWhatsAppKeepsTheID(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	if _, err := testDBService.AddToSentWhatsApp(instanceID, types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: "+393331234567", TemplateName: "t", Lang: "it",
		ContentParams: map[string]string{"name": "Mario"}, Delivered: true, WAMID: "wamid.A",
	}, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row bson.M
	if err := testDBService.collectionRefSentWhatsApp(instanceID).FindOne(ctx, bson.M{"wamid": "wamid.A"}).Decode(&row); err != nil {
		t.Fatalf("find the archived row by its wamid: %v", err)
	}
	if row["status"] != types.WhatsAppStatusDelivered {
		t.Fatalf("archived row status = %v, want delivered", row["status"])
	}
}
