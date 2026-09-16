package clients

// Language matrix, last hop: what the message-scheduler actually puts in the Meta payload.
// The scheduler hands OutgoingWhatsApp.Lang to SendTemplateMessage unchanged
// (cmd/message-scheduler/main.go:524), which runs it through mapLanguageCode
// (whatsapp_client.go:74) and writes it as template.language.code.
// These tests document today's behaviour and change no production code.

import (
	"context"
	"strings"
	"testing"
)

// langsToMeta are the languages the generators can put on a queued message.
var langsToMeta = []string{"it", "en", "fr", "de", "rm", ""}

func TestMapLanguageCodeIsIdentity(t *testing.T) {
	for _, lang := range langsToMeta {
		got := mapLanguageCode(lang)
		if got != lang {
			t.Errorf("mapLanguageCode(%q) = %q, expected the identity mapping", lang, got)
		}
	}
	t.Logf("mapLanguageCode is the identity for every language the generators produce: %v", langsToMeta)
}

// TestLanguageCodeSentToMeta captures the outgoing request for each language and reports the
// language.code Meta would receive.
func TestLanguageCodeSentToMeta(t *testing.T) {
	rows := make([]string, 0, len(langsToMeta))
	for _, lang := range langsToMeta {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", lang, map[string]string{"name": "Mario"})
		if err != nil {
			t.Fatalf("lang %q: unexpected error: %v", lang, err)
		}
		code := captured.Template.Language.Code
		if code != lang {
			t.Errorf("lang %q: Meta would receive language.code %q", lang, code)
		}
		label := lang
		if label == "" {
			label = "(empty)"
		}
		rows = append(rows, "| "+label+" | `\"code\": \""+code+"\"` |")
	}
	t.Logf("language handed to the Meta Graph API\n| queued Lang | JSON sent |\n|---|---|\n%s", strings.Join(rows, "\n"))
}

// TestEmptyLanguageReachesMetaVerbatim records that nothing below the generators guards an
// empty language: the client serialises an empty code rather than dropping the field or
// refusing the send. Only prepareOutgoingWhatsApp (bulk_messages.go:597) stops an empty
// language today, so a queue row written by anything else would reach Meta like this.
func TestEmptyLanguageReachesMetaVerbatim(t *testing.T) {
	c, captured := newCaptureClient(t)
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Template.Language.Code != "" {
		t.Errorf("expected an empty language.code, got %q", captured.Template.Language.Code)
	}
	t.Logf("empty Lang is sent as language.code=%q, the client does not reject it", captured.Template.Language.Code)
}
