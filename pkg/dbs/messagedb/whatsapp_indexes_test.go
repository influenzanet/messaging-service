package messagedb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// retentionSeconds is the archive's retention as the index stores it: 180 days.
const retentionSeconds = 180 * 24 * 3600

// freshWhatsAppInstance returns an instance whose message database does not exist yet, and
// drops it when the test ends.
func freshWhatsAppInstance(t *testing.T) string {
	t.Helper()
	instanceID := "idx" + uuid.NewString()[:8]
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := testDBService.DBClient.Database(testDBNamePrefix + instanceID + "_messageDB").Drop(ctx); err != nil {
			t.Errorf("drop the test database of %s: %v", instanceID, err)
		}
	})
	return instanceID
}

// indexSpecs lists a collection's indexes by name, with the key read in order.
func indexSpecs(t *testing.T, collection *mongo.Collection) map[string]bson.D {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list the indexes of %s: %v", collection.Name(), err)
	}
	var specs []bson.D
	if err := cursor.All(ctx, &specs); err != nil {
		t.Fatalf("read the indexes of %s: %v", collection.Name(), err)
	}
	byName := map[string]bson.D{}
	for _, spec := range specs {
		byName[fmt.Sprint(spec.Map()["name"])] = spec
	}
	return byName
}

// keyOf renders an index key in order, e.g. "addedAt:1,_id:1".
func keyOf(spec bson.D) string {
	key, _ := spec.Map()["key"].(bson.D)
	out := ""
	for i, e := range key {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%s:%v", e.Key, e.Value)
	}
	return out
}

