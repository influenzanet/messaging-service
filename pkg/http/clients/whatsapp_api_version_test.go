package clients

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// urlCapturingTransport answers every request with a success body and records the URL, so a
// test can see the full Graph URL without pointing the client at a local server.
type urlCapturingTransport struct{ url string }

func (t *urlCapturingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.url = r.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.test"}]}`)),
		Request:    r,
	}, nil
}

func TestSendTemplateMessageCallsTheConfiguredVersion(t *testing.T) {
	c := NewWhatsAppClient("token", "phone-id", "v25.0")
	transport := &urlCapturingTransport{}
	c.httpClient.Transport = transport
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "weekly", "it", map[string]string{}); err != nil {
		t.Fatalf("SendTemplateMessage: %v", err)
	}
	if transport.url != "https://graph.facebook.com/v25.0/phone-id/messages" {
		t.Fatalf("request URL = %q, want the configured version in the Graph URL", transport.url)
	}
}

func TestResolveAPIVersion(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"unset uses the default", "", "v26.0"},
		{"well-formed value is used as is", "v25.0", "v25.0"},
		{"missing v prefix falls back to the default", "26.0", "v26.0"},
		{"missing minor falls back to the default", "v26", "v26.0"},
		{"upper-case prefix falls back to the default", "V26.0", "v26.0"},
		{"leading space falls back to the default", " v26.0", "v26.0"},
		{"trailing slash falls back to the default", "v26.0/", "v26.0"},
		{"free text falls back to the default", "latest", "v26.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAPIVersion(tc.configured); got != tc.want {
				t.Fatalf("ResolveAPIVersion(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestNewWhatsAppClientBuildsBaseURLFromAPIVersion(t *testing.T) {
	c := NewWhatsAppClient("token", "phone-id", "v25.0")
	if c.apiBaseURL != "https://graph.facebook.com/v25.0" {
		t.Fatalf("apiBaseURL = %q, want the configured version", c.apiBaseURL)
	}
	c = NewWhatsAppClient("token", "phone-id", "")
	if c.apiBaseURL != "https://graph.facebook.com/v26.0" {
		t.Fatalf("apiBaseURL = %q, want the default version", c.apiBaseURL)
	}
}
