package messagedb

import (
	"errors"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

func (dbService *MessageDBService) AddToSentWhatsApp(instanceID string, msg types.OutgoingWhatsApp) (types.OutgoingWhatsApp, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	msg.AddedAt = time.Now().Unix()
	msg.ContentParams = nil

	msg.ID = primitive.NilObjectID
	res, err := dbService.collectionRefSentWhatsApp(instanceID).InsertOne(ctx, msg)
	if err != nil {
		return msg, err
	}
	msg.ID = res.InsertedID.(primitive.ObjectID)
	return msg, nil
}

func (dbService *MessageDBService) FetchOutgoingWhatsApp(instanceID string, amount int, olderThan int64) (messages []types.OutgoingWhatsApp, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	counter := 0
	for counter < amount {
		var newMsg types.OutgoingWhatsApp
		update := bson.M{"$set": bson.M{"lastSendAttempt": time.Now().Unix()}}
		filter := bson.M{"lastSendAttempt": bson.M{"$lt": time.Now().Unix() - olderThan}}
		if err := dbService.collectionRefOutgoingWhatsApp(instanceID).FindOneAndUpdate(ctx, filter, update).Decode(&newMsg); err != nil {
			break
		}
		messages = append(messages, newMsg)
		counter += 1
	}
	return messages, nil
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
		return errors.New("no outgoing whatsapp message found with the given id")
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
		return errors.New("no outgoing whatsapp message found with the given id")
	}
	return nil
}
