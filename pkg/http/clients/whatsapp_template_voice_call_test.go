package clients

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestVoiceCallPayload covers "voice_call": a VOICE_CALL button with no index, parameters
// ttl_minutes then payload, each only if present.
func TestVoiceCallPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "both fields",
			params: map[string]string{"voice_call": `{"ttl_minutes":100,"payload":"payload data"}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"voice_call","parameters":[{"type":"ttl_minutes","ttl_minutes":100},{"type":"payload","payload":"payload data"}]}]}}`,
		},
		{
			name:   "ttl_minutes only",
			params: map[string]string{"voice_call": `{"ttl_minutes":60}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"voice_call","parameters":[{"type":"ttl_minutes","ttl_minutes":60}]}]}}`,
		},
		{
			name:   "payload only",
			params: map[string]string{"voice_call": `{"payload":"hello"}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"voice_call","parameters":[{"type":"payload","payload":"hello"}]}]}}`,
		},
		{
			name:   "voice_call is last, after a button",
			params: map[string]string{"button_0": "x", "voice_call": `{"ttl_minutes":60}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"x"}]},
				{"type":"button","sub_type":"voice_call","parameters":[{"type":"ttl_minutes","ttl_minutes":60}]}]}}`,
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

func TestVoiceCallRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"not JSON":                      {"voice_call": "not json"},
		"neither field present":         {"voice_call": `{}`},
		"unknown field":                 {"voice_call": `{"ttl_minutes":60,"extra":1}`},
		"ttl_minutes too low":           {"voice_call": `{"ttl_minutes":0}`},
		"ttl_minutes too high":          {"voice_call": `{"ttl_minutes":43201}`},
		"payload too long":              {"voice_call": `{"payload":"` + strings.Repeat("x", 513) + `"}`},
		"payload null":                  {"voice_call": `{"payload":null}`},
		"ttl_minutes null":              {"voice_call": `{"ttl_minutes":null}`},
		"value type suffix not allowed": {"voice_call@text": `{"ttl_minutes":60}`},
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
