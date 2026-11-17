package messagedb

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// WhatsAppQueueDepth describes an instance's outgoing-whatsapp queue at one moment.
type WhatsAppQueueDepth struct {
	// Queued counts every message in the queue.
	Queued int64 `bson:"queued"`
	// Deferred counts the messages held back until a later time, whose claim lies in the
	// future: the messages of a template Meta refused wait there for the retry delay.
	Deferred int64 `bson:"deferred"`
	// Stale counts the messages that are not deferred and were queued before the staleBefore
	// given to GetWhatsAppQueueDepth.
	Stale int64 `bson:"stale"`
	// OldestWaitingAddedAt is the addedAt of the oldest message that is not deferred. It means
	// nothing when every queued message is deferred, or none is queued.
	OldestWaitingAddedAt int64 `bson:"oldestWaitingAddedAt"`
}

// Waiting counts the queued messages that are not deferred: those the queue hands out as soon
// as no tick holds them.
func (d WhatsAppQueueDepth) Waiting() int64 {
	return d.Queued - d.Deferred
}

// GetWhatsAppQueueDepth reads the size of an instance's queue at now in one pass: how many
// messages are queued, how many are deferred (claimed until after now), how many of the others
// were queued before staleBefore, and when the oldest of those others was queued. A message that
// a tick holds at now is not deferred: it counts as waiting. An empty queue gives all zeros.
func (dbService *MessageDBService) GetWhatsAppQueueDepth(instanceID string, now, staleBefore int64) (WhatsAppQueueDepth, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	deferred := bson.M{"$gt": bson.A{"$lastSendAttempt", now}}
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{
			"_id":      nil,
			"queued":   bson.M{"$sum": 1},
			"deferred": bson.M{"$sum": bson.M{"$cond": bson.A{deferred, 1, 0}}},
			"stale": bson.M{"$sum": bson.M{"$cond": bson.A{
				bson.M{"$and": bson.A{
					bson.M{"$not": bson.A{deferred}},
					bson.M{"$lt": bson.A{"$addedAt", staleBefore}},
				}},
				1, 0,
			}}},
			// $min skips the null a deferred message gives.
			"oldestWaitingAddedAt": bson.M{"$min": bson.M{"$cond": bson.A{deferred, nil, "$addedAt"}}},
		}}},
	}
	cursor, err := dbService.collectionRefOutgoingWhatsApp(instanceID).Aggregate(ctx, pipeline)
	if err != nil {
		return WhatsAppQueueDepth{}, err
	}
	defer cursor.Close(ctx)

	var depth WhatsAppQueueDepth
	if cursor.Next(ctx) {
		if err := cursor.Decode(&depth); err != nil {
			return WhatsAppQueueDepth{}, err
		}
	}
	return depth, cursor.Err()
}
