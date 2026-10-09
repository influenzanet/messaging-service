package clients

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// cardKeyPattern matches "card_<c>.<key>": <c> is a single digit 0-9 (a
// carousel has at most 10 cards), and the rest of the string, after the first dot, is the
// card's own inner key - which can itself contain dots or "@" (an inner "@<type>" suffix is
// split later, by the same parser that handles top-level keys).
var cardKeyPattern = regexp.MustCompile(`^card_([0-9])\.(.+)$`)

// buildCarouselCards validates and builds the "carousel" component's "cards" array from the
// raw per-card key/value maps collected while parsing the flat whatsappParams map. parent
// supplies the template name and the shared NAMED/POSITIONAL format flags (the coherence
// rule spans the top-level body/header and every card's body together).
func buildCarouselCards(parent *templateParams, raw map[int]map[string]string) ([]map[string]interface{}, error) {
	n := len(raw)
	if n < 2 || n > 10 {
		return nil, fmt.Errorf("%w: template %s: a carousel must have 2-10 cards, got %d",
			ErrInvalidTemplateParams, parent.templateName, n)
	}
	for i := 0; i < n; i++ {
		if _, ok := raw[i]; !ok {
			return nil, fmt.Errorf("%w: template %s: carousel card indexes must be contiguous starting at 0 (card %d is missing)",
				ErrInvalidTemplateParams, parent.templateName, i)
		}
	}

	var referenceKeys map[string]bool
	cards := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		keys := innerKeySet(raw[i])
		if referenceKeys == nil {
			referenceKeys = keys
		} else if !sameKeySet(referenceKeys, keys) {
			return nil, fmt.Errorf("%w: template %s: carousel card %d does not have the same set of keys as card 0 (Meta requires identical cards)",
				ErrInvalidTemplateParams, parent.templateName, i)
		}

		card, err := parseCardParams(parent, i, raw[i])
		if err != nil {
			return nil, err
		}
		if card.headerKey == "" {
			return nil, fmt.Errorf("%w: template %s: carousel card %d has no header",
				ErrInvalidTemplateParams, parent.templateName, i)
		}
		cards = append(cards, map[string]interface{}{
			"card_index": i,
			"components": buildCardComponents(card),
		})
	}
	return cards, nil
}

// innerKeySet returns the set of keys of a card's raw param map, for the "all cards have the
// same keys" check (values are irrelevant to it).
func innerKeySet(params map[string]string) map[string]bool {
	keys := make(map[string]bool, len(params))
	for k := range params {
		keys[k] = true
	}
	return keys
}

func sameKeySet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// parseCardParams parses one carousel card's inner keys into a *templateParams, reusing the
// same header/body/button parsers as the top level, but restricted to what a card supports
// to media or product header only (no text, document or location header), no
// template-level keys (limited_time_offer, voice_call), no nesting, and url/quick_reply
// buttons only. It shares the parent's NAMED/POSITIONAL flags so the coherence rule spans the
// whole message.
func parseCardParams(parent *templateParams, cardIndex int, inner map[string]string) (*templateParams, error) {
	card := &templateParams{
		templateName:        parent.templateName,
		sawNamedFormat:      parent.sawNamedFormat,
		sawPositionalFormat: parent.sawPositionalFormat,
	}

	for key, value := range inner {
		base, suffix := splitValueTypeSuffix(key)
		switch {
		case base == "header" || strings.HasPrefix(base, "header:"):
			return nil, newCardKeyError(parent.templateName, cardIndex, key, "a text header is not allowed inside a carousel card")
		case base == "header_location":
			return nil, newCardKeyError(parent.templateName, cardIndex, key, "a location header is not allowed inside a carousel card")
		case base == "header_document" || base == "header_document_id" || base == "header_document_filename":
			return nil, newCardKeyError(parent.templateName, cardIndex, key, "a document header is not allowed inside a carousel card")
		case base == "limited_time_offer" || base == "voice_call":
			return nil, newCardKeyError(parent.templateName, cardIndex, key, "is not allowed inside a carousel card")
		case base == "header_product":
			if suffix != "" {
				return nil, newCardSuffixError(parent.templateName, cardIndex, key)
			}
			if err := card.setProductHeader(key, value); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		case strings.HasPrefix(base, "header_"):
			if suffix != "" {
				return nil, newCardSuffixError(parent.templateName, cardIndex, key)
			}
			if err := card.setMediaHeader(key, value); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		case strings.HasPrefix(base, "button_"):
			if suffix != "" {
				return nil, newCardSuffixError(parent.templateName, cardIndex, key)
			}
			if err := card.addButton(key, value, true); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		case strings.HasPrefix(base, "body:"):
			name := strings.TrimPrefix(base, "body:")
			if err := card.addNamedBody(key, name, suffix, value); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		case positionalKeyPattern.MatchString(base):
			if err := card.addPositionalBody(key, base, suffix, value); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		default:
			if err := card.addBareBody(key, base, suffix, value); err != nil {
				return nil, wrapCardError(cardIndex, err)
			}
		}
	}

	if *card.sawPositionalFormat {
		sort.Slice(card.bodyParams, func(i, j int) bool { return card.bodyParams[i].n < card.bodyParams[j].n })
		if err := checkPositionalBodyContiguity(parent.templateName, card.bodyParams); err != nil {
			return nil, wrapCardError(cardIndex, err)
		}
	} else {
		sort.Slice(card.bodyParams, func(i, j int) bool { return card.bodyParams[i].name < card.bodyParams[j].name })
	}
	sort.Slice(card.buttons, func(i, j int) bool { return card.buttons[i].index < card.buttons[j].index })

	return card, nil
}

// buildCardComponents builds one card's "components" array, in order header, body, buttons -
// a card has no limited_time_offer, carousel or voice_call of its own.
func buildCardComponents(card *templateParams) []map[string]interface{} {
	var components []map[string]interface{}
	if header := card.buildHeaderComponent(); header != nil {
		components = append(components, header)
	}
	if body := card.buildBodyComponent(); body != nil {
		components = append(components, body)
	}
	components = append(components, buildButtonComponents(card.buttons)...)
	return components
}

func wrapCardError(cardIndex int, err error) error {
	return fmt.Errorf("carousel card %d: %w", cardIndex, err)
}

func newCardKeyError(templateName string, cardIndex int, key, message string) error {
	return wrapCardError(cardIndex, newTemplateParamsError(templateName, key, message))
}

func newCardSuffixError(templateName string, cardIndex int, key string) error {
	return wrapCardError(cardIndex, newTemplateParamsError(templateName, key, "does not accept a value type suffix"))
}
