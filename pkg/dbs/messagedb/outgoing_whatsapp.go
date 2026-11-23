package messagedb

import (
	"errors"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
// identically. Whatever the outcome, the row keeps only the masked number, no content
// parameters, and Meta's message id only as its hash (wamidHash): none of them has any use in
// full once the message has left the queue, the parameters may carry a login token or other
// personal data, and the id holds the full number. addedAt stays the moment the message was
// queued; the archive time goes to sentAt and, as a date the retention index can expire,
// to archivedAt.
func (dbService *MessageDBService) AddToSentWhatsApp(instanceID string, msg types.OutgoingWhatsApp, outcome types.WhatsAppSendOutcome) (types.SentWhatsApp, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	archivedAt := time.Now()
	msg.ToPhoneNumber = types.MaskPhone(msg.ToPhoneNumber)
	msg.ContentParams = nil
	msg.ID = primitive.NilObjectID
	// Queue bookkeeping: the archive states the outcome in its own status field.
	msg.Delivered = false
	// Meta's id holds the full number: the archive keeps only its hash.
	wamidHash := types.WAMIDHash(msg.WAMID)
	msg.WAMID = ""

	sent := types.SentWhatsApp{
		OutgoingWhatsApp: msg,
		Status:           outcome.Status,
		ErrorCode:        outcome.ErrorCode,
		ErrorClass:       outcome.ErrorClass,
		SentAt:           archivedAt.Unix(),
		ArchivedAt:       archivedAt,
		WAMIDHash:        wamidHash,
	}
	res, err := dbService.collectionRefSentWhatsApp(instanceID).InsertOne(ctx, sent)
	if err != nil {
		return sent, err
	}
	sent.ID = res.InsertedID.(primitive.ObjectID)
	return sent, nil
}

// outgoingWhatsAppClaimFilter selects the queued messages no tick holds at now: their claim is
// older than olderThan seconds.
func outgoingWhatsAppClaimFilter(now, olderThan int64) bson.M {
	return bson.M{"lastSendAttempt": bson.M{"$lt": now - olderThan}}
}

// outgoingWhatsAppClaimOrder hands out the oldest messages first, so that a backlog drains in
// the order it was queued and the messages closest to the maximum age go before the others.
// Messages queued in the same second go in _id order, which is the order they were queued in
// when the same process queued them.
var outgoingWhatsAppClaimOrder = bson.D{{Key: "addedAt", Value: 1}, {Key: "_id", Value: 1}}

// FetchOutgoingWhatsApp claims up to amount messages no tick holds, oldest first.
func (dbService *MessageDBService) FetchOutgoingWhatsApp(instanceID string, amount int, olderThan int64) (messages []types.OutgoingWhatsApp, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	// One instant for the whole batch: a clock read per claim would let a loop that lasts
	// longer than olderThan hand out a message it has already claimed in this same call.
	now := time.Now().Unix()
	counter := 0
	update := bson.M{"$set": bson.M{"lastSendAttempt": now}}
	filter := outgoingWhatsAppClaimFilter(now, olderThan)
	opts := options.FindOneAndUpdate().SetSort(outgoingWhatsAppClaimOrder)
	for counter < amount {
		var newMsg types.OutgoingWhatsApp
		err = dbService.collectionRefOutgoingWhatsApp(instanceID).FindOneAndUpdate(ctx, filter, update, opts).Decode(&newMsg)
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

// ErrOutgoingWhatsAppNotFound reports that the queued message is no longer there: its claim
// expired and another tick has already taken it out of the queue.
var ErrOutgoingWhatsAppNotFound = errors.New("no outgoing whatsapp message found with the given id")

// MarkOutgoingWhatsAppDelivered records on the queued message that Meta accepted it, and the id
// Meta gave it when there is one. It is written before the message is archived, so that a
// failure of the archive write leaves a row the next tick can finish, id included, instead of
// a row it would send to Meta a second time.
func (dbService *MessageDBService) MarkOutgoingWhatsAppDelivered(instanceID string, id string, wamid string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	set := bson.M{"delivered": true}
	if wamid != "" {
		set["wamid"] = wamid
	}
	update := bson.M{"$set": set}

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

// outgoingWhatsAppToCancelFilter selects the participant's queued messages of the given types
// (no types: every type) that Meta has not accepted yet.
func outgoingWhatsAppToCancelFilter(userID string, messageTypes []string) bson.M {
	filter := bson.M{
		"userId":    userID,
		"delivered": bson.M{"$ne": true},
	}
	if len(messageTypes) > 0 {
		filter["messageType"] = bson.M{"$in": messageTypes}
	}
	return filter
}

// CancelOutgoingWhatsAppForUser takes the participant's queued messages out of the queue and
// archives each one as cancelled. Only messages of the given types are cancelled; no types
// means every type. It returns how many messages it took out of the queue.
//
// Each message is removed with a find-and-delete of its own, so that calls running at the same
// time (a retry overlapping the first attempt) never archive the same message twice. A message
// Meta has already accepted is left to the scheduler, which archives it as delivered. A message
// a running tick has already claimed may still be sent: the tick holds its own copy, and only
// notices on its next write that the row is gone.
//
// Should an archive write fail, the message stays out of the queue (the participant withdrew,
// so it must not be sent) and the error is returned with the count so far; a retry carries on
// with the messages still queued.
func (dbService *MessageDBService) CancelOutgoingWhatsAppForUser(instanceID string, userID string, messageTypes []string) (int64, error) {
	if userID == "" {
		return 0, errors.New("user id is required")
	}
	filter := outgoingWhatsAppToCancelFilter(userID, messageTypes)

	var cancelled int64
	for {
		var msg types.OutgoingWhatsApp
		ctx, cancel := dbService.getContext()
		err := dbService.collectionRefOutgoingWhatsApp(instanceID).FindOneAndDelete(ctx, filter).Decode(&msg)
		cancel()
		if err == mongo.ErrNoDocuments {
			return cancelled, nil
		}
		if err != nil {
			return cancelled, err
		}
		cancelled++
		if _, err := dbService.AddToSentWhatsApp(instanceID, msg, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusCancelled}); err != nil {
			return cancelled, err
		}
	}
}

// PurgeWhatsAppForUser deletes, for a deleted account, every queued message of the participant
// and every archived one, without archiving anything. It returns how many rows it deleted from
// the queue and from the archive. A message a running tick is sending at that moment may still
// be archived after the purge; the archive's retention removes that row.
func (dbService *MessageDBService) PurgeWhatsAppForUser(instanceID string, userID string) (int64, int64, error) {
	if userID == "" {
		return 0, 0, errors.New("user id is required")
	}
	filter := bson.M{"userId": userID}

	ctx, cancel := dbService.getContext()
	defer cancel()
	queued, err := dbService.collectionRefOutgoingWhatsApp(instanceID).DeleteMany(ctx, filter)
	if err != nil {
		return 0, 0, err
	}
	archived, err := dbService.collectionRefSentWhatsApp(instanceID).DeleteMany(ctx, filter)
	if err != nil {
		return queued.DeletedCount, 0, err
	}
	return queued.DeletedCount, archived.DeletedCount, nil
}
