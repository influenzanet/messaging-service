package types

import "testing"

// TestC2_APIRoundTripDropsWhatsAppConfig demonstrates finding C2 at its root, with no
// database and no gRPC server involved.
//
// The admin UI edits a template by reading it (ToAPI), changing a field, and sending it
// back (EmailTemplateFromAPI). Neither conversion carries WhatsAppTemplateName or
// WhatsAppParams, because api.EmailTemplate has no such fields — so a round trip through
// the API type silently discards the WhatsApp configuration.
//
// Run with:  go test ./pkg/types/ -run TestC2 -v
func TestC2_APIRoundTripDropsWhatsAppConfig(t *testing.T) {
	// What an operator configured by hand in MongoDB.
	stored := EmailTemplate{
		MessageType:     "weekly",
		StudyKey:        "influenza-2024",
		DefaultLanguage: "it",
		Translations: []LocalizedTemplate{
			{Lang: "it", Subject: "Questionario settimanale", TemplateDef: "dGVzdA=="},
		},
		WhatsAppTemplateName: "weekly_reminder_v1",
		WhatsAppParams: map[string]string{
			"study_key": "studyKey",
			"login_url": "loginUrl",
		},
	}

	t.Logf("stored in MongoDB     -> whatsappTemplateName=%q whatsappParams=%v",
		stored.WhatsAppTemplateName, stored.WhatsAppParams)

	// The admin UI loads it (GetEmailTemplates -> ToAPI) and saves it back
	// (SaveEmailTemplate -> EmailTemplateFromAPI). Nothing else is changed.
	roundTripped := EmailTemplateFromAPI(stored.ToAPI())

	t.Logf("after API round trip  -> whatsappTemplateName=%q whatsappParams=%v",
		roundTripped.WhatsAppTemplateName, roundTripped.WhatsAppParams)

	// The email side survives, which is why the loss is invisible in the UI.
	if roundTripped.MessageType != stored.MessageType ||
		roundTripped.DefaultLanguage != stored.DefaultLanguage ||
		len(roundTripped.Translations) != len(stored.Translations) {
		t.Fatalf("precondition failed: the email fields should survive the round trip, got %+v", roundTripped)
	}
	t.Log("email fields survived the round trip (messageType, defaultLanguage, translations)")

	if roundTripped.WhatsAppTemplateName != stored.WhatsAppTemplateName {
		t.Errorf("C2: whatsappTemplateName was dropped by the API round trip: %q -> %q",
			stored.WhatsAppTemplateName, roundTripped.WhatsAppTemplateName)
	}
	if len(roundTripped.WhatsAppParams) != len(stored.WhatsAppParams) {
		t.Errorf("C2: whatsappParams were dropped by the API round trip: %v -> %v",
			stored.WhatsAppParams, roundTripped.WhatsAppParams)
	}

	// This is the condition prepareOutgoingWhatsApp checks first: an empty template name
	// means "no WhatsApp for this message type", so delivery stops silently.
	if roundTripped.WhatsAppTemplateName == "" {
		t.Log("consequence: prepareOutgoingWhatsApp() now returns nil for this message type — WhatsApp delivery is off")
	}
}
