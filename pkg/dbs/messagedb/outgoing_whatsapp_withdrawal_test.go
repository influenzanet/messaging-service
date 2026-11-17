package messagedb

import (
	"context"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Withdrawal of WhatsApp (D11): user-management calls DeleteOutgoingWhatsAppForUser whenever a
// participant withdraws, and retries it. These tests pin what the two DB methods behind it do
// to the queue and the archive, on a real MongoDB.

func queueWhatsAppForTest(t *testing.T, instanceID, userID, messageType string, delivered bool) {
	t.Helper()
	_, err := testDBService.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
		MessageType:   messageType,
		ToPhoneNumber: "+393331234567",
		TemplateName:  "influenzanet_" + messageType + "_v1",
		Lang:          "it",
		ContentParams: map[string]string{"loginToken": "secret-token"},
		UserID:        userID,
		Delivered:     delivered,
	})
	if err != nil {
		t.Fatalf("queueing a message: %v", err)
	}
}

func queuedWhatsAppOf(t *testing.T, instanceID, userID string) []types.OutgoingWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := testDBService.collectionRefOutgoingWhatsApp(instanceID).Find(ctx, bson.M{"userId": userID})
	if err != nil {
		t.Fatal(err)
	}
	var rows []types.OutgoingWhatsApp
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func archivedWhatsAppOf(t *testing.T, instanceID, userID string) []types.SentWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := testDBService.collectionRefSentWhatsApp(instanceID).Find(ctx, bson.M{"userId": userID})
	if err != nil {
		t.Fatal(err)
	}
	var rows []types.SentWhatsApp
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func messageTypesOf[T any](rows []T, messageType func(T) string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, messageType(r))
	}
	sort.Strings(out)
	return out
}

func queuedTypes(rows []types.OutgoingWhatsApp) []string {
	return messageTypesOf(rows, func(r types.OutgoingWhatsApp) string { return r.MessageType })
}

func archivedTypes(rows []types.SentWhatsApp) []string {
	return messageTypesOf(rows, func(r types.SentWhatsApp) string { return r.MessageType })
}

func TestCancelOutgoingWhatsAppForUser(t *testing.T) {
	const userA, userB = "cancel-user-a", "cancel-user-b"
	queueWhatsAppForTest(t, testInstanceID, userA, "weekly", false)
	queueWhatsAppForTest(t, testInstanceID, userA, "newsletter", false)
	// Meta already accepted this one; the scheduler still has to archive it as delivered.
	queueWhatsAppForTest(t, testInstanceID, userA, "study-reminder", true)
	queueWhatsAppForTest(t, testInstanceID, userB, "weekly", false)

	n, err := testDBService.CancelOutgoingWhatsAppForUser(testInstanceID, userA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("cancelled %d messages, want 2", n)
	}

	t.Run("only the delivered message is left in the queue", func(t *testing.T) {
		left := queuedWhatsAppOf(t, testInstanceID, userA)
		if len(left) != 1 || left[0].MessageType != "study-reminder" || !left[0].Delivered {
			t.Errorf("queue of the participant = %+v, want only the delivered study-reminder", left)
		}
	})

	t.Run("each cancelled message is archived once as cancelled, without its parameters", func(t *testing.T) {
		archived := archivedWhatsAppOf(t, testInstanceID, userA)
		if got := archivedTypes(archived); !slices.Equal(got, []string{"newsletter", "weekly"}) {
			t.Fatalf("archived types = %v, want [newsletter weekly]", got)
		}
		for _, row := range archived {
			if row.Status != types.WhatsAppStatusCancelled {
				t.Errorf("%s archived with status %q, want %q", row.MessageType, row.Status, types.WhatsAppStatusCancelled)
			}
			if len(row.ContentParams) != 0 {
				t.Errorf("%s archived with its content parameters %v", row.MessageType, row.ContentParams)
			}
			if row.ErrorCode != 0 || row.ErrorClass != "" {
				t.Errorf("%s archived with an error (%d, %q), but it was never sent", row.MessageType, row.ErrorCode, row.ErrorClass)
			}
			if row.SentAt == 0 {
				t.Errorf("%s archived without a time", row.MessageType)
			}
		}
	})

	t.Run("another participant's messages are untouched", func(t *testing.T) {
		if left := queuedWhatsAppOf(t, testInstanceID, userB); len(left) != 1 {
			t.Errorf("queue of the other participant has %d messages, want 1", len(left))
		}
		if archived := archivedWhatsAppOf(t, testInstanceID, userB); len(archived) != 0 {
			t.Errorf("the other participant has %d archived messages, want 0", len(archived))
		}
	})

	t.Run("a second call changes nothing", func(t *testing.T) {
		n, err := testDBService.CancelOutgoingWhatsAppForUser(testInstanceID, userA, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("second call cancelled %d messages, want 0", n)
		}
		if archived := archivedWhatsAppOf(t, testInstanceID, userA); len(archived) != 2 {
			t.Errorf("after the second call %d archived rows, want still 2", len(archived))
		}
		if left := queuedWhatsAppOf(t, testInstanceID, userA); len(left) != 1 {
			t.Errorf("after the second call %d queued rows, want still 1", len(left))
		}
	})
}

