package clients

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// buttonKeyPattern matches what follows "button_": a single digit 0-9 (a template has at most
// 10 buttons, numbered without leading zeros), then an optional ":<subtype>" and, for subtypes
// that take one, an optional ":<name>".
var buttonKeyPattern = regexp.MustCompile(`^([0-9])(?::([a-z_]+)(?::([a-z_][a-z0-9_]*))?)?$`)

// parseButtonKey splits a "button_<i>[:subtype[:name]]" key. A bare index means subtype "url"
// (today's only form); ok is false when the key does not match this grammar at all - the
// subtype itself is validated by the caller, since which subtypes exist grows commit by commit.
func parseButtonKey(key string) (index, subType, name string, ok bool) {
	m := buttonKeyPattern.FindStringSubmatch(strings.TrimPrefix(key, "button_"))
	if m == nil {
		return "", "", "", false
	}
	subType = m[2]
	if subType == "" {
		subType = "url"
	}
	return m[1], subType, m[3], true
}

// addButton parses one "button_<i>[:subtype[:name]]" key and appends it to tp.buttons.
// Two keys on the same index (for example "button_0" and "button_0:quick_reply") are a
// configuration error: Meta allows only one component per button index.
func (tp *templateParams) addButton(key, value string) error {
	index, subType, name, ok := parseButtonKey(key)
	if !ok {
		return newTemplateParamsError(tp.templateName, key,
			`is not a valid button key, expected "button_<0-9>[:<subtype>[:<name>]]"`)
	}
	for _, b := range tp.buttons {
		if b.index == index {
			return newTemplateParamsError(tp.templateName, key, "duplicate button index %q", index)
		}
	}

	switch subType {
	case "url":
		param := map[string]interface{}{"type": "text", "text": value}
		if name != "" {
			param["parameter_name"] = name
		}
		tp.buttons = append(tp.buttons, buttonParam{
			index: index, subType: "url", indexValue: index,
			parameters: []map[string]interface{}{param},
		})
	case "quick_reply":
		if name != "" {
			return newTemplateParamsError(tp.templateName, key, "subtype %q does not take a parameter name", subType)
		}
		tp.buttons = append(tp.buttons, buttonParam{
			index: index, subType: "quick_reply", indexValue: index,
			parameters: []map[string]interface{}{{"type": "payload", "payload": value}},
		})
	case "copy_code":
		if name != "" {
			return newTemplateParamsError(tp.templateName, key, "subtype %q does not take a parameter name", subType)
		}
		if n := utf8.RuneCountInString(value); n < 1 || n > 20 {
			return newTemplateParamsError(tp.templateName, key, "copy_code value must be 1-20 characters")
		}
		// Per Meta's docs, copy_code sends its index as a JSON integer, unlike
		// url/quick_reply's JSON string; the regex guarantees index is a single digit.
		idx, _ := strconv.Atoi(index)
		tp.buttons = append(tp.buttons, buttonParam{
			index: index, subType: "copy_code", indexValue: idx,
			parameters: []map[string]interface{}{{"type": "coupon_code", "coupon_code": value}},
		})
	case "flow":
		if name != "" {
			return newTemplateParamsError(tp.templateName, key, "subtype %q does not take a parameter name", subType)
		}
		action, err := parseFlowValue(value)
		if err != nil {
			return newTemplateParamsError(tp.templateName, key, "flow value: %v", err)
		}
		tp.buttons = append(tp.buttons, buttonParam{
			index: index, subType: "flow", indexValue: index,
			parameters: []map[string]interface{}{{"type": "action", "action": action}},
		})
	case "catalog":
		if name != "" {
			return newTemplateParamsError(tp.templateName, key, "subtype %q does not take a parameter name", subType)
		}
		idx, _ := strconv.Atoi(index)
		button := buttonParam{index: index, subType: "CATALOG", indexValue: idx}
		// Per Meta's catalog template docs, an empty thumbnail sends the component without
		// "parameters" at all, so
		// Meta falls back to the catalog's first product.
		if value != "" {
			button.parameters = []map[string]interface{}{{"type": "action", "action": map[string]interface{}{
				"thumbnail_product_retailer_id": value,
			}}}
		}
		tp.buttons = append(tp.buttons, button)
	case "mpm":
		if name != "" {
			return newTemplateParamsError(tp.templateName, key, "subtype %q does not take a parameter name", subType)
		}
		action, err := parseMPMValue(value)
		if err != nil {
			return newTemplateParamsError(tp.templateName, key, "mpm value: %v", err)
		}
		idx, _ := strconv.Atoi(index)
		tp.buttons = append(tp.buttons, buttonParam{
			index: index, subType: "mpm", indexValue: idx,
			parameters: []map[string]interface{}{{"type": "action", "action": action}},
		})
	default:
		return newTemplateParamsError(tp.templateName, key, "has an unknown button subtype %q", subType)
	}
	return nil
}
