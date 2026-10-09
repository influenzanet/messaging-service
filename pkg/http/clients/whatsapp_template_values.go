package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
