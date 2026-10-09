package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestHeaderLocationPayload covers "header_location" sending a LOCATION header, with
// latitude/longitude always as strings even when given as JSON numbers.
func TestHeaderLocationPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "numeric latitude/longitude",
			params: map[string]string{"header_location": `{"latitude":45.07,"longitude":7.69,"name":"Sede","address":"Via Roma 1, Torino"}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"location","location":{"latitude":"45.07","longitude":"7.69","name":"Sede","address":"Via Roma 1, Torino"}}]}]}}`,
		},
		{
			name:   "string latitude/longitude, no name or address",
			params: map[string]string{"header_location": `{"latitude":"45.07","longitude":"7.69"}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"location","location":{"latitude":"45.07","longitude":"7.69"}}]}]}}`,
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

func TestHeaderLocationRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"not JSON":                    {"header_location": "not json"},
		"missing longitude":           {"header_location": `{"latitude":45.07}`},
		"unknown field":               {"header_location": `{"latitude":45.07,"longitude":7.69,"extra":1}`},
		"latitude out of range":       {"header_location": `{"latitude":145.07,"longitude":7.69}`},
		"longitude out of range":      {"header_location": `{"latitude":45.07,"longitude":280}`},
		"latitude not numeric string": {"header_location": `{"latitude":"north","longitude":7.69}`},
		"name not a string":           {"header_location": `{"latitude":45.07,"longitude":7.69,"name":1}`},
		"name null":                   {"header_location": `{"latitude":45.07,"longitude":7.69,"name":null}`},
		"address null":                {"header_location": `{"latitude":45.07,"longitude":7.69,"address":null}`},
		"latitude null":               {"header_location": `{"latitude":null,"longitude":7.69}`},
		"clash with text header":      {"header_location": `{"latitude":45.07,"longitude":7.69}`, "header": "x"},
		"clash with media header":     {"header_location": `{"latitude":45.07,"longitude":7.69}`, "header_image": "https://example.org/a.png"},
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
