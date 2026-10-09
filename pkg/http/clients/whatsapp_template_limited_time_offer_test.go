package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestLimitedTimeOfferPayload covers "limited_time_offer": a
// standalone component between body and buttons carrying expiration_time_ms.
func TestLimitedTimeOfferPayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	params := map[string]string{
		"header_image_id":    "1602186516975000",
		"limited_time_offer": "1698562800000",
		"button_0:copy_code": "CARIBE25",
		"button_1":           "n3mtql",
	}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"tpl","language":{"code":"it"},"components":[
		{"type":"header","parameters":[{"type":"image","image":{"id":"1602186516975000"}}]},
		{"type":"limited_time_offer","parameters":[{"type":"limited_time_offer","limited_time_offer":{"expiration_time_ms":1698562800000}}]},
		{"type":"button","sub_type":"copy_code","index":0,"parameters":[{"type":"coupon_code","coupon_code":"CARIBE25"}]},
		{"type":"button","sub_type":"url","index":"1","parameters":[{"type":"text","text":"n3mtql"}]}]}}`)
}

func TestLimitedTimeOfferRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"not an integer":                {"limited_time_offer": "soon"},
		"zero":                          {"limited_time_offer": "0"},
		"negative":                      {"limited_time_offer": "-5"},
		"value type suffix not allowed": {"limited_time_offer@text": "5"},
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
