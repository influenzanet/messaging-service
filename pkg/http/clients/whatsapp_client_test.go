package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"+391234567890", "+39***7890"},
		{"+1555", "***"},
		{"", "***"},
		{"+44207", "***"},
		{"+447911123456", "+44***3456"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := maskPhone(tt.input)
			if got != tt.expected {
				t.Errorf("maskPhone(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNewWhatsAppClient(t *testing.T) {
	t.Run("returns nil when token empty", func(t *testing.T) {
		c := NewWhatsAppClient("", "phone-id")
		if c != nil {
			t.Error("expected nil client when token is empty")
		}
	})

	t.Run("returns nil when phoneID empty", func(t *testing.T) {
		c := NewWhatsAppClient("token", "")
		if c != nil {
			t.Error("expected nil client when phoneID is empty")
		}
	})

	t.Run("returns client with timeout when configured", func(t *testing.T) {
		c := NewWhatsAppClient("token", "phone-id")
		if c == nil {
			t.Fatal("expected non-nil client")
		}
		if c.httpClient.Timeout != 30*time.Second {
			t.Errorf("expected 30s timeout, got %v", c.httpClient.Timeout)
		}
	})
}

type capturedTemplate struct {
	Name     string `json:"name"`
	Language struct {
		Code string `json:"code"`
	} `json:"language"`
	Components []struct {
		Type       string                   `json:"type"`
		SubType    string                   `json:"sub_type"`
		Index      string                   `json:"index"`
		Parameters []map[string]interface{} `json:"parameters"`
	} `json:"components"`
}

type capturedPayload struct {
	MessagingProduct string           `json:"messaging_product"`
	To               string           `json:"to"`
	Type             string           `json:"type"`
	Template         capturedTemplate `json:"template"`
}

// newCaptureClient starts a test server that records the request payload
// and returns a client pointed at it.
func newCaptureClient(t *testing.T) (*WhatsAppClient, *capturedPayload) {
	t.Helper()
	captured := &capturedPayload{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Errorf("failed to decode request payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	c := NewWhatsAppClient("test-token", "test-phone-id")
	c.apiBaseURL = server.URL
	return c, captured
}

func TestSendTemplateMessageHeaderComponents(t *testing.T) {
	t.Run("text header param produces header component before body", func(t *testing.T) {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{
			"header": "Weekly survey",
			"name":   "Mario",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := captured.Template.Components
		if len(comps) != 2 {
			t.Fatalf("expected 2 components (header, body), got %d: %+v", len(comps), comps)
		}
		if comps[0].Type != "header" {
			t.Errorf("expected first component to be header, got %q", comps[0].Type)
		}
		if len(comps[0].Parameters) != 1 || comps[0].Parameters[0]["type"] != "text" || comps[0].Parameters[0]["text"] != "Weekly survey" {
			t.Errorf("unexpected header parameters: %+v", comps[0].Parameters)
		}
		if comps[1].Type != "body" {
			t.Errorf("expected second component to be body, got %q", comps[1].Type)
		}
		if len(comps[1].Parameters) != 1 || comps[1].Parameters[0]["parameter_name"] != "name" || comps[1].Parameters[0]["text"] != "Mario" {
			t.Errorf("header key leaked into body or body param missing: %+v", comps[1].Parameters)
		}
	})

	t.Run("media header params produce link-based header component", func(t *testing.T) {
		mediaCases := []struct {
			key       string
			mediaType string
		}{
			{"header_image", "image"},
			{"header_video", "video"},
			{"header_document", "document"},
		}
		for _, mc := range mediaCases {
			t.Run(mc.key, func(t *testing.T) {
				c, captured := newCaptureClient(t)
				link := "https://example.com/asset"
				err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{
					mc.key: link,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				comps := captured.Template.Components
				if len(comps) != 1 {
					t.Fatalf("expected 1 header component, got %d: %+v", len(comps), comps)
				}
				if comps[0].Type != "header" {
					t.Errorf("expected header component, got %q", comps[0].Type)
				}
				params := comps[0].Parameters
				if len(params) != 1 || params[0]["type"] != mc.mediaType {
					t.Fatalf("unexpected header parameters: %+v", params)
				}
				media, ok := params[0][mc.mediaType].(map[string]interface{})
				if !ok || media["link"] != link {
					t.Errorf("expected %s link %q, got %+v", mc.mediaType, link, params[0])
				}
			})
		}
	})

	t.Run("header body and button components are ordered deterministically", func(t *testing.T) {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "en", map[string]string{
			"button_0": "verify/abc",
			"header":   "Hello",
			"name":     "Mario",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := captured.Template.Components
		if len(comps) != 3 {
			t.Fatalf("expected 3 components, got %d: %+v", len(comps), comps)
		}
		order := []string{comps[0].Type, comps[1].Type, comps[2].Type}
		want := []string{"header", "body", "button"}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("expected component order %v, got %v", want, order)
			}
		}
		if comps[2].SubType != "url" || comps[2].Index != "0" {
			t.Errorf("button component lost its sub_type/index: %+v", comps[2])
		}
	})

	t.Run("body-only params keep existing behaviour", func(t *testing.T) {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "en", map[string]string{
			"name": "Mario",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := captured.Template.Components
		if len(comps) != 1 || comps[0].Type != "body" {
			t.Fatalf("expected single body component, got %+v", comps)
		}
	})

	// Meta requires a template's parameters to be all named or all positional. The body has
	// carried parameter_name since the named-parameter templates were introduced, so a text
	// header on the same template needs a name too — "header:<name>" supplies it, while the
	// bare "header" key stays positional for templates built that way.
	t.Run("named text header carries its parameter name", func(t *testing.T) {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{
			"header:campaign": "Weekly survey",
			"name":            "Mario",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := captured.Template.Components
		if len(comps) != 2 || comps[0].Type != "header" {
			t.Fatalf("expected header and body components, got %+v", comps)
		}
		p := comps[0].Parameters
		if len(p) != 1 || p[0]["type"] != "text" || p[0]["text"] != "Weekly survey" {
			t.Fatalf("unexpected header parameters: %+v", p)
		}
		if p[0]["parameter_name"] != "campaign" {
			t.Errorf("named header must carry parameter_name %q, got %+v", "campaign", p[0])
		}
		if comps[1].Parameters[0]["parameter_name"] != "name" {
			t.Errorf("the named header key leaked into the body: %+v", comps[1].Parameters)
		}
	})

	t.Run("rejects an unknown header media type instead of inventing one", func(t *testing.T) {
		c, _ := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{
			"header_banner": "https://example.com/asset",
		})
		if err == nil {
			t.Error("a header_<type> key with a type Meta does not accept must be reported, not sent")
		}
	})

	t.Run("rejects two header keys instead of picking one at random", func(t *testing.T) {
		c, _ := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{
			"header":       "Weekly survey",
			"header_image": "https://example.com/asset",
		})
		if err == nil {
			t.Error("two header keys are a configuration error: map iteration would otherwise decide which one wins")
		}
	})

	t.Run("no params sends template without components", func(t *testing.T) {
		c, captured := newCaptureClient(t)
		err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "en", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(captured.Template.Components) != 0 {
			t.Fatalf("expected no components, got %+v", captured.Template.Components)
		}
	})
}

func TestSendTemplateMessageTimeout(t *testing.T) {
	// Use an already-cancelled context to verify context propagation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	c := NewWhatsAppClient("fake-token", "fake-phone-id")
	err := c.SendTemplateMessage(ctx, "+391234567890", "test_template", "it", nil)
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}
