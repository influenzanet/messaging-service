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
	// WhatsAppErrorTemplate: the template itself cannot be used (paused or disabled), so every
	// message of the campaign fails the same way until an operator acts on it.
	WhatsAppErrorTemplate
)

// String names the class for a log line, and these exact names are also persisted by the
// message-scheduler in the `errorClass` field of every failed row in sent-whatsapp. Renaming
// one would split the archive into rows that no single query can select: add a class rather
// than rename a name.
func (c WhatsAppErrorClass) String() string {
	switch c {
	case WhatsAppErrorAuth:
		return "authentication"
	case WhatsAppErrorThrottled:
		return "rate limit"
	case WhatsAppErrorTransient:
		return "transient"
	case WhatsAppErrorRecipientThrottled:
		return "recipient limit"
	case WhatsAppErrorTemplate:
		return "template unusable"
	}
	return "unknown"
}

// WhatsAppSendError retains machine-readable failure information, but deliberately
// excludes Meta's free-form message and the response body: either may contain PII.
type WhatsAppSendError struct {
	StatusCode  int
	Code        int
	Subcode     int
	IsTransient bool
	// FbtraceID is the request identifier Meta support asks for; it carries no PII.
	FbtraceID string
	cause     error
}

func (e *WhatsAppSendError) Error() string {
	msg := fmt.Sprintf("failed to send template message, status code: %d, Meta code: %d, subcode: %d", e.StatusCode, e.Code, e.Subcode)
	if e.FbtraceID != "" {
		msg += ", fbtrace_id: " + e.FbtraceID
	}
	if e.cause != nil {
		// A transport error names the host and the failure, never the recipient or the token.
		msg += ", cause: " + e.cause.Error()
	}
	return msg
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
	case e.Code == 100 && e.Subcode == 33:
		// The generic Graph shape of a deleted or unknown sender phone number.
		return WhatsAppErrorAuth
	}
	switch e.Code {
	case 3, 10, 33, 190, 368, 131005, 131031, 131042, 131045, 133010:
		// Permissions, token, account restrictions, payment, unregistered, deleted or
		// misregistered sender.
		return WhatsAppErrorAuth
	case 4, 17, 341, 613, 80007, 130429, 131048, 131064:
		// 131064: messaging limit exceeded for template classification violations (account level).
		return WhatsAppErrorThrottled
	case 1, 2, 131000, 131016, 131057:
		return WhatsAppErrorTransient
	case 132015, 132016, 131063:
		// Template paused for quality, or disabled; 131063: the account has disabled marketing
		// messages on Cloud API, so every marketing template is rejected until an operator acts.
		// 132001 (unknown template) and 132018 (invalid parameters, v23+) are left to the bounded
		// retry: they are per message or per language, and the queue must keep moving.
		return WhatsAppErrorTemplate
	}
	if e.Code >= 200 && e.Code <= 299 {
		return WhatsAppErrorAuth
	}
	// Message-specific and unknown failures keep the existing bounded retry policy.
	return WhatsAppErrorUnknown
}
