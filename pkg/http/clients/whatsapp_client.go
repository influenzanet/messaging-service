package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coneno/logger"
)

const (
	// WhatsAppHTTPTimeout bounds one call to Meta. The message-scheduler reads it to know how
	// long a send can run, so that its claim window and this timeout cannot drift apart.
	WhatsAppHTTPTimeout = 30 * time.Second
	whatsAppGraphHost   = "https://graph.facebook.com"
	// Graph API version used when WHATSAPP_API_VERSION is not set or not well formed.
	// Meta retires each version about two years after release and silently serves a
	// retired version with the oldest one still available, so keep this current:
	// https://developers.facebook.com/docs/graph-api/changelog (v26.0: released 2026-07-29).
	whatsAppDefaultAPIVersion = "v26.0"
	// maxRetryAfter bounds the Retry-After a response can ask for, so that a bogus header
	// cannot park the sender for days.
	maxRetryAfter = time.Hour
)

var whatsAppAPIVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)

// ResolveAPIVersion returns the Graph API version to call: the configured value when it looks
// like "v26.0", otherwise the default. A malformed value is logged and ignored rather than
// refused, so a typo in the environment cannot stop the service.
func ResolveAPIVersion(configured string) string {
	if configured == "" {
		logger.Info.Printf("WHATSAPP_API_VERSION not set, using %s", whatsAppDefaultAPIVersion)
		return whatsAppDefaultAPIVersion
	}
	if !whatsAppAPIVersionPattern.MatchString(configured) {
		logger.Error.Printf("invalid WHATSAPP_API_VERSION %q, using %s", configured, whatsAppDefaultAPIVersion)
		return whatsAppDefaultAPIVersion
	}
	return configured
}

// parseRetryAfter reads a Retry-After header, either as a number of seconds or as an HTTP
// date measured from now. A missing, malformed or past value is 0, and a value above
// maxRetryAfter is cut to it.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var wait time.Duration
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		wait = time.Duration(seconds) * time.Second
	} else if errors.Is(err, strconv.ErrRange) && value[0] != '-' {
		return maxRetryAfter
	} else if date, dateErr := http.ParseTime(value); dateErr == nil {
		wait = date.Sub(now)
	}
	if wait <= 0 {
		return 0
	}
	if wait > maxRetryAfter {
		return maxRetryAfter
	}
	return wait
}

// WhatsAppClient handles direct communication with the WhatsApp Business API
// for bulk message delivery from the message-scheduler.
type WhatsAppClient struct {
	httpClient    *http.Client
	apiToken      string
	phoneNumberID string
	apiBaseURL    string
}

// NewWhatsAppClient creates a new client instance. Returns nil if token or phoneID are empty.
// apiVersion is the value of WHATSAPP_API_VERSION; see ResolveAPIVersion for how it is read.
func NewWhatsAppClient(token, phoneID, apiVersion string) *WhatsAppClient {
	if token == "" || phoneID == "" {
		return nil
	}
	version := ResolveAPIVersion(apiVersion)
	logger.Info.Printf("WhatsApp Graph API version: %s", version)
	return &WhatsAppClient{
		httpClient:    &http.Client{Timeout: WhatsAppHTTPTimeout},
		apiToken:      token,
		phoneNumberID: phoneID,
		apiBaseURL:    whatsAppGraphHost + "/" + version,
	}
}

func maskPhone(phone string) string {
	if len(phone) <= 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-4:]
}

// headerMediaTypes are the media types Meta accepts in a template HEADER component. A
// "header_<type>" key with any other suffix is a configuration mistake, not a new media type.
var headerMediaTypes = map[string]bool{"image": true, "video": true, "document": true}

