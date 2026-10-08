package messagedb

import (
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// whatsAppThrottleDocID is the _id of the single document that holds the sender's throttle
// state of an instance.
const whatsAppThrottleDocID = "sender"

// WhatsAppThrottle is the state the schedulers of an instance share while Meta limits the
// WhatsApp sender: NotBefore is the unix time before which no scheduler sends, Level the
// number of throttles in a row that have not been followed by an accepted message.
type WhatsAppThrottle struct {
	NotBefore int64 `bson:"notBefore"`
	Level     int   `bson:"level"`
}

func (dbService *MessageDBService) collectionRefWhatsAppThrottle(instanceID string) *mongo.Collection {
	return dbService.DBClient.Database(dbService.DBNamePrefix + instanceID + "_messageDB").Collection("whatsapp-throttle")
}

// GetWhatsAppThrottle returns the throttle state of an instance; the zero value when Meta has
// never limited it.
func (dbService *MessageDBService) GetWhatsAppThrottle(instanceID string) (WhatsAppThrottle, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	var state WhatsAppThrottle
	err := dbService.collectionRefWhatsAppThrottle(instanceID).FindOne(ctx, bson.M{"_id": whatsAppThrottleDocID}).Decode(&state)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return WhatsAppThrottle{}, nil
	}
	return state, err
}

// RecordWhatsAppThrottle stores that Meta limited the sender and that no scheduler may send
// before notBefore, and returns the state after the write. now is the unix time of the
// throttle. The write is one atomic update that reads the stored state: notBefore only moves
// forward, so a scheduler that computed a shorter wait never shortens one another has stored,
// and the level grows only when no pause is active (stored notBefore <= now). A throttle that
// arrives while a pause is active belongs to the event already recorded, so replicas and
// overlapping ticks throttled by the same Meta answer count it once.
func (dbService *MessageDBService) RecordWhatsAppThrottle(instanceID string, notBefore, now int64) (WhatsAppThrottle, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	storedNotBefore := bson.M{"$ifNull": bson.A{"$notBefore", 0}}
	storedLevel := bson.M{"$ifNull": bson.A{"$level", 0}}
	update := mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"level": bson.M{"$cond": bson.A{
			bson.M{"$lte": bson.A{storedNotBefore, now}},
			bson.M{"$add": bson.A{storedLevel, 1}},
			storedLevel,
		}},
		"notBefore": bson.M{"$max": bson.A{storedNotBefore, notBefore}},
	}}}}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var state WhatsAppThrottle
	var err error
	// Two upserts racing on a missing document can make the loser fail with a duplicate key;
	// the document exists by then, so the second try is a plain update.
	for try := 0; try < 2; try++ {
		err = dbService.collectionRefWhatsAppThrottle(instanceID).FindOneAndUpdate(ctx, bson.M{"_id": whatsAppThrottleDocID}, update, opts).Decode(&state)
		if err == nil || !mongo.IsDuplicateKeyError(err) {
			break
		}
	}
	return state, err
}

// ResetWhatsAppThrottleLevel records that Meta accepted a message: the next throttle starts
// the backoff from its shortest wait again. NotBefore is left alone, a wait already stored is
// served in full, and a missing document is not created. The reset applies only when no pause
// is active at now (notBefore <= now): an active pause means a throttle was recorded after the
// caller's tick started, and its series must not be reset by an older success.
func (dbService *MessageDBService) ResetWhatsAppThrottleLevel(instanceID string, now int64) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"_id": whatsAppThrottleDocID, "level": bson.M{"$gt": 0}, "notBefore": bson.M{"$lte": now}}
	_, err := dbService.collectionRefWhatsAppThrottle(instanceID).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"level": 0}})
	return err
}
