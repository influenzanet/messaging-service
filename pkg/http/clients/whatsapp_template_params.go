package clients

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ErrInvalidTemplateParams is the sentinel every template-parameter validation error wraps, so
// a caller can test for it with errors.Is instead of matching error text.
var ErrInvalidTemplateParams = errors.New("invalid whatsapp template params")

// headerMediaTypes are the media types Meta accepts in a template HEADER component sent by a
// public link. A "header_<type>" key with any other suffix is a configuration mistake, not a
// new media type.
var headerMediaTypes = map[string]bool{"image": true, "video": true, "document": true}

// buttonIndexPattern matches the single digit, 0-9, that Meta allows after "button_": a
// template has at most 10 buttons, numbered without leading zeros.
var buttonIndexPattern = regexp.MustCompile(`^[0-9]$`)

// textHeaderParam is a parsed text HEADER parameter; name is empty for a positional header.
type textHeaderParam struct {
	name  string
	value paramValue
}

// mediaHeaderParam is a parsed media HEADER parameter sent by public link.
type mediaHeaderParam struct {
	mediaType string
	link      string
	filename  string
}

// bodyParam is a BODY parameter: either named (name set, n unused) or positional (n set,
// name == "").
type bodyParam struct {
	name  string
	n     int
	value paramValue
}

// buttonParam is a URL button parameter at a given index.
type buttonParam struct {
	index string
	value string
}

// templateParams is a flat whatsappParams map, parsed and validated into its typed pieces.
// buildComponents turns it into Meta's "components" array; ValidateTemplateParams and
// SendTemplateMessage share this same parse step so they can never disagree.
type templateParams struct {
	templateName string
	// headerKey is the original key that set the header, kept only to name both keys in a
	// "two headers" error.
	headerKey   string
	textHeader  *textHeaderParam
	mediaHeader *mediaHeaderParam
	bodyParams  []bodyParam
	buttons     []buttonParam

	// sawNamedFormat/sawPositionalFormat track whether any textual parameter seen so far
	// (header text, body) is NAMED or POSITIONAL; a message setting both is rejected.
	sawNamedFormat      bool
	sawPositionalFormat bool
}

// newTemplateParamsError reports a configuration mistake by naming the key only: a value can
// be a participant's personal data or a secret and must never reach a log or an error message.
func newTemplateParamsError(templateName, key, format string, args ...interface{}) error {
	return fmt.Errorf("%w: template %s: key %q: %s", ErrInvalidTemplateParams, templateName, key, fmt.Sprintf(format, args...))
}

func newHeaderClashError(templateName, existingKey, newKey string) error {
	return fmt.Errorf("%w: template %s: both %q and %q set the header, a template has only one",
		ErrInvalidTemplateParams, templateName, existingKey, newKey)
}

// parseTemplateParams classifies every key of a flat whatsappParams map and validates it,
// without yet shaping Meta's JSON (that is buildComponents' job).
func parseTemplateParams(templateName string, params map[string]string) (*templateParams, error) {
	tp := &templateParams{templateName: templateName}

	documentFilename, hasDocumentFilename := params["header_document_filename"]

	for key, value := range params {
		// The optional "@<type>" suffix only makes sense on a key that produces a
		// textual parameter; base is the key with it stripped, used for classification.
		base, suffix := splitValueTypeSuffix(key)
		switch {
		case base == "header" || strings.HasPrefix(base, "header:"):
			if err := tp.setTextHeader(key, base, suffix, value); err != nil {
				return nil, err
			}
		case base == "header_document_filename":
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			// Applied below, once the header key is known.
		case strings.HasPrefix(base, "header_"):
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setMediaHeader(key, value); err != nil {
				return nil, err
			}
		case strings.HasPrefix(base, "button_"):
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.addButton(key, value); err != nil {
				return nil, err
			}
		case strings.HasPrefix(base, "body:"):
			name := strings.TrimPrefix(base, "body:")
			if err := tp.addNamedBody(key, name, suffix, value); err != nil {
				return nil, err
			}
		case positionalKeyPattern.MatchString(base):
			if err := tp.addPositionalBody(key, base, suffix, value); err != nil {
				return nil, err
			}
		default:
			if err := tp.addBareBody(key, base, suffix, value); err != nil {
				return nil, err
			}
		}
	}

	if hasDocumentFilename {
		if tp.mediaHeader == nil || tp.mediaHeader.mediaType != "document" {
			return nil, newTemplateParamsError(templateName, "header_document_filename", `needs a "header_document" key`)
		}
		// An empty name is left out: Meta would refuse the message for it.
		if documentFilename != "" {
			tp.mediaHeader.filename = documentFilename
		}
	}

	// Map iteration order is random; a deterministic payload needs a stable order for the
	// pieces that came from more than one key. Positional body params sort numerically and
	// drop parameter_name; named ones sort by name and keep it (buildBodyComponent).
	if tp.sawPositionalFormat {
		sort.Slice(tp.bodyParams, func(i, j int) bool { return tp.bodyParams[i].n < tp.bodyParams[j].n })
		if err := checkPositionalBodyContiguity(templateName, tp.bodyParams); err != nil {
			return nil, err
		}
	} else {
		sort.Slice(tp.bodyParams, func(i, j int) bool { return tp.bodyParams[i].name < tp.bodyParams[j].name })
	}
	sort.Slice(tp.buttons, func(i, j int) bool { return tp.buttons[i].index < tp.buttons[j].index })

	return tp, nil
}

