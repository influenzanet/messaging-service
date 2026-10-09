package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestHeaderProductPayload covers "header_product" (SPM): a PRODUCT header.
func TestHeaderProductPayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	params := map[string]string{"header_product": `{"product_retailer_id":"nqryix03ez","catalog_id":"194836987003835"}`}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"tpl","language":{"code":"it"},"components":[
		{"type":"header","parameters":[{"type":"product","product":{"product_retailer_id":"nqryix03ez","catalog_id":"194836987003835"}}]}]}}`)
}

func TestHeaderProductRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"not JSON":                  {"header_product": "not json"},
		"missing catalog_id":        {"header_product": `{"product_retailer_id":"a"}`},
		"empty product_retailer_id": {"header_product": `{"product_retailer_id":"","catalog_id":"a"}`},
		"unknown field":             {"header_product": `{"product_retailer_id":"a","catalog_id":"b","extra":1}`},
		"clash with text header":    {"header_product": `{"product_retailer_id":"a","catalog_id":"b"}`, "header": "x"},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, calls := newRawCaptureClient(t)
			if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err == nil {
				t.Fatal("expected an error")
			} else if !errors.Is(err, ErrInvalidTemplateParams) {
				t.Errorf("expected error to wrap ErrInvalidTemplateParams, got: %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("expected zero HTTP calls, got %d", n)
			}
		})
	}
}
