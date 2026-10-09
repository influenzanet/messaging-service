package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestButtonIndexValidation covers a button key carrying an index of exactly one digit,
// 0-9, with no leading zero, right after "button_". Anything else is a configuration mistake
// that must be caught before any request to Meta, not forwarded to it as a stray body parameter.
func TestButtonIndexValidation(t *testing.T) {
	validCases := map[string]string{
		"button_0": "0",
		"button_9": "9",
	}
	for key := range validCases {
		t.Run(key+" is accepted", func(t *testing.T) {
			c, _, calls := newRawCaptureClient(t)
			err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{key: "v"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 1 {
				t.Errorf("expected 1 HTTP call, got %d", n)
			}
		})
	}

	invalidKeys := []string{
		"button_x",
		"button_0_payload",
		"button_10",
		"button_-1",
		"button_01",
	}
	for _, key := range invalidKeys {
		t.Run(key+" is rejected before any HTTP call", func(t *testing.T) {
			c, _, calls := newRawCaptureClient(t)
			err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", map[string]string{key: "v"})
			if err == nil {
				t.Fatalf("expected an error for key %q", key)
			}
			if !errors.Is(err, ErrInvalidTemplateParams) {
				t.Errorf("expected error to wrap ErrInvalidTemplateParams, got: %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("expected zero HTTP calls, got %d", n)
			}
		})
	}
}

// TestValidateTemplateParams checks the exported validator used at queue time: it must agree
// with SendTemplateMessage without making any request.
func TestValidateTemplateParams(t *testing.T) {
	t.Run("valid params produce no error", func(t *testing.T) {
		err := ValidateTemplateParams("tpl", map[string]string{"name": "Mario", "button_0": "v"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid button index is reported", func(t *testing.T) {
		err := ValidateTemplateParams("tpl", map[string]string{"button_10": "v"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, ErrInvalidTemplateParams) {
			t.Errorf("expected error to wrap ErrInvalidTemplateParams, got: %v", err)
		}
	})
}
