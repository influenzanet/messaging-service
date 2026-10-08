package clients

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// newRawCaptureClient records the raw request body and the number of HTTP calls.
func newRawCaptureClient(t *testing.T) (*WhatsAppClient, *[]byte, *int32) {
	t.Helper()
	var raw []byte
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	c := NewWhatsAppClient("test-token", "test-phone-id", "")
	c.apiBaseURL = server.URL
	return c, &raw, &calls
}

func assertPayloadJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("sent payload is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("payload mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

func TestSendTemplateMessageHeaderPayloads(t *testing.T) {
	const phone = "+391234567890"
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "no header keys keeps the body and button payload unchanged",
			params: map[string]string{"name": "Mario", "button_0": "verify/abc"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"Mario","parameter_name":"name"}]},
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"verify/abc"}]}]}}`,
		},
		{
			name:   "named text header, body and url button",
			params: map[string]string{"header:campaign": "Weekly survey", "name": "Mario", "button_0": "verify/abc"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"text","text":"Weekly survey","parameter_name":"campaign"}]},
				{"type":"body","parameters":[{"type":"text","text":"Mario","parameter_name":"name"}]},
				{"type":"button","sub_type":"url","index":"0","parameters":[{"type":"text","text":"verify/abc"}]}]}}`,
		},
		{
			name:   "positional text header",
			params: map[string]string{"header": "Weekly survey"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"text","text":"Weekly survey"}]}]}}`,
		},
		{
			name:   "image header",
			params: map[string]string{"header_image": "https://example.org/a.png", "name": "Mario"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"image","image":{"link":"https://example.org/a.png"}}]},
				{"type":"body","parameters":[{"type":"text","text":"Mario","parameter_name":"name"}]}]}}`,
		},
		{
			name:   "video header",
			params: map[string]string{"header_video": "https://example.org/a.mp4"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"video","video":{"link":"https://example.org/a.mp4"}}]}]}}`,
		},
		{
			name:   "document header with filename",
			params: map[string]string{"header_document": "https://example.org/a.pdf", "header_document_filename": "info.pdf"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"document","document":{"link":"https://example.org/a.pdf","filename":"info.pdf"}}]}]}}`,
		},
		{
			name:   "document header with an empty filename",
			params: map[string]string{"header_document": "https://example.org/a.pdf", "header_document_filename": ""},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"document","document":{"link":"https://example.org/a.pdf"}}]}]}}`,
		},
		{
			name:   "document header without filename",
			params: map[string]string{"header_document": "https://example.org/a.pdf"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"document","document":{"link":"https://example.org/a.pdf"}}]}]}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, raw, calls := newRawCaptureClient(t)
			if err := c.SendTemplateMessage(context.Background(), phone, "tpl", "it", tc.params); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if atomic.LoadInt32(calls) != 1 {
				t.Fatalf("expected 1 HTTP call, got %d", *calls)
			}
			assertPayloadJSON(t, *raw, tc.want)
		})
	}
}

func TestSendTemplateMessageHeaderRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"text and image header":           {"header": "x", "header_image": "https://example.org/a.png"},
		"named text and document header":  {"header:campaign": "x", "header_document": "https://example.org/a.pdf"},
		"image and video header":          {"header_image": "https://example.org/a.png", "header_video": "https://example.org/a.mp4"},
		"positional and named text":       {"header": "x", "header:campaign": "y"},
		"second positional header param":  {"header_1": "x"},
		"header_0 is not a header key":    {"header_0": "x"},
		"empty header name":               {"header:": "x"},
		"filename without document":       {"header_document_filename": "info.pdf"},
		"filename with image header":      {"header_image": "https://example.org/a.png", "header_document_filename": "info.pdf"},
		"filename with text header":       {"header": "x", "header_document_filename": "info.pdf"},
		"body and button next to a clash": {"name": "Mario", "button_0": "v", "header": "x", "header_video": "https://example.org/a.mp4"},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, calls := newRawCaptureClient(t)
			if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err == nil {
				t.Fatal("expected an error")
			}
			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("expected zero HTTP calls, got %d", n)
			}
		})
	}
}
