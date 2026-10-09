package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// splitValueTypeSuffix splits a key on its last "@", the separator for the optional value type
// suffix: "total@currency" -> ("total", "currency"); a key without "@" has no suffix.
func splitValueTypeSuffix(key string) (base, suffix string) {
	idx := strings.LastIndex(key, "@")
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}

// paramValue is one Meta "parameters[]" entry for a textual slot (body or header text),
// before parameter_name is attached (buildHeaderComponent/buildBodyComponent do that).
type paramValue struct {
	kind string // "text" (default), "currency", "date_time"
	text string
	obj  map[string]interface{}
}

func (p paramValue) build() map[string]interface{} {
	switch p.kind {
	case "currency":
		return map[string]interface{}{"type": "currency", "currency": p.obj}
	case "date_time":
		return map[string]interface{}{"type": "date_time", "date_time": p.obj}
	default:
		return map[string]interface{}{"type": "text", "text": p.text}
	}
}

// parseParamValue applies the optional "@<type>" suffix to a resolved value: "" and
// "text" mean plain text (unchanged behaviour), "currency" and "date_time" parse the value as
// a strict JSON object. Errors name the key only; the value can carry a participant's amount.
func parseParamValue(templateName, key, suffix, value string) (paramValue, error) {
	switch suffix {
	case "", "text":
		return paramValue{kind: "text", text: value}, nil
	case "currency":
		obj, err := parseCurrencyValue(value)
		if err != nil {
			return paramValue{}, newTemplateParamsError(templateName, key, "currency value: %v", err)
		}
		return paramValue{kind: "currency", obj: obj}, nil
	case "date_time":
		if value == "" {
			return paramValue{}, newTemplateParamsError(templateName, key, "date_time value must not be empty")
		}
		return paramValue{kind: "date_time", obj: map[string]interface{}{"fallback_value": value}}, nil
	default:
		return paramValue{}, newTemplateParamsError(templateName, key, "has an unknown value type suffix %q", suffix)
	}
}

var currencyCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)

// parseCurrencyValue decodes a strict "@currency" JSON object: exactly fallback_value (a
// non-empty string), code (ISO 4217, ^[A-Z]{3}$) and amount_1000 (a JSON integer, the amount
// times 1000). Error messages never echo the input.
func parseCurrencyValue(raw string) (map[string]interface{}, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("is not a JSON object")
	}
	for name := range fields {
		if name != "fallback_value" && name != "code" && name != "amount_1000" {
			return nil, fmt.Errorf("has an unknown field %q", name)
		}
	}
	fallback, err := decodeNonEmptyJSONString(fields["fallback_value"])
	if err != nil {
		return nil, fmt.Errorf("fallback_value %v", err)
	}
	code, err := decodeNonEmptyJSONString(fields["code"])
	if err != nil {
		return nil, fmt.Errorf("code %v", err)
	}
	if !currencyCodePattern.MatchString(code) {
		return nil, errors.New("code must match ^[A-Z]{3}$")
	}
	amount, err := decodeJSONInteger(fields["amount_1000"])
	if err != nil {
		return nil, fmt.Errorf("amount_1000 %v", err)
	}
	return map[string]interface{}{"fallback_value": fallback, "code": code, "amount_1000": amount}, nil
}

// decodeNonEmptyJSONString requires a present, non-empty JSON string field.
func decodeNonEmptyJSONString(raw json.RawMessage) (string, error) {
	if raw == nil {
		return "", errors.New("is required")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errors.New("must be a string")
	}
	if s == "" {
		return "", errors.New("must not be empty")
	}
	return s, nil
}

// decodeJSONInteger requires a present JSON field holding an integer literal: no decimal
// point, no exponent, no surrounding quotes.
func decodeJSONInteger(raw json.RawMessage) (int64, error) {
	if raw == nil {
		return 0, errors.New("is required")
	}
	s := strings.TrimSpace(string(raw))
	if !isJSONIntegerLiteral(s) {
		return 0, errors.New("must be a JSON integer")
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, errors.New("must be a JSON integer")
	}
	return n, nil
}

