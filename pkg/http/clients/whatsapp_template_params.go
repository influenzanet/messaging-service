package clients

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalidTemplateParams is the sentinel every template-parameter validation error wraps, so
// a caller can test for it with errors.Is instead of matching error text.
var ErrInvalidTemplateParams = errors.New("invalid whatsapp template params")

// bodyParam is a BODY parameter: either named (name set, n unused) or positional (n set,
// name == "").
type bodyParam struct {
	name  string
	n     int
	value paramValue
}

// buttonParam is one "button_<i>[:subtype[:name]]" button, already shaped into its Meta
// parameters array; see whatsapp_template_buttons.go.
type buttonParam struct {
	index      string      // the original digit, for sorting and duplicate-index detection
	subType    string      // "url" (default), "quick_reply", ...
	indexValue interface{} // what Meta expects in "index": a string for url/quick_reply; some
	// other subtypes use a JSON integer instead.
	parameters []map[string]interface{}
}

// templateParams is a flat whatsappParams map, parsed and validated into its typed pieces.
// buildComponents turns it into Meta's "components" array; ValidateTemplateParams and
// SendTemplateMessage share this same parse step so they can never disagree.
type templateParams struct {
	templateName string
	// headerKey is the original key that set the header, kept only to name both keys in a
	// "two headers" error.
	headerKey          string
	textHeader         *textHeaderParam
	mediaHeader        *mediaHeaderParam
	locationHeader     map[string]interface{}
	productHeader      map[string]interface{}
	bodyParams         []bodyParam
	limitedTimeOfferMS *int64
	buttons            []buttonParam
	voiceCallParams    []map[string]interface{}

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
		case base == "header_location":
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setLocationHeader(key, value); err != nil {
				return nil, err
			}
		case base == "header_product":
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setProductHeader(key, value); err != nil {
				return nil, err
			}
		case strings.HasPrefix(base, "header_"):
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setMediaHeader(key, value); err != nil {
				return nil, err
			}
		case base == "limited_time_offer":
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setLimitedTimeOffer(key, value); err != nil {
				return nil, err
			}
		case base == "voice_call":
			if suffix != "" {
				return nil, newTemplateParamsError(templateName, key, "does not accept a value type suffix")
			}
			if err := tp.setVoiceCall(key, value); err != nil {
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

// setLimitedTimeOffer parses the "limited_time_offer" key: a positive integer, the expiration
// as a UNIX timestamp in milliseconds.
func (tp *templateParams) setLimitedTimeOffer(key, value string) error {
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ms <= 0 {
		return newTemplateParamsError(tp.templateName, key, "must be a positive integer (milliseconds)")
	}
	tp.limitedTimeOfferMS = &ms
	return nil
}

// setVoiceCall parses the "voice_call" key: its JSON value becomes the VOICE_CALL
// button's parameters, built last among the components (buildComponents).
func (tp *templateParams) setVoiceCall(key, value string) error {
	params, err := parseVoiceCallValue(value)
	if err != nil {
		return newTemplateParamsError(tp.templateName, key, "voice_call value: %v", err)
	}
	tp.voiceCallParams = params
	return nil
}

// buildComponents turns the parsed, validated params into Meta's "components" array, in the
// order header, body, limited_time_offer, carousel, buttons, voice_call.
func (tp *templateParams) buildComponents() []map[string]interface{} {
	var components []map[string]interface{}

	if header := tp.buildHeaderComponent(); header != nil {
		components = append(components, header)
	}
	if body := tp.buildBodyComponent(); body != nil {
		components = append(components, body)
	}
	if tp.limitedTimeOfferMS != nil {
		components = append(components, map[string]interface{}{
			"type": "limited_time_offer",
			"parameters": []map[string]interface{}{
				{"type": "limited_time_offer", "limited_time_offer": map[string]interface{}{
					"expiration_time_ms": *tp.limitedTimeOfferMS,
				}},
			},
		})
	}
	for _, b := range tp.buttons {
		button := map[string]interface{}{
			"type":     "button",
			"sub_type": b.subType,
			"index":    b.indexValue,
		}
		// A nil parameters list (an empty catalog thumbnail) means "send the
		// component with no parameters at all", not an empty array.
		if b.parameters != nil {
			button["parameters"] = b.parameters
		}
		components = append(components, button)
	}
	if tp.voiceCallParams != nil {
		components = append(components, map[string]interface{}{
			"type":       "button",
			"sub_type":   "voice_call",
			"parameters": tp.voiceCallParams,
		})
	}
	return components
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
