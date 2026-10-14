package clients

import (
	"regexp"
	"strconv"
	"strings"
)

// bodyNamePattern is the Meta parameter name grammar: lowercase letters,
// digits and underscores, starting with a letter or underscore.
var bodyNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// positionalKeyPattern matches a positional body key: an integer >= 1, no leading zeros.
var positionalKeyPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// reservedBodyPrefixes are the key prefixes a bare body parameter name must not start with,
// because they already mean something else (a header, a button, a carousel card). A body
// parameter that genuinely needs one of these names must use "body:<name>" instead.
var reservedBodyPrefixes = []string{"header", "button_", "card_"}

// reservedBodyNames are the exact bare body keys reserved for template-level components that
// are not nested under a component prefix.
var reservedBodyNames = map[string]bool{"voice_call": true, "limited_time_offer": true}

func isReservedBodyKey(key string) bool {
	if reservedBodyNames[key] {
		return true
	}
	for _, prefix := range reservedBodyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// markFormat records whether a textual parameter is NAMED or POSITIONAL and rejects a message
// that mixes both: Meta's parameter_format is a single, per-template setting.
// sawNamedFormat/sawPositionalFormat are pointers so that every carousel card can share the
// same two flags as the top-level template: the coherence rule spans the whole message, not
// just one component (this includes each carousel card's own body).
func (tp *templateParams) markFormat(key string, named bool) error {
	if named {
		if *tp.sawPositionalFormat {
			return newFormatMixError(tp.templateName, key)
		}
		*tp.sawNamedFormat = true
	} else {
		if *tp.sawNamedFormat {
			return newFormatMixError(tp.templateName, key)
		}
		*tp.sawPositionalFormat = true
	}
	return nil
}

func newFormatMixError(templateName, key string) error {
	return newTemplateParamsError(templateName, key,
		"mixes a positional parameter with a named one; a template is NAMED or POSITIONAL, never both")
}

// addPositionalBody adds a body parameter keyed by its 1-based position. base is the
// numeric key (suffix already stripped); key is the original, for error messages. Contiguity
// (1..N, no gaps) is checked once all keys are known, in checkPositionalBodyContiguity.
func (tp *templateParams) addPositionalBody(key, base, suffix, value string) error {
	if err := tp.markFormat(key, false); err != nil {
		return err
	}
	n, err := strconv.Atoi(base)
	if err != nil {
		// positionalKeyPattern already guarantees this parses; defensive only.
		return newTemplateParamsError(tp.templateName, key, "is not a valid positional index")
	}
	pv, err := parseParamValue(tp.templateName, key, suffix, value)
	if err != nil {
		return err
	}
	tp.bodyParams = append(tp.bodyParams, bodyParam{n: n, value: pv})
	return nil
}

// addNamedBody adds a body parameter named explicitly: originalKey is the whatsappParams key
// (for error messages), name is the parameter name Meta will see, suffix its optional value
// type.
func (tp *templateParams) addNamedBody(originalKey, name, suffix, value string) error {
	if !bodyNamePattern.MatchString(name) {
		return newTemplateParamsError(tp.templateName, originalKey,
			"parameter name %q does not match the required pattern ^[a-z_][a-z0-9_]*$", name)
	}
	if err := tp.markFormat(originalKey, true); err != nil {
		return err
	}
	if err := checkNamedValueType(tp.templateName, originalKey, suffix); err != nil {
		return err
	}
	pv, err := parseParamValue(tp.templateName, originalKey, suffix, value)
	if err != nil {
		return err
	}
	tp.bodyParams = append(tp.bodyParams, bodyParam{name: name, value: pv})
	return nil
}

// addBareBody handles a key that matched none of the other prefixes: either a reserved name
// that must be spelled "body:<name>", or a plain named body parameter. base/suffix are the key
// with its optional "@<type>" value-type suffix already split off.
func (tp *templateParams) addBareBody(key, base, suffix, value string) error {
	if isReservedBodyKey(base) {
		return newTemplateParamsError(tp.templateName, key, `is reserved, write it as "body:%s"`, base)
	}
	return tp.addNamedBody(key, base, suffix, value)
}

// checkPositionalBodyContiguity requires the positional body keys to be exactly 1..N, with no
// gaps and no duplicates. It assumes tp.bodyParams holds only positional
// entries (name == ""), which the caller guarantees by format.
func checkPositionalBodyContiguity(templateName string, bodyParams []bodyParam) error {
	for i, bp := range bodyParams {
		if bp.n != i+1 {
			return newTemplateParamsError(templateName, strconv.Itoa(bp.n),
				"positional body parameters must be contiguous, starting at 1")
		}
	}
	return nil
}
