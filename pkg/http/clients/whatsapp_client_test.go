package clients

import (
	"context"
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
