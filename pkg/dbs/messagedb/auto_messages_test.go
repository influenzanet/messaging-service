package messagedb

import (
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
)

func TestAutoMessageDB(t *testing.T) {
	t1 := types.AutoMessage{
		Type:     "testtype",
		NextTime: time.Now().Unix() - 10,
		Template: types.EmailTemplate{
			DefaultLanguage: "test1",
		},
	}
	t.Run("save not existing message", func(t *testing.T) {
		var err error
		t1, err = testDBService.SaveAutoMessage(testInstanceID, t1, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("save existing message", func(t *testing.T) {
		t1.Type = "testtype2"
		res, err := testDBService.SaveAutoMessage(testInstanceID, t1, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.Type != "testtype2" {
			t.Errorf("unexpected result: %v", res)
		}
	})

	t.Run("delete existing message", func(t *testing.T) {
		err := testDBService.DeleteAutoMessage(testInstanceID, t1.ID.Hex())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})

	t.Run("delete not existing message", func(t *testing.T) {
		err := testDBService.DeleteAutoMessage(testInstanceID, t1.ID.Hex())
		if err == nil {
			t.Error("should fail when message already deleted")
			return
		}
	})

	testMessages := []types.AutoMessage{
		{
			Type:     "t1",
			NextTime: time.Now().Unix() - 10,
		},
		{
			Type:     "t3",
			NextTime: time.Now().Unix() + 10,
		},
	}
	for _, temp := range testMessages {
		_, err := testDBService.SaveAutoMessage(testInstanceID, temp, false)
		if err != nil {
			t.Errorf("unexpected error when creating test messages: %v", err)
			return
		}
	}

	t.Run("find active message", func(t *testing.T) {
		res, err := testDBService.FindAutoMessages(testInstanceID, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		logger.Debug.Println(res)
		if len(res) != 1 {
			t.Errorf("unexpected number of messages found: %d", len(res))
		}
	})
	t.Run("find all message", func(t *testing.T) {
		res, err := testDBService.FindAutoMessages(testInstanceID, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		logger.Debug.Println(res)
		if len(res) != 2 {
			t.Errorf("unexpected number of messages found: %d", len(res))
		}
	})

}

// Auto messages carry the same binding inside their template, and the scheduler re-saves them on
// every run: the preserving save keeps an operator's WhatsApp configuration across those writes.
func TestSaveAutoMessageWhatsAppBinding(t *testing.T) {
	bound := types.AutoMessage{
		Type:     "binding-type",
		NextTime: time.Now().Unix() + 3600,
		Template: types.EmailTemplate{
			DefaultLanguage:      "it",
			WhatsAppTemplateName: "weekly_reminder_v1",
			WhatsAppParams:       map[string]string{"nome": "profileAlias"},
		},
	}

	var err error
	bound, err = testDBService.SaveAutoMessage(testInstanceID, bound, false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}
	defer func() {
		if err := testDBService.DeleteAutoMessage(testInstanceID, bound.ID.Hex()); err != nil {
			t.Errorf("unexpected error during cleanup: %v", err)
		}
	}()
	if bound.Template.WhatsAppTemplateName != "weekly_reminder_v1" {
		t.Errorf("expected the binding to be stored, got %v", bound.Template)
	}

	withoutBinding := bound
	withoutBinding.Template = types.EmailTemplate{DefaultLanguage: "en"}

	t.Run("a save without the binding preserves the stored one", func(t *testing.T) {
		res, err := testDBService.SaveAutoMessage(testInstanceID, withoutBinding, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.Template.DefaultLanguage != "en" {
			t.Errorf("expected the e-mail fields to be replaced, got %v", res.Template)
		}
		if res.Template.WhatsAppTemplateName != "weekly_reminder_v1" {
			t.Errorf("expected the binding to survive, got %q", res.Template.WhatsAppTemplateName)
		}
		if res.Template.WhatsAppParams["nome"] != "profileAlias" {
			t.Errorf("expected the params to survive, got %v", res.Template.WhatsAppParams)
		}
	})

	t.Run("a save that clears the binding removes it", func(t *testing.T) {
		res, err := testDBService.SaveAutoMessage(testInstanceID, withoutBinding, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.Template.WhatsAppTemplateName != "" || len(res.Template.WhatsAppParams) > 0 {
			t.Errorf("expected the binding to be gone, got %v", res.Template)
		}
	})

	t.Run("the scheduler cannot bring a cleared binding back", func(t *testing.T) {
		restored, err := testDBService.SaveAutoMessage(testInstanceID, bound, false)
		if err != nil {
			t.Errorf("unexpected error while arranging: %v", err)
			return
		}
		// The scheduler saves the message as it read it, with the schedule advanced.
		staleCopy := restored
		staleCopy.NextTime += 3600
		if _, err := testDBService.SaveAutoMessage(testInstanceID, withoutBinding, false); err != nil {
			t.Errorf("unexpected error while clearing: %v", err)
			return
		}

		res, err := testDBService.SaveAutoMessage(testInstanceID, staleCopy, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.Template.WhatsAppTemplateName != "" || len(res.Template.WhatsAppParams) > 0 {
			t.Errorf("expected the cleared binding to stay cleared, got %v", res.Template)
		}
		if res.NextTime != staleCopy.NextTime {
			t.Errorf("expected the schedule to advance to %d, got %d", staleCopy.NextTime, res.NextTime)
		}
	})
}
