package messaging_service

import (
	"context"
	"testing"
	"time"

	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeleteOutgoingWhatsAppForUser is user-management's way to stop WhatsApp messages queued for
// a participant who withdrew (D11). The DB package tests the queue and archive semantics; these
// tests pin the request contract and the outcome user-management sees.

func queueWhatsAppForEndpointTest(t *testing.T, userID, messageType string) {
	t.Helper()
	if _, err := testMessageDBService.AddToOutgoingWhatsApp(testInstanceID, types.OutgoingWhatsApp{
		MessageType:  messageType,
		TemplateName: "influenzanet_" + messageType + "_v1",
		Lang:         "it",
		UserID:       userID,
	}); err != nil {
		t.Fatal(err)
	}
}

func whatsAppRowsOf(t *testing.T, collection, userID string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := testMessageDBService.DBClient.Database(testDBNamePrefix+testInstanceID+"_messageDB").
		Collection(collection).CountDocuments(ctx, bson.M{"userId": userID})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteOutgoingWhatsAppForUserInvalidRequests(t *testing.T) {
	const user = "endpoint-user-invalid"
	queueWhatsAppForEndpointTest(t, user, "weekly")
	s := messagingServer{messageDBservice: testMessageDBService}

	for name, req := range map[string]*api.DeleteOutgoingWhatsAppForUserReq{
		"nil request":         nil,
		"no instance":         {UserId: user},
		"no user":             {InstanceId: testInstanceID},
		"purge with a filter": {InstanceId: testInstanceID, UserId: user, PurgeArchive: true, MessageTypes: []string{"newsletter"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.DeleteOutgoingWhatsAppForUser(context.Background(), req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("error = %v, want InvalidArgument", err)
			}
		})
	}
	if n := whatsAppRowsOf(t, "outgoing-whatsapp", user); n != 1 {
		t.Errorf("a refused request changed the queue: %d rows, want 1", n)
	}
}

func TestDeleteOutgoingWhatsAppForUserCancels(t *testing.T) {
	const user, other = "endpoint-user-cancel", "endpoint-user-cancel-other"
	queueWhatsAppForEndpointTest(t, user, "weekly")
	queueWhatsAppForEndpointTest(t, user, "newsletter")
	queueWhatsAppForEndpointTest(t, other, "newsletter")
	s := messagingServer{messageDBservice: testMessageDBService}

	// A subscription turned off: only that type goes.
	resp, err := s.DeleteOutgoingWhatsAppForUser(context.Background(), &api.DeleteOutgoingWhatsAppForUserReq{
		InstanceId: testInstanceID, UserId: user, MessageTypes: []string{"newsletter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RemovedFromQueue != 1 || resp.RemovedFromArchive != 0 {
		t.Errorf("response = %+v, want 1 removed from the queue and none from the archive", resp)
	}
	if n := whatsAppRowsOf(t, "outgoing-whatsapp", user); n != 1 {
		t.Errorf("%d rows left in the queue, want the weekly one", n)
	}

	// WhatsApp withdrawn: every type goes, archived as cancelled.
	resp, err = s.DeleteOutgoingWhatsAppForUser(context.Background(), &api.DeleteOutgoingWhatsAppForUserReq{
		InstanceId: testInstanceID, UserId: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RemovedFromQueue != 1 {
		t.Errorf("RemovedFromQueue = %d, want 1", resp.RemovedFromQueue)
	}
	if n := whatsAppRowsOf(t, "outgoing-whatsapp", user); n != 0 {
		t.Errorf("%d rows left in the queue, want 0", n)
	}
	if n := whatsAppRowsOf(t, "sent-whatsapp", user); n != 2 {
		t.Errorf("%d archive rows, want the 2 cancelled ones", n)
	}
	if n := whatsAppRowsOf(t, "outgoing-whatsapp", other); n != 1 {
		t.Errorf("the other participant's queue has %d rows, want 1", n)
	}

	// The retry user-management sends when it did not see the first answer.
	resp, err = s.DeleteOutgoingWhatsAppForUser(context.Background(), &api.DeleteOutgoingWhatsAppForUserReq{
		InstanceId: testInstanceID, UserId: user,
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if resp.RemovedFromQueue != 0 {
		t.Errorf("retry removed %d, want 0", resp.RemovedFromQueue)
	}
}

func TestDeleteOutgoingWhatsAppForUserPurgesOnAccountDeletion(t *testing.T) {
	const user = "endpoint-user-purge"
	queueWhatsAppForEndpointTest(t, user, "weekly")
	if _, err := testMessageDBService.AddToSentWhatsApp(testInstanceID,
		types.OutgoingWhatsApp{MessageType: "weekly", UserID: user},
		types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}); err != nil {
		t.Fatal(err)
	}
	s := messagingServer{messageDBservice: testMessageDBService}

	resp, err := s.DeleteOutgoingWhatsAppForUser(context.Background(), &api.DeleteOutgoingWhatsAppForUserReq{
		InstanceId: testInstanceID, UserId: user, PurgeArchive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RemovedFromQueue != 1 || resp.RemovedFromArchive != 1 {
		t.Errorf("response = %+v, want 1 and 1", resp)
	}
	if q, a := whatsAppRowsOf(t, "outgoing-whatsapp", user), whatsAppRowsOf(t, "sent-whatsapp", user); q != 0 || a != 0 {
		t.Errorf("after the purge %d queued and %d archived rows, want none (no cancelled row either)", q, a)
	}
}

func TestDeleteOutgoingWhatsAppForUserDatabaseDown(t *testing.T) {
	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:1"))
	if err != nil {
		t.Fatal(err)
	}
	s := messagingServer{messageDBservice: &messagedb.MessageDBService{DBClient: client, DBNamePrefix: testDBNamePrefix}}

	for name, req := range map[string]*api.DeleteOutgoingWhatsAppForUserReq{
		"cancel": {InstanceId: testInstanceID, UserId: "any-user"},
		"purge":  {InstanceId: testInstanceID, UserId: "any-user", PurgeArchive: true},
	} {
		t.Run(name, func(t *testing.T) {
			// user-management retries on this answer, so it must be an error, not a zero count.
			if _, err := s.DeleteOutgoingWhatsAppForUser(context.Background(), req); status.Code(err) != codes.Internal {
				t.Errorf("error = %v, want Internal", err)
			}
		})
	}
}
