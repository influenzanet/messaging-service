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

// SaveAutoMessage writes the auto message as sent. When preserveWhatsAppBinding is set the caller
// sent no WhatsApp section inside the template, so the binding already stored is kept, the same
// rule as for e-mail templates, and it also protects the message from the scheduler, which
// re-saves it on every run.
func (dbService *MessageDBService) SaveAutoMessage(instanceID string, messageDef types.AutoMessage, preserveWhatsAppBinding bool) (types.AutoMessage, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	if messageDef.ID.IsZero() {
		res, err := dbService.collectionRefAutoMessages(instanceID).InsertOne(ctx, messageDef)
		if err != nil {
			return messageDef, err
		}
		messageDef.ID = res.InsertedID.(primitive.ObjectID)
		return messageDef, nil
	}

	filter := bson.M{"_id": messageDef.ID}
	elem := types.AutoMessage{}
	collection := dbService.collectionRefAutoMessages(instanceID)

	if !preserveWhatsAppBinding {
		opts := options.FindOneAndReplace().SetReturnDocument(options.After)
		err := collection.FindOneAndReplace(ctx, filter, messageDef, opts).Decode(&elem)
		return elem, err
	}

	// The binding sits inside the template subdocument, so the template is merged on its own and
	// the result is written back over the incoming message.
	mergedTemplate, err := mergeKeepingWhatsAppBinding(messageDef.Template, "template.")
	if err != nil {
		return elem, err
	}
	message, err := literalDocument(messageDef)
	if err != nil {
		return elem, err
	}
	merged := bson.M{"$mergeObjects": bson.A{message, bson.M{"template": mergedTemplate}}}
	update := mongo.Pipeline{bson.D{{Key: "$replaceWith", Value: merged}}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	err = collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&elem)
	return elem, err
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
