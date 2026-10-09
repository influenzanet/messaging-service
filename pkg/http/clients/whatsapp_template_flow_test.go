package clients

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestButtonFlowPayload covers "button_<i>:flow": plain text is the flow_token, a JSON object
// can also carry flow_action_data; the index is a JSON string, per Meta's flow-sending docs.
func TestButtonFlowPayload(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "plain text value is the flow_token",
			params: map[string]string{"button_0:flow": "tok"},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"flow","index":"0","parameters":[{"type":"action","action":{"flow_token":"tok"}}]}]}}`,
		},
		{
			name:   "JSON object with flow_token and flow_action_data",
			params: map[string]string{"button_0:flow": `{"flow_token":"tok","flow_action_data":{"screen":"A"}}`},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"flow","index":"0","parameters":[{"type":"action","action":{"flow_token":"tok","flow_action_data":{"screen":"A"}}}]}]}}`,
		},
		{
			name:   "empty value omits flow_token, relying on Meta's default",
			params: map[string]string{"button_0:flow": ""},
			want: `{"messaging_product":"whatsapp","to":"+391234567890","type":"template","template":{
				"name":"tpl","language":{"code":"it"},"components":[
				{"type":"button","sub_type":"flow","index":"0","parameters":[{"type":"action","action":{}}]}]}}`,
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

func TestButtonFlowRejectedBeforeHTTP(t *testing.T) {
	cases := map[string]map[string]string{
		"malformed JSON (starts with brace)": {"button_0:flow": "{not json"},
		"unknown field":                      {"button_0:flow": `{"flow_token":"x","extra":1}`},
		"flow_token not a string":            {"button_0:flow": `{"flow_token":1}`},
		"flow_action_data not an object":     {"button_0:flow": `{"flow_action_data":"x"}`},
		"flow_token null":                    {"button_0:flow": `{"flow_token":null}`},
		"flow_action_data null":              {"button_0:flow": `{"flow_action_data":null}`},
		"flow does not take a name":          {"button_0:flow:extra": "tok"},
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

// TestButtonFlowActionDataSentVerbatim guards the integers of flow_action_data: decoding them
// into float64 would send 12345678901234567890 as 12345678901234567000.
func TestButtonFlowActionDataSentVerbatim(t *testing.T) {
	c, raw, _ := newRawCaptureClient(t)
	params := map[string]string{"button_0:flow": `{"flow_action_data":{"order_id":12345678901234567890}}`}
	if err := c.SendTemplateMessage(context.Background(), "+391234567890", "tpl", "it", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(*raw), `"order_id":12345678901234567890`) {
		t.Errorf("flow_action_data was not sent verbatim: %s", *raw)
	}
}