// SendTemplateMessage sends a message using a specific WhatsApp template with named parameters.
// Param key conventions:
//   - "header"            -> text HEADER parameter, positional (templates built with {{1}})
//   - "header:<name>"     -> text HEADER parameter named <name> (templates built with {{name}});
//     Meta requires every parameter of a template to be named or positional consistently, and
//     BODY parameters are always sent named, so a text header on such a template needs its name
//   - "header_image", "header_video", "header_document" -> media HEADER component (value is the asset link)
//   - "header_document_filename" -> optional file name shown for a "header_document" asset
//   - "button_<index>"    -> URL button parameter at the given index
//   - anything else       -> named BODY parameter
//
// A template has at most one header: two header keys are rejected rather than resolved by map
// iteration order, which would silently pick a different one on every send.
func (c *WhatsAppClient) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	apiURL := fmt.Sprintf("%s/%s/messages", c.apiBaseURL, c.phoneNumberID)

	template := map[string]interface{}{
		"name": templateName,
		"language": map[string]string{
			"code": lang,
		},
	}

	if len(params) > 0 {
		var headerComponent map[string]interface{}
		headerKey := ""
		documentFilename, hasDocumentFilename := params["header_document_filename"]
		var bodyParams []map[string]interface{}
		var buttonComponents []map[string]interface{}

		for key, value := range params {
			switch {
			case key == "header" || strings.HasPrefix(key, "header:"):
				if headerComponent != nil {
					return fmt.Errorf("template %s: both %q and %q set the header, a template has only one", templateName, headerKey, key)
				}
				param := map[string]interface{}{"type": "text", "text": value}
				if strings.HasPrefix(key, "header:") {
					name := strings.TrimPrefix(key, "header:")
					if name == "" {
						return fmt.Errorf("template %s: parameter key %q is missing the parameter name after the colon", templateName, key)
					}
					param["parameter_name"] = name
				}
				headerComponent = map[string]interface{}{
					"type":       "header",
					"parameters": []map[string]interface{}{param},
				}
				headerKey = key
			case key == "header_document_filename":
				// Applied to the document header once the loop knows which header the message has.
			case strings.HasPrefix(key, "header_"):
				if headerComponent != nil {
					return fmt.Errorf("template %s: both %q and %q set the header, a template has only one", templateName, headerKey, key)
				}
				mediaType := strings.TrimPrefix(key, "header_")
				if !headerMediaTypes[mediaType] {
					return fmt.Errorf("template %s: parameter key %q asks for header media type %q, which Meta does not accept", templateName, key, mediaType)
				}
				media := map[string]interface{}{"link": value}
				// An empty name is left out: Meta would refuse the message for it.
				if mediaType == "document" && hasDocumentFilename && documentFilename != "" {
					media["filename"] = documentFilename
				}
				headerComponent = map[string]interface{}{
					"type": "header",
					"parameters": []map[string]interface{}{
						{"type": mediaType, mediaType: media},
					},
				}
				headerKey = key
			case strings.HasPrefix(key, "button_"):
				btnIndex := strings.TrimPrefix(key, "button_")
				buttonComponents = append(buttonComponents, map[string]interface{}{
					"type":     "button",
					"sub_type": "url",
					"index":    btnIndex,
					"parameters": []map[string]interface{}{
						{"type": "text", "text": value},
					},
				})
			default:
				bodyParams = append(bodyParams, map[string]interface{}{
					"type":           "text",
					"text":           value,
					"parameter_name": key,
				})
			}
		}

		if hasDocumentFilename && headerKey != "header_document" {
			return fmt.Errorf("template %s: \"header_document_filename\" needs a \"header_document\" key", templateName)
		}

		var components []map[string]interface{}
		if headerComponent != nil {
			components = append(components, headerComponent)
		}
		if len(bodyParams) > 0 {
			components = append(components, map[string]interface{}{
				"type":       "body",
				"parameters": bodyParams,
			})
		}
		components = append(components, buttonComponents...)
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
		sendErr := &WhatsAppSendError{cause: err}
		logger.Error.Printf("WhatsApp SendTemplateMessage: %v", sendErr)
		return sendErr
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj struct {
			Error struct {
				Code        int    `json:"code"`
				Subcode     int    `json:"error_subcode"`
				IsTransient bool   `json:"is_transient"`
				FbtraceID   string `json:"fbtrace_id"`
			} `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&respObj)
		sendErr := &WhatsAppSendError{
			StatusCode: resp.StatusCode, Code: respObj.Error.Code,
			Subcode: respObj.Error.Subcode, IsTransient: respObj.Error.IsTransient,
			FbtraceID:  respObj.Error.FbtraceID,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
		var networkErr net.Error
		if errors.As(decodeErr, &networkErr) || errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, io.ErrUnexpectedEOF) {
			// Timeouts/disconnects can occur after the response headers arrive.
			sendErr.cause = decodeErr
		}
		logger.Error.Printf("WhatsApp SendTemplateMessage: %v", sendErr)
		return sendErr
	}
	logger.Info.Println("WhatsApp SendTemplateMessage: delivered to API")

	return nil
}
