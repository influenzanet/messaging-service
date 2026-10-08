package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"absent", "", 0},
		{"blank", "   ", 0},
		{"seconds", "120", 120 * time.Second},
		{"seconds with spaces", " 30 ", 30 * time.Second},
		{"zero", "0", 0},
		{"negative seconds", "-5", 0},
		{"garbage", "soon", 0},
		{"fractional seconds are not valid", "1.5", 0},
		{"huge seconds are cut", "86400", maxRetryAfter},
		{"seconds beyond int64 are cut", "99999999999999999999999", maxRetryAfter},
		{"negative beyond int64 is zero", "-99999999999999999999999", 0},
		{"http date in the future", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"http date in the past", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"http date far away is cut", now.Add(48 * time.Hour).Format(http.TimeFormat), maxRetryAfter},
		{"malformed date", "Thu, 99 Foo 2026 00:00:00 GMT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.value, now); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestSendTemplateMessageKeepsRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		status int
		want   time.Duration
	}{
		{"seconds on 429", "45", http.StatusTooManyRequests, 45 * time.Second},
		{"seconds on a Meta throttle code", "10", http.StatusBadRequest, 10 * time.Second},
		{"no header", "", http.StatusTooManyRequests, 0},
		{"invalid header", "later", http.StatusTooManyRequests, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("Retry-After", tt.header)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":{"code":130429,"message":"free-form text that must not be kept"}}`))
			}))
			defer server.Close()
			c := NewWhatsAppClient("test-token", "test-phone-id", "")
			c.apiBaseURL = server.URL

			err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", nil)
			sendErr, ok := err.(*WhatsAppSendError)
			if !ok {
				t.Fatalf("expected a WhatsAppSendError, got %T: %v", err, err)
			}
			if sendErr.RetryAfter != tt.want {
				t.Errorf("RetryAfter = %v, want %v", sendErr.RetryAfter, tt.want)
			}
			if sendErr.Class() != WhatsAppErrorThrottled {
				t.Errorf("class = %v, want rate limit", sendErr.Class())
			}
		})
	}
}

func TestSendTemplateMessageRetryAfterAsHTTPDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(2*time.Minute).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	c := NewWhatsAppClient("test-token", "test-phone-id", "")
	c.apiBaseURL = server.URL

	err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", nil)
	sendErr, ok := err.(*WhatsAppSendError)
	if !ok {
		t.Fatalf("expected a WhatsAppSendError, got %T: %v", err, err)
	}
	if sendErr.RetryAfter < 100*time.Second || sendErr.RetryAfter > 2*time.Minute {
		t.Errorf("RetryAfter = %v, want about 2m", sendErr.RetryAfter)
	}
}
