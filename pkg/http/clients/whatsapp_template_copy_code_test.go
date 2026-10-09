package clients

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestButtonCopyCodePayload covers "button_<i>:copy_code" sending a COPY_CODE button, with
// the index as a JSON integer, unlike url/quick_reply's JSON string.
func TestButtonCopyCodePayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	params := map[string]string{"button_0:copy_code": "CARIBE25"}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"tpl","language":{"code":"it"},"components":[
		{"type":"button","sub_type":"copy_code","index":0,"parameters":[{"type":"coupon_code","coupon_code":"CARIBE25"}]}]}}`)
}

func TestButtonCopyCodeRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"empty code":                     {"button_0:copy_code": ""},
		"code over 20 characters":        {"button_0:copy_code": strings.Repeat("x", 21)},
		"copy_code does not take a name": {"button_0:copy_code:extra": "CODE"},
		"duplicate index with url":       {"button_0": "x", "button_0:copy_code": "CODE"},
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
