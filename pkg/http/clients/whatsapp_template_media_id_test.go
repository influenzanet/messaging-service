package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestMediaHeaderByIDPayload covers "header_<type>_id": a media header sent by an uploaded
// media handle instead of a public link.
func TestMediaHeaderByIDPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "image header by id",
			params: map[string]string{"header_image_id": "1602186516975000"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"image","image":{"id":"1602186516975000"}}]}]}}`,
		},
		{
			name:   "video header by id",
			params: map[string]string{"header_video_id": "1602186516975001"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"video","video":{"id":"1602186516975001"}}]}]}}`,
		},
		{
			name:   "document header by id with filename",
			params: map[string]string{"header_document_id": "1602186516975002", "header_document_filename": "info.pdf"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"document","document":{"id":"1602186516975002","filename":"info.pdf"}}]}]}}`,
		},
		{
			name:   "document header by id without filename",
			params: map[string]string{"header_document_id": "1602186516975002"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"header","parameters":[{"type":"document","document":{"id":"1602186516975002"}}]}]}}`,
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

func TestMediaHeaderByIDRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"empty image id":                         {"header_image_id": ""},
		"empty document id":                      {"header_document_id": ""},
		"id and link header clash":               {"header_image_id": "1", "header_image": "https://example.org/a.png"},
		"two id headers clash":                   {"header_image_id": "1", "header_video_id": "2"},
		"unknown _id suffix":                     {"header_banner_id": "1"},
		"filename with id header but wrong type": {"header_image_id": "1", "header_document_filename": "info.pdf"},
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