func (tp *templateParams) setTextHeader(key, base, suffix, value string) error {
	if tp.headerKey != "" {
		return newHeaderClashError(tp.templateName, tp.headerKey, key)
	}
	name := ""
	if strings.HasPrefix(base, "header:") {
		name = strings.TrimPrefix(base, "header:")
		if !bodyNamePattern.MatchString(name) {
			return newTemplateParamsError(tp.templateName, key,
				"parameter name %q does not match the required pattern ^[a-z_][a-z0-9_]*$", name)
		}
		if err := tp.markFormat(key, true); err != nil {
			return err
		}
	} else if err := tp.markFormat(key, false); err != nil {
		return err
	}
	pv, err := parseParamValue(tp.templateName, key, suffix, value)
	if err != nil {
		return err
	}
	tp.textHeader = &textHeaderParam{name: name, value: pv}
	tp.headerKey = key
	return nil
}

func (tp *templateParams) setMediaHeader(key, value string) error {
	if tp.headerKey != "" {
		return newHeaderClashError(tp.templateName, tp.headerKey, key)
	}
	mediaType := strings.TrimPrefix(key, "header_")
	if !headerMediaTypes[mediaType] {
		return newTemplateParamsError(tp.templateName, key, "asks for header media type %q, which Meta does not accept", mediaType)
	}
	tp.mediaHeader = &mediaHeaderParam{mediaType: mediaType, link: value}
	tp.headerKey = key
	return nil
}

func (tp *templateParams) addButton(key, value string) error {
	index := strings.TrimPrefix(key, "button_")
	if !buttonIndexPattern.MatchString(index) {
		return newTemplateParamsError(tp.templateName, key, `is not a valid button key, expected "button_<0-9>"`)
	}
	tp.buttons = append(tp.buttons, buttonParam{index: index, value: value})
	return nil
}

// buildComponents turns the parsed, validated params into Meta's "components" array, in the
// order header, body, buttons.
func (tp *templateParams) buildComponents() []map[string]interface{} {
	var components []map[string]interface{}

	if header := tp.buildHeaderComponent(); header != nil {
		components = append(components, header)
	}
	if body := tp.buildBodyComponent(); body != nil {
		components = append(components, body)
	}
	for _, b := range tp.buttons {
		components = append(components, map[string]interface{}{
			"type":     "button",
			"sub_type": "url",
			"index":    b.index,
			"parameters": []map[string]interface{}{
				{"type": "text", "text": b.value},
			},
		})
	}
	return components
}

func (tp *templateParams) buildHeaderComponent() map[string]interface{} {
	switch {
	case tp.textHeader != nil:
		param := tp.textHeader.value.build()
		if tp.textHeader.name != "" {
			param["parameter_name"] = tp.textHeader.name
		}
		return map[string]interface{}{"type": "header", "parameters": []map[string]interface{}{param}}
	case tp.mediaHeader != nil:
		media := map[string]interface{}{"link": tp.mediaHeader.link}
		if tp.mediaHeader.filename != "" {
			media["filename"] = tp.mediaHeader.filename
		}
		return map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": tp.mediaHeader.mediaType, tp.mediaHeader.mediaType: media},
			},
		}
	default:
		return nil
	}
}

func (tp *templateParams) buildBodyComponent() map[string]interface{} {
	if len(tp.bodyParams) == 0 {
		return nil
	}
	params := make([]map[string]interface{}, 0, len(tp.bodyParams))
	for _, bp := range tp.bodyParams {
		param := bp.value.build()
		if bp.name != "" {
			param["parameter_name"] = bp.name
		}
		params = append(params, param)
	}
	return map[string]interface{}{"type": "body", "parameters": params}
}

// ValidateTemplateParams builds a template's components without sending anything, so that a
// caller can reject bad configuration before any attempt reaches Meta.
func ValidateTemplateParams(templateName string, params map[string]string) error {
	_, err := parseTemplateParams(templateName, params)
	return err
}