// parseLocationValue decodes a strict "header_location" JSON object: required latitude
// ([-90, 90]) and longitude ([-180, 180]), each a JSON number or a numeric string and always
// sent as a string; optional name and address strings; no other field.
func parseLocationValue(raw string) (map[string]interface{}, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("is not a JSON object")
	}
	for name := range fields {
		switch name {
		case "latitude", "longitude", "name", "address":
		default:
			return nil, fmt.Errorf("has an unknown field %q", name)
		}
	}
	lat, err := decodeCoordinate(fields["latitude"], -90, 90)
	if err != nil {
		return nil, fmt.Errorf("latitude %v", err)
	}
	lon, err := decodeCoordinate(fields["longitude"], -180, 180)
	if err != nil {
		return nil, fmt.Errorf("longitude %v", err)
	}
	result := map[string]interface{}{"latitude": lat, "longitude": lon}
	name, present, err := decodeOptionalJSONString(fields["name"])
	if err != nil {
		return nil, fmt.Errorf("name %v", err)
	} else if present {
		result["name"] = name
	}
	address, present, err := decodeOptionalJSONString(fields["address"])
	if err != nil {
		return nil, fmt.Errorf("address %v", err)
	} else if present {
		result["address"] = address
	}
	return result, nil
}

// decodeCoordinate reads a JSON number or a numeric string within [min, max] and returns it as
// a string, the form Meta's LOCATION header expects.
func decodeCoordinate(raw json.RawMessage, min, max float64) (string, error) {
	if raw == nil {
		return "", errors.New("is required")
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", errors.New("must be a number or a numeric string")
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", errors.New("must be a number or a numeric string")
		}
		if f < min || f > max {
			return "", fmt.Errorf("must be between %v and %v", min, max)
		}
		return s, nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return "", errors.New("must be a number or a numeric string")
	}
	f, err := num.Float64()
	if err != nil {
		return "", errors.New("must be a number or a numeric string")
	}
	if f < min || f > max {
		return "", fmt.Errorf("must be between %v and %v", min, max)
	}
	return num.String(), nil
}

// isJSONNull reports whether a field holds the JSON literal null, which json.Unmarshal turns
// into the zero value of a string or a map without any error.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// decodeOptionalJSONString reads an optional JSON string field: present=false when the field
// is absent, an error when it is present but not a string (null included).
func decodeOptionalJSONString(raw json.RawMessage) (value string, present bool, err error) {
	if raw == nil {
		return "", false, nil
	}
	if isJSONNull(raw) {
		return "", true, errors.New("must be a string")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, errors.New("must be a string")
	}
	return value, true, nil
}

// parseFlowValue decodes a "button_<i>:flow" value (per Meta's flow-sending docs): plain text (not
// starting with "{") is the flow_token outright; a JSON object may carry flow_token (a string)
// and/or flow_action_data (an object), both optional. An empty value yields neither field,
// relying on Meta's own default ("unused").
func parseFlowValue(raw string) (map[string]interface{}, error) {
	action := map[string]interface{}{}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if raw != "" {
			action["flow_token"] = raw
		}
		return action, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("is not a valid JSON object")
	}
	for name := range fields {
		if name != "flow_token" && name != "flow_action_data" {
			return nil, fmt.Errorf("has an unknown field %q", name)
		}
	}
	if raw, ok := fields["flow_token"]; ok {
		var token string
		if isJSONNull(raw) || json.Unmarshal(raw, &token) != nil {
			return nil, errors.New("flow_token must be a string")
		}
		action["flow_token"] = token
	}
	if raw, ok := fields["flow_action_data"]; ok {
		var data map[string]json.RawMessage
		if isJSONNull(raw) || json.Unmarshal(raw, &data) != nil {
			return nil, errors.New("flow_action_data must be a JSON object")
		}
		// Sent as written: decoding into interface{} would turn large integers into floats.
		action["flow_action_data"] = raw
	}
	return action, nil
}

// parseProductValue decodes a "header_product" JSON object (per Meta's SPM template docs): exactly
// product_retailer_id and catalog_id, both non-empty strings.
func parseProductValue(raw string) (map[string]interface{}, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("is not a JSON object")
	}
	for name := range fields {
		if name != "product_retailer_id" && name != "catalog_id" {
			return nil, fmt.Errorf("has an unknown field %q", name)
		}
	}
	productID, err := decodeNonEmptyJSONString(fields["product_retailer_id"])
	if err != nil {
		return nil, fmt.Errorf("product_retailer_id %v", err)
	}
	catalogID, err := decodeNonEmptyJSONString(fields["catalog_id"])
	if err != nil {
		return nil, fmt.Errorf("catalog_id %v", err)
	}
	return map[string]interface{}{"product_retailer_id": productID, "catalog_id": catalogID}, nil
}

// decodeJSONArray requires a present field holding a JSON array.
func decodeJSONArray(raw json.RawMessage, fieldName string) ([]json.RawMessage, error) {
	if raw == nil {
		return nil, fmt.Errorf("%s is required", fieldName)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array", fieldName)
	}
	return items, nil
}

func isJSONIntegerLiteral(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' {
		i = 1
	}
	if i >= len(s) {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
