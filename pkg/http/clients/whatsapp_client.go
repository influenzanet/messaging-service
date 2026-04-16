package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coneno/logger"
)

const (
	whatsAppHTTPTimeout = 30 * time.Second
	whatsAppAPIBaseURL  = "https://graph.facebook.com/v19.0"
)

// WhatsAppClient handles direct communication with the WhatsApp Business API
// for bulk message delivery from the message-scheduler.
type WhatsAppClient struct {
	httpClient    *http.Client
	apiToken      string
	phoneNumberID string
}

// NewWhatsAppClient creates a new client instance. Returns nil if token or phoneID are empty.
func NewWhatsAppClient(token, phoneID string) *WhatsAppClient {
	if token == "" || phoneID == "" {
		return nil
	}
	return &WhatsAppClient{
		httpClient:    &http.Client{Timeout: whatsAppHTTPTimeout},
		apiToken:      token,
		phoneNumberID: phoneID,
	}
}

// mapLanguageCode converts system language codes to Meta WhatsApp API codes.
// Currently identity (en→en, it→it) because both systems use ISO 639-1.
// Extend this map if a language requires a different code on Meta's side (e.g. "pt"→"pt_BR").
func mapLanguageCode(lang string) string {
	langMap := map[string]string{
		"en": "en",
		"it": "it",
	}
	if mapped, ok := langMap[lang]; ok {
		return mapped
	}
	return lang
}

func maskPhone(phone string) string {
	if len(phone) <= 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-4:]
}

// SendTemplateMessage sends a message using a specific WhatsApp template with named parameters.
func (c *WhatsAppClient) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	apiURL := fmt.Sprintf("%s/%s/messages", whatsAppAPIBaseURL, c.phoneNumberID)

	whatsappLangCode := mapLanguageCode(lang)

	template := map[string]interface{}{
		"name": templateName,
		"language": map[string]string{
			"code": whatsappLangCode,
		},
	}

	if len(params) > 0 {
		var bodyParams []map[string]interface{}
		var components []map[string]interface{}

		for key, value := range params {
			if strings.HasPrefix(key, "button_") {
				btnIndex := strings.TrimPrefix(key, "button_")
				components = append(components, map[string]interface{}{
					"type":     "button",
					"sub_type": "url",
					"index":    btnIndex,
					"parameters": []map[string]interface{}{
						{"type": "text", "text": value},
					},
				})
			} else {
				bodyParams = append(bodyParams, map[string]interface{}{
					"type":           "text",
					"text":           value,
					"parameter_name": key,
				})
			}
		}

		if len(bodyParams) > 0 {
			components = append(components, map[string]interface{}{
				"type":       "body",
				"parameters": bodyParams,
			})
		}
		if len(components) > 0 {
			template["components"] = components
		}
	}

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "template",
		"template":          template,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendTemplateMessage -> to:%s template:%s lang:%s", maskPhone(toPhoneNumber), templateName, lang)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendTemplateMessage failed status=%d resp=%v", resp.StatusCode, respObj)
		return fmt.Errorf("failed to send template message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendTemplateMessage: delivered to API")

	return nil
}