// expireAfterOf returns the TTL of an index in seconds, or -1 when it has none.
func expireAfterOf(spec bson.D) int64 {
	switch v := spec.Map()["expireAfterSeconds"].(type) {
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return -1
}

// keysOf lists the keys of a collection's indexes, each with its TTL when it has one.
func keysOf(t *testing.T, collection *mongo.Collection) map[string]int64 {
	t.Helper()
	keys := map[string]int64{}
	for _, spec := range indexSpecs(t, collection) {
		keys[keyOf(spec)] = expireAfterOf(spec)
	}
	return keys
}

// On a database that has never seen WhatsApp, the queue gets its claim, template and
// participant indexes, and the archive its participant index, a sparse index on Meta's message
// id and an index that expires rows 180 days after archivedAt. A second run changes nothing and does not fail.
// Fails if an index is missing, has its keys in another order, or the retention is not 180 days
// on the archive date.
func TestCreateIndexesForWhatsAppOnAFreshDatabase(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	want := map[string]map[string]int64{
		"outgoing-whatsapp": {
			"_id:1":                             -1,
			"addedAt:1,_id:1,lastSendAttempt:1": -1,
			"templateName:1,lang:1,lastSendAttempt:1": -1,
			"userId:1": -1,
		},
		"sent-whatsapp": {
			"_id:1":        -1,
			"userId:1":     -1,
			"wamid:1":      -1,
			"archivedAt:1": retentionSeconds,
		},
	}

	for run := 1; run <= 2; run++ {
		if err := testDBService.CreateIndexesForWhatsApp(instanceID); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for collection, wantKeys := range want {
			got := keysOf(t, testDBService.DBClient.Database(testDBNamePrefix+instanceID+"_messageDB").Collection(collection))
			if len(got) != len(wantKeys) {
				t.Fatalf("run %d: %s has indexes %v, want %v", run, collection, got, wantKeys)
			}
			for key, ttl := range wantKeys {
				if gotTTL, ok := got[key]; !ok || gotTTL != ttl {
					t.Fatalf("run %d: %s index %s: present %v, TTL %d; want TTL %d (all: %v)", run, collection, key, ok, gotTTL, ttl, got)
				}
			}
		}
		// Only the rows of accepted messages carry a wamid; the others stay out of its index.
		sent := testDBService.collectionRefSentWhatsApp(instanceID)
		if sparse := indexSpecs(t, sent)["wamid_1"].Map()["sparse"]; sparse != true {
			t.Fatalf("run %d: the wamid index of sent-whatsapp is not sparse (sparse=%v)", run, sparse)
		}
	}
}

// A retention index left with another expiry, or an index on archivedAt alone with none, in
// either direction, is brought to 180 days in place, so that only one expiry acts on the date.
// An index that only starts with archivedAt is not the retention index: it is left as it is,
// and the retention index is created next to it when it is the only one.
// Fails if an existing index on archivedAt is left with the wrong expiry (creating it again
// with the new value is refused, or ignored), if a second expiry is added next to it, or if
// the wrong index is changed.
func TestCreateIndexesForWhatsAppChangesAnExistingRetention(t *testing.T) {
	compound := mongo.IndexModel{Keys: bson.D{{Key: "archivedAt", Value: 1}, {Key: "status", Value: 1}}}
	for _, tt := range []struct {
		name      string
		existing  []mongo.IndexModel
		retention string // the key of the index that must end up with the retention
	}{
		{"shorter retention", []mongo.IndexModel{
			{Keys: bson.D{{Key: "archivedAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(3600)}, compound,
		}, "archivedAt:1"},
		{"longer retention", []mongo.IndexModel{
			{Keys: bson.D{{Key: "archivedAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(2 * retentionSeconds)}, compound,
		}, "archivedAt:1"},
		{"no retention", []mongo.IndexModel{
			{Keys: bson.D{{Key: "archivedAt", Value: 1}}}, compound,
		}, "archivedAt:1"},
		{"descending, shorter retention", []mongo.IndexModel{
			{Keys: bson.D{{Key: "archivedAt", Value: -1}}, Options: options.Index().SetExpireAfterSeconds(3600)},
		}, "archivedAt:-1"},
		{"only the compound index", []mongo.IndexModel{compound}, "archivedAt:1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instanceID := freshWhatsAppInstance(t)
			sent := testDBService.collectionRefSentWhatsApp(instanceID)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := sent.Indexes().CreateMany(ctx, tt.existing); err != nil {
				t.Fatalf("create the indexes of an earlier version: %v", err)
			}
			hadCompound := keysOf(t, sent)["archivedAt:1,status:1"] == -1

			if err := testDBService.CreateIndexesForWhatsApp(instanceID); err != nil {
				t.Fatal(err)
			}

			got := keysOf(t, sent)
			if ttl := got[tt.retention]; ttl != retentionSeconds {
				t.Fatalf("index %s TTL = %d, want %d (all: %v)", tt.retention, ttl, retentionSeconds, got)
			}
			for key, ttl := range got {
				if ttl != -1 && key != tt.retention {
					t.Fatalf("a second expiry, on %s (%d s), next to the retention index (all: %v)", key, ttl, got)
				}
			}
			if hadCompound {
				if ttl, ok := got["archivedAt:1,status:1"]; !ok || ttl != -1 {
					t.Fatalf("the compound index starting with archivedAt was changed: present %v, TTL %d", ok, ttl)
				}
			}
		})
	}
}

// The retention index only expires a document whose field is a BSON date: archivedAt has to be
// one, at the moment of the archive, and match sentAt. The archive also keeps the moment the
// message was queued instead of overwriting it.
// Fails if archivedAt is stored as a number or a string (the rows would never expire), or if
// addedAt is overwritten with the archive time again.
func TestSentWhatsAppStoresTheArchiveDateAndKeepsTheQueueTime(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	queuedAt := time.Now().Add(-5 * time.Hour).Unix()
	before := time.Now().Truncate(time.Millisecond)
	if _, err := testDBService.AddToSentWhatsApp(instanceID, types.OutgoingWhatsApp{
		MessageType: "weekly", ToPhoneNumber: "+393331234567", TemplateName: "t", Lang: "it",
		UserID: "archive-date-user", AddedAt: queuedAt,
	}, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := testDBService.collectionRefSentWhatsApp(instanceID).FindOne(ctx, bson.M{"userId": "archive-date-user"}).Raw()
	if err != nil {
		t.Fatalf("read the archive row: %v", err)
	}
	archivedAt := raw.Lookup("archivedAt")
	if archivedAt.Type != bson.TypeDateTime {
		t.Fatalf("archivedAt is stored as %s, want a BSON date", archivedAt.Type)
	}
	at := archivedAt.Time()
	if at.Before(before) || at.After(after) {
		t.Fatalf("archivedAt = %s, want the archive time (between %s and %s)", at, before, after)
	}
	if sentAt := raw.Lookup("sentAt").AsInt64(); sentAt != at.Unix() {
		t.Fatalf("sentAt = %d, archivedAt = %d: the two must name the same moment", sentAt, at.Unix())
	}
	if addedAt := raw.Lookup("addedAt").AsInt64(); addedAt != queuedAt {
		t.Fatalf("addedAt = %d, want the queue time %d", addedAt, queuedAt)
	}
}

// The queue hands out the oldest messages first, whatever order they were inserted in;
// messages queued in the same second go in _id order; a claimed message is skipped however old
// it is. The two messages of the same second are inserted against their _id order, so that
// the order the collection happens to return them in cannot pass for the tie-break.
// Fails if the claim stops sorting, sorts the wrong way, or loses the tie-break.
func TestFetchOutgoingWhatsAppHandsOutTheOldestFirst(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	now := time.Now().Unix()
	earlierID := primitive.NewObjectIDFromTimestamp(time.Unix(now-30, 0))
	laterID := primitive.NewObjectIDFromTimestamp(time.Unix(now-29, 0))
	queue := []struct {
		id          primitive.ObjectID
		phone       string
		addedAt     int64
		lastAttempt int64
	}{
		{primitive.NilObjectID, "+393330000010", now - 10, 0},
		{laterID, "+393330000031", now - 30, 0},                 // same second as 030, but the larger _id
		{primitive.NilObjectID, "+393330000099", now - 99, now}, // the oldest, but claimed by another tick
		{primitive.NilObjectID, "+393330000020", now - 20, 0},
		{earlierID, "+393330000030", now - 30, 0},
	}
	for _, m := range queue {
		if _, err := testDBService.AddToOutgoingWhatsApp(instanceID, types.OutgoingWhatsApp{
			ID: m.id, MessageType: "weekly", ToPhoneNumber: m.phone, TemplateName: "t", Lang: "it",
			AddedAt: m.addedAt, LastSendAttempt: m.lastAttempt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	for _, batch := range []int{2, 10} {
		messages, err := testDBService.FetchOutgoingWhatsApp(instanceID, batch, 60)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			got = append(got, m.ToPhoneNumber)
		}
	}
	want := []string{"+393330000030", "+393330000031", "+393330000020", "+393330000010"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("claim order = %v, want %v", got, want)
	}
}

// planOf returns the stages and index names of a query's winning plan, wherever the server
// nests them.
func planOf(t *testing.T, db *mongo.Database, explained bson.D) (stages, indexes []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bson.M
	if err := db.RunCommand(ctx, bson.D{{Key: "explain", Value: explained}, {Key: "verbosity", Value: "queryPlanner"}}).Decode(&out); err != nil {
		t.Fatalf("explain %v: %v", explained, err)
	}
	planner, _ := out["queryPlanner"].(bson.M)
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch n := v.(type) {
		case bson.M:
			if s, ok := n["stage"].(string); ok {
				stages = append(stages, s)
			}
			if s, ok := n["indexName"].(string); ok {
				indexes = append(indexes, s)
			}
			for _, child := range n {
				walk(child)
			}
		case bson.A:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(planner["winningPlan"])
	return stages, indexes
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The queries that run on the queue and the archive go through their indexes: the claim reads
// the claim index in order and needs no sort of its own, and the template deferral, the
// cancellation, the purge and the match of a delivery status do not scan the collection.
// Fails if an index is dropped, or the claim index is built with the claim age first, which
// makes every claim sort all the free messages.
func TestWhatsAppQueriesUseTheirIndexes(t *testing.T) {
	instanceID := freshWhatsAppInstance(t)
	if err := testDBService.CreateIndexesForWhatsApp(instanceID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		queueWhatsAppForTest(t, instanceID, fmt.Sprintf("user-%d", i), "weekly", false)
	}
	db := testDBService.DBClient.Database(testDBNamePrefix + instanceID + "_messageDB")
	now := time.Now().Unix()

	stages, indexes := planOf(t, db, bson.D{
		{Key: "findAndModify", Value: "outgoing-whatsapp"},
		{Key: "query", Value: outgoingWhatsAppClaimFilter(now, 60)},
		{Key: "sort", Value: outgoingWhatsAppClaimOrder},
		{Key: "update", Value: bson.M{"$set": bson.M{"lastSendAttempt": now}}},
	})
	if !contains(indexes, "addedAt_1__id_1_lastSendAttempt_1") {
		t.Fatalf("the claim does not use the claim index: stages %v, indexes %v", stages, indexes)
	}
	if contains(stages, "SORT") || contains(stages, "COLLSCAN") {
		t.Fatalf("the claim sorts or scans: stages %v", stages)
	}

	for name, query := range map[string]bson.D{
		"template deferral": {
			{Key: "find", Value: "outgoing-whatsapp"},
			{Key: "filter", Value: func() bson.M {
				f := outgoingWhatsAppOfTemplateFilter("influenzanet_weekly_v1", "it", nil, now-60)
				f["sendAttempt"] = bson.M{"$lt": 4}
				return f
			}()},
		},
		"cancellation": {
			{Key: "find", Value: "outgoing-whatsapp"},
			{Key: "filter", Value: outgoingWhatsAppToCancelFilter("user-1", []string{"weekly"})},
		},
		"purge of the queue": {
			{Key: "find", Value: "outgoing-whatsapp"},
			{Key: "filter", Value: bson.M{"userId": "user-1"}},
		},
		"purge of the archive": {
			{Key: "find", Value: "sent-whatsapp"},
			{Key: "filter", Value: bson.M{"userId": "user-1"}},
		},
		"status match on the archive": {
			{Key: "find", Value: "sent-whatsapp"},
			{Key: "filter", Value: bson.M{"wamid": "wamid.A"}},
		},
	} {
		stages, indexes := planOf(t, db, query)
		if contains(stages, "COLLSCAN") || len(indexes) == 0 {
			t.Fatalf("%s scans the collection: stages %v, indexes %v", name, stages, indexes)
		}
	}
}
