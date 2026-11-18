package clients

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

// sendWithResponse sends one template message to a server that answers with status and body,
// and returns what SendTemplateMessage returned and everything it logged.
func sendWithResponse(t *testing.T, status int, body string) (wamid string, logs string, err error) {
	t.Helper()
	var logged bytes.Buffer
	for _, l := range []*log.Logger{logger.Info, logger.Warning, logger.Error} {
		prev := l.Writer()
		l.SetOutput(&logged)
		t.Cleanup(func() { l.SetOutput(prev) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c := NewWhatsAppClient("test-token", "test-phone-id", "")
	c.apiBaseURL = server.URL

	wamid, err = c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", nil)
	return wamid, logged.String(), err
}

// Meta's answer to an accepted send, as documented: the recipient echoed back in contacts, and
// the message id in messages.
const acceptedResponse = `{"messaging_product":"whatsapp",` +
	`"contacts":[{"input":"+391234567890","wa_id":"391234567890"}],` +
	`"messages":[{"id":"wamid.HBgLMzkxMjM0NTY3ODkwFQIAERgSQUJDREVGMDEyMzQ1Njc4OQA=","message_status":"accepted"}]}`

// The id Meta gives an accepted message is returned, so that the scheduler can store it and
// match the delivery statuses Meta reports later.
// Fails if the id is not read from messages[0].id, or is altered on the way.
func TestSendTemplateMessageReturnsTheIDMetaGave(t *testing.T) {
	wamid, _, err := sendWithResponse(t, http.StatusOK, acceptedResponse)
	if err != nil {
		t.Fatalf("an accepted send returned an error: %v", err)
	}
	if want := "wamid.HBgLMzkxMjM0NTY3ODkwFQIAERgSQUJDREVGMDEyMzQ1Njc4OQA="; wamid != want {
		t.Fatalf("message id = %q, want %q", wamid, want)
	}
}

// A 2xx answer means Meta accepted the message, whether or not the response names an id: the
// send is not an error, or the scheduler would send the message again and the participant
// would receive it twice. The missing id is logged as a warning, without the response body,
// which echoes the recipient's number.
// Fails if an accepted send without a readable id is reported as a failure, if an id is made
// up, if the warning is dropped, or if the body reaches the log.
func TestSendTemplateMessageAcceptedWithoutAnIDIsNotAnError(t *testing.T) {
	echo := `"contacts":[{"input":"+391234567890","wa_id":"391234567890"}]`
	for name, body := range map[string]string{
		"empty body":     ``,
		"not JSON":       `OK`,
		"truncated JSON": `{` + echo + `,"messages":[{"id":"wamid.A`,
		"no messages":    `{` + echo + `}`,
		"empty messages": `{` + echo + `,"messages":[]}`,
		"empty id":       `{` + echo + `,"messages":[{"id":""}]}`,
		"blank id":       `{` + echo + `,"messages":[{"id":"  "}]}`,
		// The id past the read limit: a response is not read without bound.
		"id past the limit": `{"padding":"` + strings.Repeat("x", maxAcceptedResponseBytes) + `",` + echo + `,"messages":[{"id":"wamid.A"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			wamid, logs, err := sendWithResponse(t, http.StatusOK, body)
			if err != nil {
				t.Fatalf("an accepted send was reported as failed: %v", err)
			}
			if wamid != "" {
				t.Fatalf("message id = %q, want none", wamid)
			}
			if !strings.Contains(logs, "names no message id") {
				t.Errorf("no warning about the missing id: %q", logs)
			}
			if strings.Contains(logs, "1234567890") {
				t.Errorf("the log holds the recipient's full number: %q", logs)
			}
		})
	}
}

// A refused send names no id, even when Meta's error response happens to carry one.
// Fails if the id is read on the error path, where the scheduler would store it on a message
// Meta never accepted.
func TestSendTemplateMessageRefusedReturnsNoID(t *testing.T) {
	wamid, _, err := sendWithResponse(t, http.StatusBadRequest,
		`{"error":{"code":131026},"messages":[{"id":"wamid.A"}]}`)
	if err == nil {
		t.Fatal("a refused send returned no error")
	}
	if wamid != "" {
		t.Fatalf("a refused send returned the message id %q", wamid)
	}
}

// An accepted send with an id logs no warning: the warning is reserved for the missing id.
// Fails if the warning is logged on every send, which would make it useless as a signal.
func TestSendTemplateMessageWithAnIDLogsNoWarning(t *testing.T) {
	_, logs, _ := sendWithResponse(t, http.StatusOK, acceptedResponse)
	if strings.Contains(logs, "names no message id") {
		t.Fatalf("a send whose response names its id warned about a missing id: %q", logs)
	}
}
