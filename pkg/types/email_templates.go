package types

import (
	emailClientAPI "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type EmailTemplate struct {
	ID                   primitive.ObjectID  `bson:"_id,omitempty"`
	MessageType          string              `bson:"messageType"` // e.g. 'registration','invitation'..), use constants EMAIL_TYPE_* in go-utils/pkg/constants
	StudyKey             string              `bson:"studyKey,omitempty"`
	DefaultLanguage      string              `bson:"defaultLanguage"`
	HeaderOverrides      *HeaderOverrides    `bson:"headerOverrides"`
	Translations         []LocalizedTemplate `bson:"translations"`
	WhatsAppTemplateName string              `bson:"whatsappTemplateName,omitempty"`
	WhatsAppParams       map[string]string   `bson:"whatsappParams,omitempty"`
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
	// The binding is one unit: parameters are the arguments of the template named next to them,
	// and cannot be delivered on their own. A request that names no template therefore carries no
	// parameters either, so clearing a binding cannot leave its parameters behind.
	whatsAppParams := obj.GetWhatsappParams()
	if obj.GetWhatsappTemplateName() == "" {
		whatsAppParams = nil
	}
	return EmailTemplate{
		ID:                   _id,
		MessageType:          obj.MessageType,
		StudyKey:             obj.StudyKey,
		DefaultLanguage:      obj.DefaultLanguage,
		HeaderOverrides:      HeaderOverridesFromAPI(obj.HeaderOverrides),
		Translations:         translations,
		WhatsAppTemplateName: obj.GetWhatsappTemplateName(),
		WhatsAppParams:       whatsAppParams,
	}
}

// ToAPI converts a email template object from DB format into the API format
func (obj EmailTemplate) ToAPI() *api.EmailTemplate {
	translations := make([]*api.LocalizedTemplate, len(obj.Translations))
	for i, t := range obj.Translations {
		translations[i] = t.ToAPI()
	}
	template := &api.EmailTemplate{
		Id:              obj.ID.Hex(),
		MessageType:     obj.MessageType,
		StudyKey:        obj.StudyKey,
		DefaultLanguage: obj.DefaultLanguage,
		HeaderOverrides: obj.HeaderOverrides.ToAPI(),
		Translations:    translations,
		WhatsappParams:  obj.WhatsAppParams,
	}
	// The name is sent only when a binding exists, so a client that reads a message without one
	// and sends it back does not read as asking to clear a binding set meanwhile.
	if obj.WhatsAppTemplateName != "" {
		name := obj.WhatsAppTemplateName
		template.WhatsappTemplateName = &name
	}
	return template
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
