package messagedb

import (
	"testing"

	"github.com/influenzanet/messaging-service/pkg/types"
)

func TestEmailTemplatesDB(t *testing.T) {
	t1 := types.EmailTemplate{
		MessageType:     "test-type",
		StudyKey:        "test-study",
		DefaultLanguage: "en",
	}
	t.Run("save not existing template", func(t *testing.T) {
		_, err := testDBService.SaveEmailTemplate(testInstanceID, t1, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("save existing template", func(t *testing.T) {
		t1.DefaultLanguage = "de"
		res, err := testDBService.SaveEmailTemplate(testInstanceID, t1, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.DefaultLanguage != "de" {
			t.Errorf("unexpected result: %v", res)
		}
	})

	t.Run("delete existing template", func(t *testing.T) {
		err := testDBService.DeleteEmailTemplate(testInstanceID, t1.MessageType, t1.StudyKey)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})

	t.Run("delete not existing template", func(t *testing.T) {
		err := testDBService.DeleteEmailTemplate(testInstanceID, t1.MessageType, t1.StudyKey)
		if err == nil {
			t.Error("should fail when template already deleted")
			return
		}
	})

	testTemplates := []types.EmailTemplate{
		{
			MessageType:     "t1",
			StudyKey:        "test-study",
			DefaultLanguage: "en",
		},
		{
			MessageType:     "t2",
			DefaultLanguage: "de",
		},
		{
			MessageType:     "t2",
			StudyKey:        "for study",
			DefaultLanguage: "fr",
		},
	}
	for _, temp := range testTemplates {
		_, err := testDBService.SaveEmailTemplate(testInstanceID, temp, false)
		if err != nil {
			t.Errorf("unexpected error when creating test templates: %v", err)
			return
		}
	}

	t.Run("find template by message type", func(t *testing.T) {
		res, err := testDBService.FindEmailTemplateByType(testInstanceID, "t2", "")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.DefaultLanguage != "de" || res.StudyKey != "" {
			t.Errorf("unexpected template found: %v", res)
		}
	})

	t.Run("find template by message type and study key", func(t *testing.T) {
		res, err := testDBService.FindEmailTemplateByType(testInstanceID, "t2", "for study")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.DefaultLanguage != "fr" || res.StudyKey != "for study" {
			t.Errorf("unexpected template found: %v", res)
		}
	})

	t.Run("find all templates", func(t *testing.T) {
		res, err := testDBService.FindAllEmailTempates(testInstanceID)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(res) != 3 {
			t.Errorf("unexpected number of templates found: %d", len(res))
		}
	})

}

// A save that carries no WhatsApp section must not silently drop the binding an operator
// configured earlier: the CLI and the management API rebuild the message from the e-mail
// definition alone, and every such save used to switch the WhatsApp channel of that message off.
func TestSaveEmailTemplateWhatsAppBinding(t *testing.T) {
	bound := types.EmailTemplate{
		MessageType:          "binding-type",
		StudyKey:             "binding-study",
		DefaultLanguage:      "it",
		WhatsAppTemplateName: "weekly_reminder_v1",
		WhatsAppParams:       map[string]string{"nome": "profileAlias"},
	}
	withoutBinding := types.EmailTemplate{
		MessageType:     bound.MessageType,
		StudyKey:        bound.StudyKey,
		DefaultLanguage: "en",
	}
	defer func() {
		if err := testDBService.DeleteEmailTemplate(testInstanceID, bound.MessageType, bound.StudyKey); err != nil {
			t.Errorf("unexpected error during cleanup: %v", err)
		}
	}()

	t.Run("a save that carries the binding stores it", func(t *testing.T) {
		res, err := testDBService.SaveEmailTemplate(testInstanceID, bound, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.WhatsAppTemplateName != bound.WhatsAppTemplateName || res.WhatsAppParams["nome"] != "profileAlias" {
			t.Errorf("unexpected result: %v", res)
		}
	})

	t.Run("a save without the binding preserves the stored one", func(t *testing.T) {
		res, err := testDBService.SaveEmailTemplate(testInstanceID, withoutBinding, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.DefaultLanguage != "en" {
			t.Errorf("expected the e-mail fields to be replaced, got %v", res)
		}
		if res.WhatsAppTemplateName != bound.WhatsAppTemplateName {
			t.Errorf("expected the binding to survive, got %q", res.WhatsAppTemplateName)
		}
		if res.WhatsAppParams["nome"] != "profileAlias" {
			t.Errorf("expected the params to survive, got %v", res.WhatsAppParams)
		}
	})

	t.Run("a save that clears the binding removes it", func(t *testing.T) {
		res, err := testDBService.SaveEmailTemplate(testInstanceID, withoutBinding, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.WhatsAppTemplateName != "" || len(res.WhatsAppParams) > 0 {
			t.Errorf("expected the binding to be gone, got %v", res)
		}
	})

	t.Run("preserving on a template that never had a binding adds nothing", func(t *testing.T) {
		res, err := testDBService.SaveEmailTemplate(testInstanceID, withoutBinding, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.WhatsAppTemplateName != "" || len(res.WhatsAppParams) > 0 {
			t.Errorf("expected no binding, got %v", res)
		}
	})

	t.Run("a stale copy cannot bring a cleared binding back", func(t *testing.T) {
		if _, err := testDBService.SaveEmailTemplate(testInstanceID, bound, false); err != nil {
			t.Errorf("unexpected error while arranging: %v", err)
			return
		}
		// What a caller that read the template before the clear still holds in memory.
		staleCopy := bound
		if _, err := testDBService.SaveEmailTemplate(testInstanceID, withoutBinding, false); err != nil {
			t.Errorf("unexpected error while clearing: %v", err)
			return
		}

		res, err := testDBService.SaveEmailTemplate(testInstanceID, staleCopy, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.WhatsAppTemplateName != "" || len(res.WhatsAppParams) > 0 {
			t.Errorf("expected the cleared binding to stay cleared, got %v", res)
		}
	})

	t.Run("preserving does not store params the caller carries on its own", func(t *testing.T) {
		if _, err := testDBService.SaveEmailTemplate(testInstanceID, withoutBinding, false); err != nil {
			t.Errorf("unexpected error while arranging: %v", err)
			return
		}
		paramsOnly := withoutBinding
		paramsOnly.WhatsAppParams = map[string]string{"nome": "profileAlias"}

		res, err := testDBService.SaveEmailTemplate(testInstanceID, paramsOnly, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(res.WhatsAppParams) > 0 {
			t.Errorf("expected no params without a template name, got %v", res.WhatsAppParams)
		}
	})
}
