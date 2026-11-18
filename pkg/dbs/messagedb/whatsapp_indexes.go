package messagedb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SentWhatsAppRetention is how long an archived WhatsApp message is kept in sent-whatsapp
// before MongoDB deletes it: 180 days, which covers a flu season and the reconciliation with
// Meta's invoices.
const SentWhatsAppRetention = 180 * 24 * time.Hour

// sentWhatsAppRetentionField is the date the retention index expires archive rows on.
const sentWhatsAppRetentionField = "archivedAt"

// outgoingWhatsAppIndexes serve the queries on the queue:
//   - the claim, which filters on the claim age and hands out the oldest messages first. The
//     sort keys come before the range key, so that the index returns the messages already in
//     order and the claim stops at the first free one, instead of sorting every free message
//     on each claim;
//   - the deferral of a refused template, which picks the messages of one template in one
//     language by their claim;
//   - the cancellation for a participant who withdraws, and the purge of a deleted account.
var outgoingWhatsAppIndexes = []mongo.IndexModel{
	{Keys: bson.D{{Key: "addedAt", Value: 1}, {Key: "_id", Value: 1}, {Key: "lastSendAttempt", Value: 1}}},
	{Keys: bson.D{{Key: "templateName", Value: 1}, {Key: "lang", Value: 1}, {Key: "lastSendAttempt", Value: 1}}},
	{Keys: bson.D{{Key: "userId", Value: 1}}},
}

// CreateIndexesForWhatsApp makes sure the WhatsApp collections of an instance have their
// indexes: the queue's, the archive's index on userId for the purge of a deleted account, its
// index on wamid for matching the delivery statuses Meta reports, and its retention index,
// which deletes a row SentWhatsAppRetention after it was archived. It can run any number of
// times. A retention index left by an earlier version with
// another retention is changed in place with collMod, since creating it again with the new
// value would be refused.
func (dbService *MessageDBService) CreateIndexesForWhatsApp(instanceID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	if _, err := dbService.collectionRefOutgoingWhatsApp(instanceID).Indexes().CreateMany(ctx, outgoingWhatsAppIndexes); err != nil {
		return fmt.Errorf("indexes of outgoing-whatsapp: %w", err)
	}
	sent := dbService.collectionRefSentWhatsApp(instanceID)
	if _, err := sent.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "userId", Value: 1}}}); err != nil {
		return fmt.Errorf("userId index of sent-whatsapp: %w", err)
	}
	// Sparse: only the rows of messages Meta accepted carry a wamid.
	if _, err := sent.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "wamid", Value: 1}},
		Options: options.Index().SetSparse(true),
	}); err != nil {
		return fmt.Errorf("wamid index of sent-whatsapp: %w", err)
	}
	if err := ensureTTLIndex(ctx, sent, sentWhatsAppRetentionField, int64(SentWhatsAppRetention/time.Second)); err != nil {
		return fmt.Errorf("retention index of sent-whatsapp: %w", err)
	}
	return nil
}

// ensureTTLIndex makes sure collection has an index on field alone that expires documents
// expireAfter seconds after the date in field. A missing index is created, ascending; an
// existing one, in either direction, with another expiry or none, is changed in place with
// collMod, so that the collection never ends up with two expiries on the same date.
func ensureTTLIndex(ctx context.Context, collection *mongo.Collection, field string, expireAfter int64) error {
	name, current, found, err := ttlOfSingleFieldIndex(ctx, collection, field)
	if err != nil {
		return err
	}
	if !found {
		_, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: field, Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(expireAfter)),
		})
		return err
	}
	if current == expireAfter {
		return nil
	}
	return collection.Database().RunCommand(ctx, bson.D{
		{Key: "collMod", Value: collection.Name()},
		{Key: "index", Value: bson.D{
			{Key: "name", Value: name},
			{Key: "expireAfterSeconds", Value: expireAfter},
		}},
	}).Err()
}

// ttlOfSingleFieldIndex looks for the index on field alone and returns its name and its expiry
// in seconds, or -1 when it has none.
func ttlOfSingleFieldIndex(ctx context.Context, collection *mongo.Collection, field string) (string, int64, bool, error) {
	// The driver lists no index, without an error, for a collection that does not exist yet.
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return "", 0, false, err
	}
	var specs []bson.M
	if err := cursor.All(ctx, &specs); err != nil {
		return "", 0, false, err
	}
	for _, spec := range specs {
		key, ok := spec["key"].(bson.M)
		if !ok || len(key) != 1 {
			continue
		}
		if _, onField := key[field]; !onField {
			continue
		}
		name, _ := spec["name"].(string)
		switch expire := spec["expireAfterSeconds"].(type) {
		case int32:
			return name, int64(expire), true, nil
		case int64:
			return name, expire, true, nil
		case float64:
			return name, int64(expire), true, nil
		default:
			return name, -1, true, nil
		}
	}
	return "", 0, false, nil
}
