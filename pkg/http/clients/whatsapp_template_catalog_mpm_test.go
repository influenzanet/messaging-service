package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestButtonCatalogPayload covers "button_<i>:catalog": sub_type is exactly "CATALOG"
// (uppercase, per Meta's docs), index is a JSON integer, and an empty value omits "parameters"
// entirely so Meta falls back to the first product.
func TestButtonCatalogPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "with a thumbnail",
			params: map[string]string{"button_0:catalog": "2lc20305pt"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"CATALOG","index":0,"parameters":[{"type":"action","action":{"thumbnail_product_retailer_id":"2lc20305pt"}}]}]}}`,
		},
		{
			name:   "empty value sends no parameters",
			params: map[string]string{"button_0:catalog": ""},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"CATALOG","index":0}]}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, raw, calls := newRawCaptureClient(t)
			if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", tc.params); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 1 {
				t.Fatalf("expected 1 HTTP call, got %d", n)
			}
			assertPayloadJSON(t, *raw, tc.want)
		})
	}
}

// TestButtonMPMPayload covers "button_<i>:mpm": sub_type "mpm", index a JSON integer.
func TestButtonMPMPayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	params := map[string]string{
		"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[{"title":"T","product_items":[{"product_retailer_id":"a"}]}]}`,
	}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"tpl","language":{"code":"it"},"components":[
		{"type":"button","sub_type":"mpm","index":0,"parameters":[{"type":"action","action":{"thumbnail_product_retailer_id":"a","sections":[{"title":"T","product_items":[{"product_retailer_id":"a"}]}]}}]}]}}`)
}

func TestButtonCatalogMPMRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"catalog does not take a name":         {"button_0:catalog:extra": "x"},
		"mpm: not JSON":                        {"button_0:mpm": "not json"},
		"mpm: missing thumbnail":               {"button_0:mpm": `{"sections":[{"product_items":[{"product_retailer_id":"a"}]}]}`},
		"mpm: empty sections":                  {"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[]}`},
		"mpm: section without product_items":   {"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[{"title":"T"}]}`},
		"mpm: multiple sections require title": {"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[{"product_items":[{"product_retailer_id":"a"}]},{"title":"T","product_items":[{"product_retailer_id":"b"}]}]}`},
		"mpm: unknown field":                   {"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[{"product_items":[{"product_retailer_id":"a"}]}],"extra":1}`},
		"mpm: unknown product field":           {"button_0:mpm": `{"thumbnail_product_retailer_id":"a","sections":[{"product_items":[{"product_retailer_id":"a","sku":"x"}]}]}`},
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
