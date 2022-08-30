package types

import (
	emailClientAPI "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type EmailTemplate struct {
	ID              primitive.ObjectID  `bson:"_id,omitempty"`
	MessageType     string              `bson:"messageType"` // e.g. 'registration','invitation'..), use constants EMAIL_TYPE_* in go-utils/pkg/constants
	StudyKey        string              `bson:"studyKey,omitempty"`
	DefaultLanguage string              `bson:"defaultLanguage"`
	HeaderOverrides *HeaderOverrides    `bson:"headerOverrides"`
	Translations    []LocalizedTemplate `bson:"translations"`
}

type HeaderOverrides struct {
	From      string   `bson:"from"`
	Sender    string   `bson:"sender"`
	ReplyTo   []string `bson:"replyTo"`
	NoReplyTo bool     `bson:"noReplyTo"`
}

type LocalizedTemplate struct {
	Lang        string `bson:"languageCode"`
	Subject     string `bson:"subject"`
	TemplateDef string `bson:"templateDef"`
}

func HeaderOverridesFromAPI(obj *api.HeaderOverrides) *HeaderOverrides {
	if obj == nil {
		return nil
	}
	return &HeaderOverrides{
		From:      obj.From,
		Sender:    obj.Sender,
		ReplyTo:   obj.ReplyTo,
		NoReplyTo: obj.NoReplyTo,
	}
}

func HeaderOverridesFromEmailClientAPI(obj *emailClientAPI.HeaderOverrides) *HeaderOverrides {
	if obj == nil {
		return nil
	}
	return &HeaderOverrides{
		From:      obj.From,
		Sender:    obj.Sender,
		ReplyTo:   obj.ReplyTo,
		NoReplyTo: obj.NoReplyTo,
	}
}

func HeaderOverridesAPItoAPI(obj *api.HeaderOverrides) *emailClientAPI.HeaderOverrides {
	if obj == nil {
		return nil
	}
	return &emailClientAPI.HeaderOverrides{
		From:      obj.From,
		Sender:    obj.Sender,
		ReplyTo:   obj.ReplyTo,
		NoReplyTo: obj.NoReplyTo,
	}
}

func (obj *HeaderOverrides) ToAPI() *api.HeaderOverrides {
	if obj == nil {
		return nil
	}
	return &api.HeaderOverrides{
		From:      obj.From,
		Sender:    obj.Sender,
		ReplyTo:   obj.ReplyTo,
		NoReplyTo: obj.NoReplyTo,
	}
}

func (obj *HeaderOverrides) ToEmailClientAPI() *emailClientAPI.HeaderOverrides {
	if obj == nil {
		return nil
	}
	return &emailClientAPI.HeaderOverrides{
		From:      obj.From,
		Sender:    obj.Sender,
		ReplyTo:   obj.ReplyTo,
		NoReplyTo: obj.NoReplyTo,
	}
}

func EmailTemplateFromAPI(obj *api.EmailTemplate) EmailTemplate {
	if obj == nil {
		return EmailTemplate{}
	}
	_id, _ := primitive.ObjectIDFromHex(obj.Id)
	translations := make([]LocalizedTemplate, len(obj.Translations))
	for i, t := range obj.Translations {
		translations[i] = LocalizedTemplateFromAPI(t)
	}
	return EmailTemplate{
		ID:              _id,
		MessageType:     obj.MessageType,
		StudyKey:        obj.StudyKey,
		DefaultLanguage: obj.DefaultLanguage,
		HeaderOverrides: HeaderOverridesFromAPI(obj.HeaderOverrides),
		Translations:    translations,
	}
}

// ToAPI converts a email template object from DB format into the API format
func (obj EmailTemplate) ToAPI() *api.EmailTemplate {
	translations := make([]*api.LocalizedTemplate, len(obj.Translations))
	for i, t := range obj.Translations {
		translations[i] = t.ToAPI()
	}
	return &api.EmailTemplate{
		Id:              obj.ID.Hex(),
		MessageType:     obj.MessageType,
		StudyKey:        obj.StudyKey,
		DefaultLanguage: obj.DefaultLanguage,
		HeaderOverrides: obj.HeaderOverrides.ToAPI(),
		Translations:    translations,
	}
}

func LocalizedTemplateFromAPI(obj *api.LocalizedTemplate) LocalizedTemplate {
	if obj == nil {
		return LocalizedTemplate{}
	}
	return LocalizedTemplate{
		Lang:        obj.Lang,
		Subject:     obj.Subject,
		TemplateDef: obj.TemplateDef,
	}
}

// ToAPI converts a localized template from DB format into the API format
func (obj LocalizedTemplate) ToAPI() *api.LocalizedTemplate {
	return &api.LocalizedTemplate{
		Lang:        obj.Lang,
		Subject:     obj.Subject,
		TemplateDef: obj.TemplateDef,
	}
}
