package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestValueTypeSuffixPayload covers "@currency" and "@date_time" on a body or header text
// key produce Meta's typed parameter object instead of a plain text one. "@text" is the
// explicit, redundant spelling of the default.
func TestValueTypeSuffixPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			// Named body parameters are matched by parameter_name; currency is accepted by Meta in a
			// template with named parameters (verified with a real send on the test number).
			name: "currency on a named body param",
			params: map[string]string{
				"total@currency": `{"fallback_value":"€10,50","code":"EUR","amount_1000":10500}`,
				"due":            "31 ottobre",
			},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[
				  {"type":"text","text":"31 ottobre","parameter_name":"due"},
				  {"type":"currency","currency":{"fallback_value":"€10,50","code":"EUR","amount_1000":10500},"parameter_name":"total"}]}]}}`,
		},
		{
			// Meta accepts date_time only in a template with positional parameters (a real send with a
			// named date_time parameter is refused with error 100).
			name: "date_time on a positional body param",
			params: map[string]string{
				"1":           "settimanale",
				"2@date_time": "31 ottobre",
			},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[
				  {"type":"text","text":"settimanale"},
				  {"type":"date_time","date_time":{"fallback_value":"31 ottobre"}}]}]}}`,
		},
		{
			name:   "explicit @text suffix behaves like the default",
			params: map[string]string{"name@text": "Mario"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"Mario","parameter_name":"name"}]}]}}`,
		},
		{
			name:   "currency suffix on a positional body key",
			params: map[string]string{"1@currency": `{"fallback_value":"10","code":"EUR","amount_1000":1000}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"currency","currency":{"fallback_value":"10","code":"EUR","amount_1000":1000}}]}]}}`,
		},
		{
			name:   "date_time suffix on a positional header",
			params: map[string]string{"header@date_time": "31 ottobre"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"date_time","date_time":{"fallback_value":"31 ottobre"}}]}]}}`,
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

// TestValueTypeSuffixRejectedBeforeHTTP covers the validation side of the value-type suffix.
func TestValueTypeSuffixRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"currency: not JSON":                 {"total@currency": "not json"},
		"currency: missing code":             {"total@currency": `{"fallback_value":"x","amount_1000":10}`},
		"currency: unknown field":            {"total@currency": `{"fallback_value":"x","code":"EUR","amount_1000":10,"extra":1}`},
		"currency: empty fallback_value":     {"total@currency": `{"fallback_value":"","code":"EUR","amount_1000":10}`},
		"currency: lowercase code":           {"total@currency": `{"fallback_value":"x","code":"eur","amount_1000":10}`},
		"currency: amount as string":         {"total@currency": `{"fallback_value":"x","code":"EUR","amount_1000":"10"}`},
		"currency: amount as float":          {"total@currency": `{"fallback_value":"x","code":"EUR","amount_1000":10.5}`},
		"date_time: empty value":             {"1@date_time": ""},
		"date_time: named body param":        {"due@date_time": "31 ottobre"},
		"date_time: body:<name> param":       {"body:due@date_time": "31 ottobre"},
		"date_time: named header":            {"header:due@date_time": "31 ottobre"},
		"unknown suffix":                     {"name@unknown": "x"},
		"suffix on media header":             {"header_image@currency": "https://example.org/a.png"},
		"suffix on button":                   {"button_0@currency": "x"},
		"suffix on header_document_filename": {"header_document": "https://example.org/a.pdf", "header_document_filename@text": "info.pdf"},
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