func TestCancelOutgoingWhatsAppForUserByMessageType(t *testing.T) {
	const user = "cancel-user-types"
	for _, mt := range []string{"weekly", "newsletter", "study-reminder"} {
		queueWhatsAppForTest(t, testInstanceID, user, mt, false)
	}

	n, err := testDBService.CancelOutgoingWhatsAppForUser(testInstanceID, user, []string{"newsletter", "study-reminder"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("cancelled %d messages, want 2", n)
	}
	if got := queuedTypes(queuedWhatsAppOf(t, testInstanceID, user)); !slices.Equal(got, []string{"weekly"}) {
		t.Errorf("still queued = %v, want [weekly]: a subscription turned off must not cancel the others", got)
	}
	if got := archivedTypes(archivedWhatsAppOf(t, testInstanceID, user)); !slices.Equal(got, []string{"newsletter", "study-reminder"}) {
		t.Errorf("archived = %v, want [newsletter study-reminder]", got)
	}
}

func TestCancelOutgoingWhatsAppForUserRequiresAUser(t *testing.T) {
	// A row with no userId (written before the field existed) must not be matched by a call
	// that lost its user ID on the way.
	queueWhatsAppForTest(t, testInstanceID, "", "weekly-without-user", false)

	if _, err := testDBService.CancelOutgoingWhatsAppForUser(testInstanceID, "", nil); err == nil {
		t.Error("cancel with no user ID succeeded, want an error")
	}
	if _, _, err := testDBService.PurgeWhatsAppForUser(testInstanceID, ""); err == nil {
		t.Error("purge with no user ID succeeded, want an error")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := testDBService.collectionRefOutgoingWhatsApp(testInstanceID).CountDocuments(ctx, bson.M{"messageType": "weekly-without-user"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("the row without a user ID was removed (count %d)", count)
	}
}

// User-management retries the call, so a retry can overlap the first attempt. Between them,
// every message must be taken out of the queue and archived exactly once.
func TestCancelOutgoingWhatsAppForUserConcurrentCalls(t *testing.T) {
	const user = "cancel-user-concurrent"
	const messages = 30
	for i := 0; i < messages; i++ {
		queueWhatsAppForTest(t, testInstanceID, user, "weekly", false)
	}

	const callers = 6
	var wg sync.WaitGroup
	counts := make([]int64, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			counts[i], errs[i] = testDBService.CancelOutgoingWhatsAppForUser(testInstanceID, user, nil)
		}(i)
	}
	close(start)
	wg.Wait()

	var total int64
	for i := range counts {
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
		}
		total += counts[i]
	}
	if total != messages {
		t.Errorf("callers cancelled %d messages in total, want %d", total, messages)
	}
	if left := queuedWhatsAppOf(t, testInstanceID, user); len(left) != 0 {
		t.Errorf("%d messages left in the queue", len(left))
	}
	if archived := archivedWhatsAppOf(t, testInstanceID, user); len(archived) != messages {
		t.Errorf("%d archive rows, want exactly %d (one per message)", len(archived), messages)
	}
}

// If the archive refuses a write, the message already taken out of the queue stays out (it
// must not be sent), the error is reported, and a retry finishes the rest.
func TestCancelOutgoingWhatsAppForUserArchiveFailure(t *testing.T) {
	instanceID := testInstanceID + "_archive_refused"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := testDBService.DBClient.Database(testDBNamePrefix + instanceID + "_messageDB")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
	})
	// A validator no document can satisfy: every insert into the archive fails, which is what
	// a write error of the archive looks like to the method.
	if err := db.CreateCollection(ctx, "sent-whatsapp", options.CreateCollection().SetValidator(
		bson.M{"status": "no-status-matches-this"})); err != nil {
		t.Fatal(err)
	}

	const user = "cancel-user-archive-failure"
	for i := 0; i < 3; i++ {
		queueWhatsAppForTest(t, instanceID, user, "weekly", false)
	}

	n, err := testDBService.CancelOutgoingWhatsAppForUser(instanceID, user, nil)
	if err == nil {
		t.Fatal("cancel succeeded although the archive refused the write")
	}
	if n != 1 {
		t.Errorf("reported %d cancelled before the error, want 1", n)
	}
	if left := queuedWhatsAppOf(t, instanceID, user); len(left) != 2 {
		t.Fatalf("%d messages left in the queue, want 2 (the first one stays out)", len(left))
	}

	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: "sent-whatsapp"}, {Key: "validator", Value: bson.M{}}}).Err(); err != nil {
		t.Fatal(err)
	}
	n, err = testDBService.CancelOutgoingWhatsAppForUser(instanceID, user, nil)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n != 2 {
		t.Errorf("retry cancelled %d, want the 2 left", n)
	}
	if left := queuedWhatsAppOf(t, instanceID, user); len(left) != 0 {
		t.Errorf("%d messages left in the queue after the retry", len(left))
	}
}

