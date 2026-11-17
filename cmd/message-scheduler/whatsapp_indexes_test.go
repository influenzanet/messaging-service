package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// archiveRetentionOf returns the TTL of the archive's index on archivedAt in an instance's
// message database, and whether that index exists.
func (db *schedulerTestDB) archiveRetentionOf(t *testing.T, instanceID string) (int64, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent := db.service.DBClient.Database(db.prefix + instanceID + "_messageDB").Collection("sent-whatsapp")
	cursor, err := sent.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list the archive indexes of %s: %v", instanceID, err)
	}
	var specs []bson.M
	if err := cursor.All(ctx, &specs); err != nil {
		t.Fatalf("read the archive indexes of %s: %v", instanceID, err)
	}
	for _, spec := range specs {
		if spec["name"] != "archivedAt_1" {
			continue
		}
		switch ttl := spec["expireAfterSeconds"].(type) {
		case int32:
			return int64(ttl), true
		case int64:
			return ttl, true
		}
		return -1, true
	}
	return 0, false
}

// otherInstanceDB is the message database of a second instance under this test's prefix,
// dropped when the test ends.
func (db *schedulerTestDB) otherInstanceDB(t *testing.T, instanceID string) *mongo.Database {
	t.Helper()
	other := db.service.DBClient.Database(db.prefix + instanceID + "_messageDB")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := other.Drop(ctx); err != nil {
			t.Errorf("drop the database of instance %s: %v", instanceID, err)
		}
	})
	return other
}

// At startup every known instance gets the 180-day archive retention, whether WhatsApp is on
// for it or not (the function is not told).
// Fails if the scheduler stops creating the indexes, or only does so for one instance.
func TestEnsureWhatsAppIndexesCoversEveryInstance(t *testing.T) {
	const second = "j"
	db := newSchedulerTestDB(t)
	db.otherInstanceDB(t, second)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, schedulerTestInstanceID)
	insertSchedulerTestInstance(t, gdb, second)

	ensureWhatsAppIndexes(db.service, gdb)

	for _, instanceID := range []string{schedulerTestInstanceID, second} {
		if ttl, ok := db.archiveRetentionOf(t, instanceID); !ok || ttl != 180*24*3600 {
			t.Fatalf("instance %s: archive retention index present %v, TTL %d s; want 180 days", instanceID, ok, ttl)
		}
	}
}

// An instance whose indexes cannot be created is named in the error log, and the instances
// after it still get theirs. The failure comes from a database user that may create indexes in
// the second instance's database only.
// Fails if one failing instance stops the others, or the failure is not logged.
func TestEnsureWhatsAppIndexesGoesOnAfterAFailingInstance(t *testing.T) {
	// One letter each: the test prefix leaves no room in a database name for more.
	const failing, working = "f", "w"
	db := newSchedulerTestDB(t)
	db.otherInstanceDB(t, failing)
	db.otherInstanceDB(t, working)
	gdb := newSchedulerTestGlobalDB(t)
	insertSchedulerTestInstance(t, gdb, failing)
	insertSchedulerTestInstance(t, gdb, working)
	restricted := db.serviceWithPrivileges(t, "index_one", bson.A{bson.M{
		"resource": bson.M{"db": db.prefix + working + "_messageDB", "collection": ""},
		"actions":  bson.A{"createIndex", "listIndexes", "collMod", "createCollection", "find", "insert"},
	}})
	errorsLog := captureErrorLog(t)

	ensureWhatsAppIndexes(restricted, gdb)

	if !strings.Contains(errorsLog.String(), "["+failing+"] cannot create the WhatsApp indexes") {
		t.Fatalf("the failing instance was not named; errors: %s", errorsLog.String())
	}
	if _, ok := db.archiveRetentionOf(t, failing); ok {
		t.Fatal("the instance the user may not touch has a retention index: the failure was not real")
	}
	if ttl, ok := db.archiveRetentionOf(t, working); !ok || ttl != 180*24*3600 {
		t.Fatalf("the instance after the failing one: retention index present %v, TTL %d s", ok, ttl)
	}
}

// Without the list of instances nothing can be checked, and the log says so.
// Fails if the failure to list the instances goes unreported.
func TestEnsureWhatsAppIndexesReportsAnUnreadableInstanceList(t *testing.T) {
	db := newSchedulerTestDB(t)
	gdb := globaldb.NewGlobalDBService(types.DBConfig{
		URI:             os.Getenv("F04_TEST_MONGODB_URI"),
		Timeout:         30,
		IdleConnTimeout: 30,
		MaxPoolSize:     10,
		DBNamePrefix:    db.prefix,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gdb.DBClient.Disconnect(ctx); err != nil {
		t.Fatalf("disconnect the global database service: %v", err)
	}
	errorsLog := captureErrorLog(t)

	ensureWhatsAppIndexes(db.service, gdb)

	if !strings.Contains(errorsLog.String(), "cannot list the instances, the WhatsApp indexes were not checked") {
		t.Fatalf("the unreadable instance list was not reported; errors: %s", errorsLog.String())
	}
}
