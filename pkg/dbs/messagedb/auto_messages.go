package messagedb

import (
	"errors"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (dbService *MessageDBService) SaveAutoMessage(instanceID string, messageDef types.AutoMessage) (types.AutoMessage, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	if !messageDef.ID.IsZero() {
		filter := bson.M{"_id": messageDef.ID}

		upsert := false
		rd := options.After
		options := options.FindOneAndReplaceOptions{
			Upsert:         &upsert,
			ReturnDocument: &rd,
		}
		elem := types.AutoMessage{}
		err := dbService.collectionRefAutoMessages(instanceID).FindOneAndReplace(
			ctx, filter, messageDef, &options,
		).Decode(&elem)
		return elem, err
	} else {
		res, err := dbService.collectionRefAutoMessages(instanceID).InsertOne(ctx, messageDef)
		if err != nil {
			return messageDef, err
		}
		messageDef.ID = res.InsertedID.(primitive.ObjectID)
		return messageDef, nil
	}
}

func (dbService *MessageDBService) DeleteAutoMessage(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}

	res, err := dbService.collectionRefAutoMessages(instanceID).DeleteOne(ctx, filter)
	if res.DeletedCount < 1 {
		err = errors.New("not found")
	}
	return err
}

func (dbService *MessageDBService) FindAutoMessages(instanceID string, onlyActives bool) (messages []types.AutoMessage, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{}
	if onlyActives {
		filter["nextTime"] = bson.M{"$lt": time.Now().Unix()}
	}

	cur, err := dbService.collectionRefAutoMessages(instanceID).Find(
		ctx,
		filter,
	)

	if err != nil {
		return messages, err
	}
	defer cur.Close(ctx)

	messages = []types.AutoMessage{}
	for cur.Next(ctx) {
		var result types.AutoMessage
		err := cur.Decode(&result)
		if err != nil {
			return messages, err
		}

		messages = append(messages, result)
	}
	if err := cur.Err(); err != nil {
		return messages, err
	}

	return messages, nil
}
