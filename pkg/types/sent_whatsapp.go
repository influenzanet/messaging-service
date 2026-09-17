package types

// Outcomes of a WhatsApp message that left the outgoing queue. Without them an archived row
// that Meta accepted and one the scheduler gave up on cannot be told apart: both carry the
// same fields, sendAttempt included.
const (
	WhatsAppStatusDelivered = "delivered"
	WhatsAppStatusFailed    = "failed"
)

// WhatsAppSendOutcome is how a message left the queue. On a failure it names Meta's numeric
// error code and the class the retry policy read from it; Meta's free-form message and
// response body are deliberately not kept, as either may carry personal data.
type WhatsAppSendOutcome struct {
	Status     string
	ErrorCode  int
	ErrorClass string
}

// SentWhatsApp is an archived message: the queued message as it was last read, plus the
// outcome that took it out of the queue. The outcome fields belong to the archive alone, so
// they are kept off OutgoingWhatsApp and never written to the queue.
type SentWhatsApp struct {
	OutgoingWhatsApp `bson:",inline"`

	Status string `bson:"status"`
	// ErrorCode is 0 on a delivered message and on a failure that never reached Meta.
	ErrorCode int `bson:"errorCode"`
	// ErrorClass is empty on a delivered message.
	ErrorClass string `bson:"errorClass,omitempty"`
	// SentAt is the moment the message was archived, delivered or given up on.
	SentAt int64 `bson:"sentAt"`
}
