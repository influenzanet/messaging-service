package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestBodyPositionalPayload covers positional body keys "1".."N" being sent in numeric
// order, contiguous from 1, without parameter_name.
func TestBodyPositionalPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "single positional parameter",
			params: map[string]string{"1": "123456"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"123456"}]}]}}`,
		},
		{
			name:   "two positional parameters sent in numeric order",
			params: map[string]string{"2": "second", "1": "first"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]}}`,
		},
		{
			name:   "OTP shape: positional body plus url button carrying the same code",
			params: map[string]string{"1": "123456", "button_0": "123456"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"123456"}]},
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"123456"}]}]}}`,
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

// TestBodyNamedViaPrefixPayload covers the "body:<name>" escape, which lets a body parameter
// be named like a reserved key (header, button_, card_, voice_call, limited_time_offer).
func TestBodyNamedViaPrefixPayload(t *testing.T) {
	c, raw, calls := newRawCaptureClient(t)
	params := map[string]string{"body:header": "not the header"}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", n)
	}
	assertPayloadJSON(t, *raw, `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
		"name":"tpl","language":{"code":"it"},"components":[
		{"type":"body","parameters":[{"type":"text","text":"not the header","parameter_name":"header"}]}]}}`)
}

// TestTemplateParamsRejectedBeforeHTTP covers param validation: grammar, reserved names and
// NAMED/POSITIONAL coherence, all caught before any request to Meta.
func TestTemplateParamsRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"key outside the grammar (uppercase)":         {"Name": "x"},
		"key outside the grammar (hyphen)":            {"first-name": "x"},
		"key outside the grammar (space)":             {"first name": "x"},
		"positional key with leading zero":            {"01": "x"},
		"positional key zero":                         {"0": "x"},
		"body: with empty name":                       {"body:": "x"},
		"reserved bare name: header prefix":           {"headertext": "x"},
		"reserved bare name: button_ prefix":          {"button_extra": "x"},
		"reserved bare name: card_ prefix":            {"card_0.name": "x"},
		"reserved bare name: voice_call":              {"voice_call": "x"},
		"reserved bare name: limited_time_offer":      {"limited_time_offer": "x"},
		"mixed format: positional header, named body": {"header": "x", "name": "y"},
		"mixed format: named header, positional body": {"header:campaign": "x", "1": "y"},
		"mixed format: positional and named body":     {"1": "x", "name": "y"},
		"non-contiguous positional body (missing 2)":  {"1": "x", "3": "y"},
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
