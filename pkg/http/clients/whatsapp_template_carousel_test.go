package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestCarouselPayload covers "card_<c>.<key>": a carousel component
// with 2-10 cards, card_index as a JSON integer, each card built in order header, body,
// buttons, and the whole carousel placed after body/limited_time_offer at the top level.
func TestCarouselPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			// This worked example (the "=" literal-value prefix is a
			// bulk_messages-layer concern; the client only ever sees resolved values).
			name: "image header cards with quick_reply and url buttons",
			params: map[string]string{
				"1":                           "nome",
				"card_0.header_image_id":      "111",
				"card_0.button_0:quick_reply": "more",
				"card_0.button_1":             "a",
				"card_1.header_image_id":      "222",
				"card_1.button_0:quick_reply": "more",
				"card_1.button_1":             "b",
			},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"nome"}]},
				{"type":"carousel","cards":[
				  {"card_index":0,"components":[
				    {"type":"header","parameters":[{"type":"image","image":{"id":"111"}}]},
				    {"type":"button","sub_type":"quick_reply","index":"0","parameters":[{"type":"payload","payload":"more"}]},
				    {"type":"button","sub_type":"url","index":"1","parameters":[{"type":"text","text":"a"}]}]},
				  {"card_index":1,"components":[
				    {"type":"header","parameters":[{"type":"image","image":{"id":"222"}}]},
				    {"type":"button","sub_type":"quick_reply","index":"0","parameters":[{"type":"payload","payload":"more"}]},
				    {"type":"button","sub_type":"url","index":"1","parameters":[{"type":"text","text":"b"}]}]}]}]}}`,
		},
		{
			name: "header_product cards (SPM-style), no top-level body",
			params: map[string]string{
				"card_0.header_product": `{"product_retailer_id":"p1","catalog_id":"c1"}`,
				"card_1.header_product": `{"product_retailer_id":"p2","catalog_id":"c2"}`,
			},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"carousel","cards":[
				  {"card_index":0,"components":[
				    {"type":"header","parameters":[{"type":"product","product":{"product_retailer_id":"p1","catalog_id":"c1"}}]}]},
				  {"card_index":1,"components":[
				    {"type":"header","parameters":[{"type":"product","product":{"product_retailer_id":"p2","catalog_id":"c2"}}]}]}]}]}}`,
		},
		{
			// Positional body inside each card has its own 1..N numbering, separate from
			// the top-level body's; all-positional here (top level and every card), so the
			// global NAMED/POSITIONAL coherence check succeeds.
			name: "positional body inside each card, alongside a positional top-level body",
			params: map[string]string{
				"1":                      "z",
				"card_0.1":               "x",
				"card_0.header_image_id": "111",
				"card_1.1":               "y",
				"card_1.header_image_id": "222",
			},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"body","parameters":[{"type":"text","text":"z"}]},
				{"type":"carousel","cards":[
				  {"card_index":0,"components":[
				    {"type":"header","parameters":[{"type":"image","image":{"id":"111"}}]},
				    {"type":"body","parameters":[{"type":"text","text":"x"}]}]},
				  {"card_index":1,"components":[
				    {"type":"header","parameters":[{"type":"image","image":{"id":"222"}}]},
				    {"type":"body","parameters":[{"type":"text","text":"y"}]}]}]}]}}`,
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

// TestCarouselRejectedBeforeHTTP covers the carousel validation rules: card
// count and contiguity, exactly one header per card, identical key sets across cards, the
// restricted set of inner keys, no nesting, and participation in the global NAMED/POSITIONAL
// and per-body-positional-contiguity rules.
//
// Note: an "more than 10 cards" case is not included - the card index <c> is a single digit
// 0-9 by key grammar ("<c> = card index 0-9"), so an 11th card has no key that
// could ever express it; the 2-10 upper bound is unreachable through this API's key syntax.
func TestCarouselRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"only 1 card": {
			"card_0.header_image_id": "1",
			"card_0.button_0":        "a",
		},
		"non-contiguous card indexes (0 and 2, missing 1)": {
			"card_0.header_image_id": "1",
			"card_0.button_0":        "a",
			"card_2.header_image_id": "2",
			"card_2.button_0":        "b",
		},
		"a card with no header at all (only a body param)": {
			"card_0.1": "x",
			"card_1.1": "y",
		},
		"two headers in the same card": {
			"card_0.header_image_id": "1",
			"card_0.header_video_id": "1",
			"card_1.header_image_id": "2",
			"card_1.header_video_id": "2",
		},
		"cards with different key sets": {
			"card_0.header_image_id": "1",
			"card_0.button_0":        "a",
			"card_1.header_image_id": "2",
		},
		"a text header inside a card": {
			"card_0.header": "x",
			"card_1.header": "y",
		},
		"a document header inside a card": {
			"card_0.header_document": "https://example.org/a.pdf",
			"card_1.header_document": "https://example.org/b.pdf",
		},
		"a location header inside a card": {
			"card_0.header_location": `{"latitude":1,"longitude":1}`,
			"card_1.header_location": `{"latitude":2,"longitude":2}`,
		},
		"limited_time_offer inside a card": {
			"card_0.limited_time_offer": "123",
			"card_1.limited_time_offer": "456",
		},
		"voice_call inside a card": {
			"card_0.voice_call": `{"payload":"x"}`,
			"card_1.voice_call": `{"payload":"y"}`,
		},
		"nested card key": {
			"card_0.card_1.header_image_id": "1",
			"card_1.header_image_id":        "2",
		},
		"malformed card index (not a single digit)": {
			"card_a.header_image_id": "1",
			"card_1.header_image_id": "2",
		},
		"malformed card key (missing the dot)": {
			"card_0header_image_id":  "1",
			"card_1.header_image_id": "2",
		},
		"mixing a NAMED top-level body with a POSITIONAL card body": {
			"name":                   "x",
			"card_0.1":               "a",
			"card_0.header_image_id": "1",
			"card_1.1":               "b",
			"card_1.header_image_id": "2",
		},
		"non-contiguous positional body within one card": {
			"card_0.1":               "x",
			"card_0.3":               "y",
			"card_0.header_image_id": "1",
			"card_1.1":               "p",
			"card_1.3":               "q",
			"card_1.header_image_id": "2",
		},
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
