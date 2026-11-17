package main

import (
	"errors"
	"fmt"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
)

// defaultWhatsAppMaxAge is how long, in seconds, a message may wait in outgoing-whatsapp when
// MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE is not set. A message that has waited longer is archived as
// expired and never sent. One day is longer than any legitimate wait in the queue (a refused
// template keeps its messages for its five tries, a throttle pause lasts at most an hour) and
// short enough for a reminder, which is of no use the day after.
const defaultWhatsAppMaxAge int64 = 86400

// parseWhatsAppMaxAge reads MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE: empty means the default,
// anything else must be a positive number of seconds.
func parseWhatsAppMaxAge(value string) (int64, error) {
	return parsePositiveSeconds(value, defaultWhatsAppMaxAge)
}

// whatsAppMaxAgeProblem names the configuration in which the max age cuts short the wait of a
// refused template: its messages are tried once every templateRetryDelay seconds until their
// attempts are spent, so with a shorter max age they expire before the template has had all
// its tries. It returns an empty string when the max age leaves room for every try.
func whatsAppMaxAgeProblem(maxAge, templateRetryDelay int64) string {
	deferralWindow := int64(maxWhatsAppSendAttempts) * templateRetryDelay
	if maxAge > deferralWindow {
		return ""
	}
	return fmt.Sprintf(
		"MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE is %d s, not longer than the %d s a refused template keeps its messages (%d tries, %d s apart): such messages expire before their last try. Raise it above %d s or lower MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY",
		maxAge, deferralWindow, maxWhatsAppSendAttempts, templateRetryDelay, deferralWindow,
	)
}

// whatsAppMessageExpired reports whether a message queued at addedAt has, at now, waited
// longer than maxAge seconds. A message exactly maxAge seconds old may still be sent.
func whatsAppMessageExpired(addedAt, now, maxAge int64) bool {
	return now-addedAt > maxAge
}

// expireWhatsApp takes a message that waited too long out of the queue and archives it as
// expired, without sending it. It reports whether this call took the message out.
//
// Unlike archiveWhatsApp, the queued row is deleted first. Whoever deletes the row archives
// it, so a participant's cancellation running at the same moment never leaves two archive rows
// for one message. The price is that a failed archive write leaves no trace of the message
// but the error log; the message itself is not sent either way.
func expireWhatsApp(mdb *messagedb.MessageDBService, instanceID string, msg types.OutgoingWhatsApp) bool {
	if err := mdb.DeleteOutgoingWhatsApp(instanceID, msg.ID.Hex()); err != nil {
		if errors.Is(err, messagedb.ErrOutgoingWhatsAppNotFound) {
			// Cancelled for the participant, or archived by another tick, in the meantime.
			return false
		}
		// Still queued under this tick's claim: a later tick expires it.
		logger.Error.Printf("[%s] Error removing expired whatsapp ('%s') from the queue: %v", instanceID, msg.MessageType, err)
		return false
	}
	if _, err := mdb.AddToSentWhatsApp(instanceID, msg, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusExpired}); err != nil {
		logger.Error.Printf("[%s] Error archiving expired whatsapp ('%s'), it is out of the queue and was not sent: %v", instanceID, msg.MessageType, err)
	}
	return true
}
