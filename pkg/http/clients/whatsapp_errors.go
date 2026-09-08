package clients

import (
	"fmt"
	"net/http"
)

type WhatsAppErrorClass int

const (
	WhatsAppErrorUnknown WhatsAppErrorClass = iota
	WhatsAppErrorAuth
	WhatsAppErrorThrottled
	WhatsAppErrorTransient
	WhatsAppErrorRecipientThrottled
)

// WhatsAppSendError retains machine-readable failure information, but deliberately
// excludes Meta's free-form message and the response body: either may contain PII.
type WhatsAppSendError struct {
	StatusCode  int
	Code        int
	Subcode     int
	IsTransient bool
	cause       error
}

func (e *WhatsAppSendError) Error() string {
	return fmt.Sprintf("failed to send template message, status code: %d, Meta code: %d, subcode: %d", e.StatusCode, e.Code, e.Subcode)
}

func (e *WhatsAppSendError) Unwrap() error { return e.cause }

// Class distinguishes message failures from sender/API failures that must not
// exhaust the retry budget of every queued message. Global HTTP failures take
// precedence even when a proxy returns a malformed or contradictory error body.
// Meta references:
// https://developers.facebook.com/docs/whatsapp/cloud-api/support/error-codes/
// https://developers.facebook.com/docs/graph-api/guides/error-handling/
func (e *WhatsAppSendError) Class() WhatsAppErrorClass {
	switch {
	case e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden:
		return WhatsAppErrorAuth
	case e.cause != nil || e.StatusCode == http.StatusRequestTimeout || e.StatusCode >= 500:
		return WhatsAppErrorTransient
	case e.Code == 131056:
		// This limit applies only to the sender/recipient pair, not the queue.
		return WhatsAppErrorRecipientThrottled
	case e.StatusCode == http.StatusTooManyRequests:
		return WhatsAppErrorThrottled
	case e.IsTransient:
		return WhatsAppErrorTransient
	}
	switch e.Code {
	case 3, 10, 190, 368, 131005, 131031, 131042, 133010:
		// Permissions, token, account restrictions, payment or unregistered sender.
		return WhatsAppErrorAuth
	case 4, 17, 341, 613, 80007, 130429, 131048:
		return WhatsAppErrorThrottled
	case 1, 2, 131000, 131016, 131057:
		return WhatsAppErrorTransient
	}
	if e.Code >= 200 && e.Code <= 299 {
		return WhatsAppErrorAuth
	}
	// Message-specific and unknown failures keep the existing bounded retry policy.
	return WhatsAppErrorUnknown
}
