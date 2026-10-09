package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// parseMPMValue decodes a "button_<i>:mpm" JSON object (per Meta's mpm template docs): a non-empty
// thumbnail_product_retailer_id and 1-10 sections, each with at least one product_item and a
// title (required once there is more than one section, <= 24 characters); at most 30 products
// across all sections.
func parseMPMValue(raw string) (map[string]interface{}, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, errors.New("is not a JSON object")
	}
	for name := range fields {
		if name != "thumbnail_product_retailer_id" && name != "sections" {
			return nil, fmt.Errorf("has an unknown field %q", name)
		}
	}
	thumbnail, err := decodeNonEmptyJSONString(fields["thumbnail_product_retailer_id"])
	if err != nil {
		return nil, fmt.Errorf("thumbnail_product_retailer_id %v", err)
	}
	rawSections, err := decodeJSONArray(fields["sections"], "sections")
	if err != nil {
		return nil, err
	}
	if len(rawSections) < 1 || len(rawSections) > 10 {
		return nil, errors.New("sections must have 1-10 entries")
	}
	sections := make([]map[string]interface{}, 0, len(rawSections))
	totalProducts := 0
	for i, rs := range rawSections {
		section, productCount, err := parseMPMSection(rs, len(rawSections) > 1)
		if err != nil {
			return nil, fmt.Errorf("sections[%d] %v", i, err)
		}
		totalProducts += productCount
		sections = append(sections, section)
	}
	if totalProducts > 30 {
		return nil, errors.New("sections must have 30 products or fewer in total")
	}
	return map[string]interface{}{"thumbnail_product_retailer_id": thumbnail, "sections": sections}, nil
}

func parseMPMSection(raw json.RawMessage, titleRequired bool) (map[string]interface{}, int, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, 0, errors.New("is not a JSON object")
	}
	for name := range fields {
		if name != "title" && name != "product_items" {
			return nil, 0, fmt.Errorf("has an unknown field %q", name)
		}
	}
	section := map[string]interface{}{}
	if titleRaw, hasTitle := fields["title"]; hasTitle {
		title, err := decodeNonEmptyJSONString(titleRaw)
		if err != nil {
			return nil, 0, fmt.Errorf("title %v", err)
		}
		if utf8.RuneCountInString(title) > 24 {
			return nil, 0, errors.New("title must be 24 characters or fewer")
		}
		section["title"] = title
	} else if titleRequired {
		return nil, 0, errors.New("title is required when there is more than one section")
	}
	rawItems, err := decodeJSONArray(fields["product_items"], "product_items")
	if err != nil {
		return nil, 0, err
	}
	if len(rawItems) < 1 {
		return nil, 0, errors.New("product_items must have at least 1 entry")
	}
	items := make([]map[string]interface{}, 0, len(rawItems))
	for i, ri := range rawItems {
		id, err := parseMPMProductItem(ri)
		if err != nil {
			return nil, 0, fmt.Errorf("product_items[%d] %v", i, err)
		}
		items = append(items, map[string]interface{}{"product_retailer_id": id})
	}
	section["product_items"] = items
	return section, len(items), nil
}

func parseMPMProductItem(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", errors.New("is not a JSON object")
	}
	for name := range fields {
		if name != "product_retailer_id" {
			return "", fmt.Errorf("has an unknown field %q", name)
		}
	}
	id, err := decodeNonEmptyJSONString(fields["product_retailer_id"])
	if err != nil {
		return "", fmt.Errorf("product_retailer_id %v", err)
	}
	return id, nil
}
