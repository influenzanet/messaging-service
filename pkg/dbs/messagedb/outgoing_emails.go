package messagedb

import (
	"errors"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (dbService *MessageDBService) AddToOutgoingEmails(instanceID string, email types.OutgoingEmail) (types.OutgoingEmail, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	if email.AddedAt <= 0 {
		email.AddedAt = time.Now().Unix()
	}

	res, err := dbService.collectionRefOutgoingEmails(instanceID).InsertOne(ctx, email)
	if err != nil {
		return email, err
	}
	email.ID = res.InsertedID.(primitive.ObjectID)
	return email, nil
}

func (dbService *MessageDBService) AddToSentEmails(instanceID string, email types.OutgoingEmail) (types.OutgoingEmail, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	email.AddedAt = time.Now().Unix()
	email.Content = ""

	email.ID = primitive.NilObjectID
	res, err := dbService.collectionRefSentEmails(instanceID).InsertOne(ctx, email)
	if err != nil {
		return email, err
	}
	email.ID = res.InsertedID.(primitive.ObjectID)
	return email, nil
}

func (dbService *MessageDBService) FetchOutgoingEmails(instanceID string, amount int, olderThan int64, onlyHighPrio bool) (emails []types.OutgoingEmail, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	counter := 0
	for counter < amount {
		var newEmail types.OutgoingEmail
		update := bson.M{"$set": bson.M{"lastSendAttempt": time.Now().Unix()}}
		filter := bson.M{"lastSendAttempt": bson.M{"$lt": time.Now().Unix() - olderThan}}
		if onlyHighPrio {
			filter["highPrio"] = true
		}
		if err := dbService.collectionRefOutgoingEmails(instanceID).FindOneAndUpdate(ctx, filter, update).Decode(&newEmail); err != nil {
			break
		}
		emails = append(emails, newEmail)
		counter += 1
	}
	return emails, nil
}

func (dbService *MessageDBService) ResetLastSendAttemptForOutgoing(instaceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"lastSendAttempt": 0}}

	res, err := dbService.collectionRefOutgoingEmails(instaceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.ModifiedCount < 1 {
		return errors.New("no outgoing email found with the given id")
	}
	return nil
}

func (dbService *MessageDBService) DeleteOutgoingEmail(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}

	res, err := dbService.collectionRefOutgoingEmails(instanceID).DeleteOne(ctx, filter, nil)
	if err != nil {
		return err
	}
	if res.DeletedCount < 1 {
		return errors.New("no outgoing email found with the given id")
	}
	return nil
}