func TestPurgeWhatsAppForUser(t *testing.T) {
	const userE, userF = "purge-user-e", "purge-user-f"
	queueWhatsAppForTest(t, testInstanceID, userE, "weekly", false)
	queueWhatsAppForTest(t, testInstanceID, userE, "newsletter", true)
	queueWhatsAppForTest(t, testInstanceID, userF, "weekly", false)
	for _, archive := range []struct {
		user    string
		outcome types.WhatsAppSendOutcome
	}{
		{userE, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}},
		{userE, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusFailed, ErrorCode: 131026, ErrorClass: "permanent"}},
		{userF, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}},
	} {
		if _, err := testDBService.AddToSentWhatsApp(testInstanceID, types.OutgoingWhatsApp{MessageType: "weekly", UserID: archive.user}, archive.outcome); err != nil {
			t.Fatal(err)
		}
	}

	queued, archived, err := testDBService.PurgeWhatsAppForUser(testInstanceID, userE)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 2 || archived != 2 {
		t.Errorf("purge deleted %d queued and %d archived, want 2 and 2", queued, archived)
	}

	t.Run("nothing of the deleted account is left, not even a cancelled row", func(t *testing.T) {
		if left := queuedWhatsAppOf(t, testInstanceID, userE); len(left) != 0 {
			t.Errorf("%d queued rows left, delivered ones included: %+v", len(left), left)
		}
		if left := archivedWhatsAppOf(t, testInstanceID, userE); len(left) != 0 {
			t.Errorf("%d archive rows left: %+v", len(left), left)
		}
	})

	t.Run("another participant keeps queue and archive", func(t *testing.T) {
		if left := queuedWhatsAppOf(t, testInstanceID, userF); len(left) != 1 {
			t.Errorf("%d queued rows for the other participant, want 1", len(left))
		}
		if left := archivedWhatsAppOf(t, testInstanceID, userF); len(left) != 1 {
			t.Errorf("%d archive rows for the other participant, want 1", len(left))
		}
	})

	t.Run("a second purge changes nothing", func(t *testing.T) {
		queued, archived, err := testDBService.PurgeWhatsAppForUser(testInstanceID, userE)
		if err != nil {
			t.Fatal(err)
		}
		if queued != 0 || archived != 0 {
			t.Errorf("second purge deleted %d and %d, want 0 and 0", queued, archived)
		}
	})
}

func TestWhatsAppWithdrawalWhenTheDatabaseIsDown(t *testing.T) {
	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:1"))
	if err != nil {
		t.Fatal(err)
	}
	down := &MessageDBService{DBClient: client, timeout: 1, DBNamePrefix: testDBNamePrefix}

	if _, err := down.CancelOutgoingWhatsAppForUser(testInstanceID, "any-user", nil); err == nil {
		t.Error("cancel reported success with the database down")
	}
	if _, _, err := down.PurgeWhatsAppForUser(testInstanceID, "any-user"); err == nil {
		t.Error("purge reported success with the database down")
	}
}
