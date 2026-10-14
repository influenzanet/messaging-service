package clients

import (
	"fmt"
	"strings"
)

// headerMediaTypes are the media types Meta accepts in a template HEADER component sent by a
// public link. A "header_<type>" key with any other suffix is a configuration mistake, not a
// new media type.
var headerMediaTypes = map[string]bool{"image": true, "video": true, "document": true}

// textHeaderParam is a parsed text HEADER parameter; name is empty for a positional header.
type textHeaderParam struct {
	name  string
	value paramValue
}

// mediaHeaderParam is a parsed media HEADER parameter, sent either by public link or by an
// uploaded media id ("header_<type>_id").
type mediaHeaderParam struct {
	mediaType string
	value     string // the link, or the id when byID is true
	byID      bool
	filename  string
}

func newHeaderClashError(templateName, existingKey, newKey string) error {
	return fmt.Errorf("%w: template %s: both %q and %q set the header, a template has only one",
		ErrInvalidTemplateParams, templateName, existingKey, newKey)
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
		if err := checkNamedValueType(tp.templateName, key, suffix); err != nil {
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
	mediaType, byID, ok := parseMediaHeaderKey(key)
	if !ok {
		return newTemplateParamsError(tp.templateName, key, "asks for header media type %q, which Meta does not accept", mediaType)
	}
	if byID && value == "" {
		return newTemplateParamsError(tp.templateName, key, "must not be empty")
	}
	tp.mediaHeader = &mediaHeaderParam{mediaType: mediaType, value: value, byID: byID}
	tp.headerKey = key
	return nil
}

// setLocationHeader parses "header_location": its JSON value becomes the LOCATION
// header's single "location" parameter.
func (tp *templateParams) setLocationHeader(key, value string) error {
	if tp.headerKey != "" {
		return newHeaderClashError(tp.templateName, tp.headerKey, key)
	}
	loc, err := parseLocationValue(value)
	if err != nil {
		return newTemplateParamsError(tp.templateName, key, "location value: %v", err)
	}
	tp.locationHeader = loc
	tp.headerKey = key
	return nil
}

// setProductHeader parses "header_product" (SPM): its JSON value becomes the PRODUCT
// header's single "product" parameter.
func (tp *templateParams) setProductHeader(key, value string) error {
	if tp.headerKey != "" {
		return newHeaderClashError(tp.templateName, tp.headerKey, key)
	}
	product, err := parseProductValue(value)
	if err != nil {
		return newTemplateParamsError(tp.templateName, key, "product value: %v", err)
	}
	tp.productHeader = product
	tp.headerKey = key
	return nil
}

// parseMediaHeaderKey splits a "header_<type>" or "header_<type>_id" key into its media type
// and whether it is the by-id form; ok is false when the type is not one Meta accepts.
func parseMediaHeaderKey(key string) (mediaType string, byID bool, ok bool) {
	rest := strings.TrimPrefix(key, "header_")
	if trimmed := strings.TrimSuffix(rest, "_id"); trimmed != rest {
		if headerMediaTypes[trimmed] {
			return trimmed, true, true
		}
		return rest, true, false
	}
	if headerMediaTypes[rest] {
		return rest, false, true
	}
	return rest, false, false
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
		media := map[string]interface{}{}
		if tp.mediaHeader.byID {
			media["id"] = tp.mediaHeader.value
		} else {
			media["link"] = tp.mediaHeader.value
		}
		if tp.mediaHeader.filename != "" {
			media["filename"] = tp.mediaHeader.filename
		}
		return map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": tp.mediaHeader.mediaType, tp.mediaHeader.mediaType: media},
			},
		}
	case tp.locationHeader != nil:
		return map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": "location", "location": tp.locationHeader},
			},
		}
	case tp.productHeader != nil:
		return map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": "product", "product": tp.productHeader},
			},
		}
	default:
		return nil
	}
}
