package messagedb

import (
	"errors"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func (dbService *MessageDBService) AddToOutgoingWhatsApp(instanceID string, msg types.OutgoingWhatsApp) (types.OutgoingWhatsApp, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	if msg.AddedAt <= 0 {
		msg.AddedAt = time.Now().Unix()
	}

	res, err := dbService.collectionRefOutgoingWhatsApp(instanceID).InsertOne(ctx, msg)
	if err != nil {
		return msg, err
	}
	msg.ID = res.InsertedID.(primitive.ObjectID)
	return msg, nil
}

// AddToSentWhatsApp archives a message that left the outgoing queue, together with the outcome
// that took it out: a delivered message and one the scheduler gave up on are otherwise stored
// identically. The content parameters are dropped, as they may carry personal data.
func (dbService *MessageDBService) AddToSentWhatsApp(instanceID string, msg types.OutgoingWhatsApp, outcome types.WhatsAppSendOutcome) (types.SentWhatsApp, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	archivedAt := time.Now().Unix()
	msg.AddedAt = archivedAt
	msg.ContentParams = nil
	msg.ID = primitive.NilObjectID
	// Queue bookkeeping: the archive states the outcome in its own status field.
	msg.Delivered = false

	sent := types.SentWhatsApp{
		OutgoingWhatsApp: msg,
		Status:           outcome.Status,
		ErrorCode:        outcome.ErrorCode,
		ErrorClass:       outcome.ErrorClass,
		SentAt:           archivedAt,
	}
	res, err := dbService.collectionRefSentWhatsApp(instanceID).InsertOne(ctx, sent)
	if err != nil {
		return sent, err
	}
	sent.ID = res.InsertedID.(primitive.ObjectID)
	return sent, nil
}

func (dbService *MessageDBService) FetchOutgoingWhatsApp(instanceID string, amount int, olderThan int64) (messages []types.OutgoingWhatsApp, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	// One instant for the whole batch: a clock read per claim would let a loop that lasts
	// longer than olderThan hand out a message it has already claimed in this same call.
	now := time.Now().Unix()
	counter := 0
	for counter < amount {
		var newMsg types.OutgoingWhatsApp
		update := bson.M{"$set": bson.M{"lastSendAttempt": now}}
		filter := bson.M{"lastSendAttempt": bson.M{"$lt": now - olderThan}}
		err = dbService.collectionRefOutgoingWhatsApp(instanceID).FindOneAndUpdate(ctx, filter, update).Decode(&newMsg)
		if err != nil {
			break
		}
		messages = append(messages, newMsg)
		counter += 1
	}
	// ErrNoDocuments means the batch is exhausted — not a real error.
	if err == mongo.ErrNoDocuments {
		return messages, nil
	}
	return messages, err
}

func (dbService *MessageDBService) ResetLastSendAttemptForOutgoingWhatsApp(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"lastSendAttempt": 0}}

	res, err := dbService.collectionRefOutgoingWhatsApp(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.ModifiedCount < 1 {
		return ErrOutgoingWhatsAppNotFound
	}
	return nil
}

// ErrOutgoingWhatsAppNotFound reports that the queued message is no longer there: its claim
// expired and another tick has already taken it out of the queue.
var ErrOutgoingWhatsAppNotFound = errors.New("no outgoing whatsapp message found with the given id")

// MarkOutgoingWhatsAppDelivered records on the queued message that Meta accepted it. It is
// written before the message is archived, so that a failure of the archive write leaves a row
// the next tick can finish instead of a row it would send to Meta a second time.
func (dbService *MessageDBService) MarkOutgoingWhatsAppDelivered(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"delivered": true}}

	res, err := dbService.collectionRefOutgoingWhatsApp(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount < 1 {
		return ErrOutgoingWhatsAppNotFound
	}
	return nil
}

// IncrementSendAttemptForOutgoingWhatsApp bumps the retry counter without
// resetting lastSendAttempt, so the natural lock timeout provides backoff.
func (dbService *MessageDBService) IncrementSendAttemptForOutgoingWhatsApp(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$inc": bson.M{"sendAttempt": 1}}

	_, err := dbService.collectionRefOutgoingWhatsApp(instanceID).UpdateOne(ctx, filter, update)
	return err
}

func (dbService *MessageDBService) DeleteOutgoingWhatsApp(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}

	res, err := dbService.collectionRefOutgoingWhatsApp(instanceID).DeleteOne(ctx, filter, nil)
	if err != nil {
		return err
	}
	if res.DeletedCount < 1 {
		return ErrOutgoingWhatsAppNotFound
	}
	return nil
}

// outgoingWhatsAppOfTemplateFilter selects the queued messages of one template in one language
// that a tick may act on: the ones it claimed itself (ownIDs) and the ones no tick holds, whose
// claim is older than unclaimedBefore. A message another tick holds is left to that tick, so
// that two ticks never charge it twice. A delivered message waits for its archive, not a send.
func outgoingWhatsAppOfTemplateFilter(templateName, lang string, ownIDs []string, unclaimedBefore int64) bson.M {
	own := make([]primitive.ObjectID, 0, len(ownIDs))
	for _, id := range ownIDs {
		if _id, err := primitive.ObjectIDFromHex(id); err == nil {
			own = append(own, _id)
		}
	}
	return bson.M{
		"templateName": templateName,
		"lang":         lang,
		"delivered":    bson.M{"$ne": true},
		"$or": bson.A{
			bson.M{"_id": bson.M{"$in": own}},
			bson.M{"lastSendAttempt": bson.M{"$lt": unclaimedBefore}},
		},
	}
}

// FindOutgoingWhatsAppOfTemplate returns the messages of a template in one language, among
// those the tick may act on, that have already used at least minAttempts attempts.
func (dbService *MessageDBService) FindOutgoingWhatsAppOfTemplate(instanceID, templateName, lang string, ownIDs []string, unclaimedBefore int64, minAttempts int) ([]types.OutgoingWhatsApp, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := outgoingWhatsAppOfTemplateFilter(templateName, lang, ownIDs, unclaimedBefore)
	filter["sendAttempt"] = bson.M{"$gte": minAttempts}
	cur, err := dbService.collectionRefOutgoingWhatsApp(instanceID).Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	var messages []types.OutgoingWhatsApp
	if err := cur.All(ctx, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// DeferOutgoingWhatsAppOfTemplate charges one attempt to the messages of a template in one
// language, among those the tick may act on, that have used fewer than belowAttempts attempts,
// and sets their claim to claimedAt, so that the queue hands them out again only once that
// claim has expired. It returns how many messages it deferred.
func (dbService *MessageDBService) DeferOutgoingWhatsAppOfTemplate(instanceID, templateName, lang string, ownIDs []string, unclaimedBefore int64, belowAttempts int, claimedAt int64) (int64, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := outgoingWhatsAppOfTemplateFilter(templateName, lang, ownIDs, unclaimedBefore)
	filter["sendAttempt"] = bson.M{"$lt": belowAttempts}
	update := bson.M{"$inc": bson.M{"sendAttempt": 1}, "$set": bson.M{"lastSendAttempt": claimedAt}}
	res, err := dbService.collectionRefOutgoingWhatsApp(instanceID).UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}
