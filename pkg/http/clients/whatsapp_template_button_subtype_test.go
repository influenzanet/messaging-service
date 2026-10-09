package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestButtonSubtypePayload covers the explicit "button_<i>:url" alias, "button_<i>:url:<name>"
// (a parameter_name on the URL parameter), and "button_<i>:quick_reply".
func TestButtonSubtypePayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "bare button_<i> still means url (unchanged)",
			params: map[string]string{"button_0": "verify/abc"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"verify/abc"}]}]}}`,
		},
		{
			name:   "explicit :url alias is identical to the bare key",
			params: map[string]string{"button_0:url": "verify/abc"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"verify/abc"}]}]}}`,
		},
		{
			name:   "url with a parameter_name",
			params: map[string]string{"button_0:url:token": "verify/abc"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"verify/abc","parameter_name":"token"}]}]}}`,
		},
		{
			name:   "quick_reply",
			params: map[string]string{"button_0:quick_reply": "stop"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"quick_reply","index":"0","parameters":[{"type":"payload","payload":"stop"}]}]}}`,
		},
		{
			name:   "quick_reply and url buttons at different indexes, sorted",
			params: map[string]string{"button_1": "verify/abc", "button_0:quick_reply": "stop"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"quick_reply","index":"0","parameters":[{"type":"payload","payload":"stop"}]},
				{"type":"button","sub_type":"url","index":"1","parameters":[{"type":"text","text":"verify/abc"}]}]}}`,
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

func TestButtonSubtypeRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown subtype":                            {"button_0:bogus": "x"},
		"quick_reply does not take a name":           {"button_0:quick_reply:extra": "x"},
		"duplicate index: bare and quick_reply":      {"button_0": "x", "button_0:quick_reply": "y"},
		"duplicate index: url alias and quick_reply": {"button_0:url": "x", "button_0:quick_reply": "y"},
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
