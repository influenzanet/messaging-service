package messagedb

import (
	"errors"
	"slices"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// whatsAppBindingFields are the fields that tie a message to the WhatsApp template it is
// delivered with. They are configured on their own, so a client that saves the e-mail definition
// alone must be able to leave them where they are.
var whatsAppBindingFields = []string{"whatsappTemplateName", "whatsappParams"}

// keepStoredWhatsAppBinding builds the object that carries the stored binding back on top of an
// incoming document. A field with no stored value evaluates to $$REMOVE and contributes nothing,
// so a message that never had a binding does not gain an empty one.
func keepStoredWhatsAppBinding(prefix string) bson.M {
	kept := bson.M{}
	for _, field := range whatsAppBindingFields {
		kept[field] = bson.M{"$ifNull": bson.A{"$" + prefix + field, "$$REMOVE"}}
	}
	return kept
}

// asBSONDocument marshals a value into an ordered BSON document.
func asBSONDocument(document interface{}) (bson.D, error) {
	raw, err := bson.Marshal(document)
	if err != nil {
		return nil, err
	}
	var asDocument bson.D
	if err := bson.Unmarshal(raw, &asDocument); err != nil {
		return nil, err
	}
	return asDocument, nil
}

// literalDocument wraps a document so the pipeline stores its values verbatim: without $literal a
// subject holding a "$" would be parsed as a field path.
func literalDocument(document interface{}) (bson.M, error) {
	asDocument, err := asBSONDocument(document)
	if err != nil {
		return nil, err
	}
	return bson.M{"$literal": asDocument}, nil
}

// withoutWhatsAppBinding drops the binding fields from an incoming document. A caller that reaches
// the preserving path expressed no intent about the binding, so the copy it happens to carry must
// not be written: without this a caller holding a stale copy, the scheduler above all, would write
// a binding back over one that was cleared meanwhile.
func withoutWhatsAppBinding(document bson.D) bson.D {
	kept := make(bson.D, 0, len(document))
	for _, element := range document {
		if slices.Contains(whatsAppBindingFields, element.Key) {
			continue
		}
		kept = append(kept, element)
	}
	return kept
}

// mergeKeepingWhatsAppBinding turns a document into the expression that writes it whole while
// carrying the stored binding over, in one operation. Reading the binding first and replacing
// afterwards would let a concurrent save land in between and lose it. The incoming binding is
// dropped first, so the result is the stored binding or none at all.
func mergeKeepingWhatsAppBinding(document interface{}, prefix string) (bson.M, error) {
	asDocument, err := asBSONDocument(document)
	if err != nil {
		return nil, err
	}
	incoming := bson.M{"$literal": withoutWhatsAppBinding(asDocument)}
	return bson.M{"$mergeObjects": bson.A{incoming, keepStoredWhatsAppBinding(prefix)}}, nil
}

// SaveEmailTemplate writes the template, replacing the message definition as sent. When
// preserveWhatsAppBinding is set the caller sent no WhatsApp section, so the binding already
// stored is kept: importing an e-mail template no longer switches the WhatsApp channel of that
// message off without saying so.
func (dbService *MessageDBService) SaveEmailTemplate(instanceID string, template types.EmailTemplate, preserveWhatsAppBinding bool) (types.EmailTemplate, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{
		"messageType": template.MessageType,
		"studyKey":    template.StudyKey,
	}
	if template.StudyKey == "" {
		filter["studyKey"] = bson.M{"$exists": false}
	}

	elem := types.EmailTemplate{}
	collection := dbService.collectionRefEmailTemplates(instanceID)

	if !preserveWhatsAppBinding {
		opts := options.FindOneAndReplace().SetUpsert(true).SetReturnDocument(options.After)
		err := collection.FindOneAndReplace(ctx, filter, template, opts).Decode(&elem)
		return elem, err
	}

	merged, err := mergeKeepingWhatsAppBinding(template, "")
	if err != nil {
		return elem, err
	}
	update := mongo.Pipeline{bson.D{{Key: "$replaceWith", Value: merged}}}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	err = collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&elem)
	return elem, err
}

func (dbService *MessageDBService) DeleteEmailTemplate(instanceID string, messageType string, studyKey string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{
		"messageType": messageType,
		"studyKey":    studyKey,
	}
	if studyKey == "" {
		filter["studyKey"] = bson.M{"$exists": false}
	}
	res, err := dbService.collectionRefEmailTemplates(instanceID).DeleteOne(ctx, filter)
	if res.DeletedCount < 1 {
		err = errors.New("not found")
	}
	return err
}

func (dbService *MessageDBService) FindEmailTemplateByType(instanceID string, messageType string, studyKey string) (types.EmailTemplate, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{
		"messageType": messageType,
		"studyKey":    studyKey,
	}
	if studyKey == "" {
		filter["studyKey"] = bson.M{"$exists": false}
	}

	elem := types.EmailTemplate{}
	err := dbService.collectionRefEmailTemplates(instanceID).FindOne(ctx, filter).Decode(&elem)
	return elem, err
}

func (dbService *MessageDBService) FindAllEmailTempates(instanceID string) (templates []types.EmailTemplate, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{}
	cur, err := dbService.collectionRefEmailTemplates(instanceID).Find(
		ctx,
		filter,
	)

	if err != nil {
		return templates, err
	}
	defer cur.Close(ctx)

	templates = []types.EmailTemplate{}
	for cur.Next(ctx) {
		var result types.EmailTemplate
		err := cur.Decode(&result)
		if err != nil {
			return templates, err
		}

		templates = append(templates, result)
	}
	if err := cur.Err(); err != nil {
		return templates, err
	}

	return templates, nil
}
