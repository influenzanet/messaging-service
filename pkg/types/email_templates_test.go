package types

import (
	"testing"

	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
)

// The binding between a message and its WhatsApp template lives in two fields that used to be
// stored in the database only. These tests pin them to the API contract in both directions, so a
// client can read a binding, send it back, and find it unchanged.
func TestEmailTemplateWhatsAppBindingRoundTrip(t *testing.T) {
	name := "weekly_reminder_v1"

	t.Run("from API keeps the binding", func(t *testing.T) {
		converted := EmailTemplateFromAPI(&api.EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			WhatsappTemplateName: &name,
			WhatsappParams:       map[string]string{"nome": "profileAlias"},
		})
		if converted.WhatsAppTemplateName != name {
			t.Errorf("expected template name %q, got %q", name, converted.WhatsAppTemplateName)
		}
		if converted.WhatsAppParams["nome"] != "profileAlias" {
			t.Errorf("unexpected params: %v", converted.WhatsAppParams)
		}
	})

	t.Run("to API exposes the binding", func(t *testing.T) {
		converted := EmailTemplate{
			MessageType:          "weekly",
			WhatsAppTemplateName: name,
			WhatsAppParams:       map[string]string{"nome": "profileAlias"},
		}.ToAPI()
		if converted.WhatsappTemplateName == nil || *converted.WhatsappTemplateName != name {
			t.Errorf("expected template name %q, got %v", name, converted.WhatsappTemplateName)
		}
		if converted.WhatsappParams["nome"] != "profileAlias" {
			t.Errorf("unexpected params: %v", converted.WhatsappParams)
		}
	})

	t.Run("from API drops params that name no template", func(t *testing.T) {
		converted := EmailTemplateFromAPI(&api.EmailTemplate{
			MessageType:    "weekly",
			WhatsappParams: map[string]string{"nome": "profileAlias"},
		})
		if len(converted.WhatsAppParams) > 0 {
			t.Errorf("expected no params without a template name, got %v", converted.WhatsAppParams)
		}
	})

	t.Run("from API drops params when the binding is cleared", func(t *testing.T) {
		cleared := ""
		converted := EmailTemplateFromAPI(&api.EmailTemplate{
			MessageType:          "weekly",
			WhatsappTemplateName: &cleared,
			WhatsappParams:       map[string]string{"nome": "profileAlias"},
		})
		if converted.WhatsAppTemplateName != "" || len(converted.WhatsAppParams) > 0 {
			t.Errorf("expected the whole binding to be cleared, got %v", converted)
		}
	})

	t.Run("to API omits the name when there is no binding", func(t *testing.T) {
		converted := EmailTemplate{MessageType: "weekly"}.ToAPI()
		if converted.WhatsappTemplateName != nil {
			t.Errorf("expected no template name, got %q", *converted.WhatsappTemplateName)
		}
	})

	t.Run("a full round trip changes nothing", func(t *testing.T) {
		original := EmailTemplate{
			MessageType:          "weekly",
			DefaultLanguage:      "it",
			WhatsAppTemplateName: name,
			WhatsAppParams:       map[string]string{"nome": "profileAlias", "link": "loginUrl"},
		}
		back := EmailTemplateFromAPI(original.ToAPI())
		if back.WhatsAppTemplateName != original.WhatsAppTemplateName {
			t.Errorf("expected template name %q, got %q", original.WhatsAppTemplateName, back.WhatsAppTemplateName)
		}
		if len(back.WhatsAppParams) != len(original.WhatsAppParams) {
			t.Errorf("unexpected params: %v", back.WhatsAppParams)
		}
		for k, v := range original.WhatsAppParams {
			if back.WhatsAppParams[k] != v {
				t.Errorf("param %q: expected %q, got %q", k, v, back.WhatsAppParams[k])
			}
		}
	})
}
