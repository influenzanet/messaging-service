package main

import (
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
)

// whatsAppTemplateKey identifies what Meta approves, pauses and disables: a template in one
// language.
func whatsAppTemplateKey(msg types.OutgoingWhatsApp) string {
	return msg.TemplateName + "|" + msg.Lang
}

// deferWhatsAppTemplate handles a template Meta refuses (paused, disabled, marketing turned
// off). Only an operator or Meta can lift that, and every message of the template would get the
// same answer, so the whole template is dealt with at once instead of message by message: each
// of its messages pays one attempt, the ones on their last attempt are archived as failed, and
// the others wait retryDelay seconds before the queue hands them out again. The messages of the
// other templates are not affected. Only messages this tick claimed (handled) or that no tick
// holds are touched.
func deferWhatsAppTemplate(mdb *messagedb.MessageDBService, instanceID string, msg types.OutgoingWhatsApp, cause error, handled map[string]bool, lockSeconds, retryDelay int64) {
	now := time.Now().Unix()
	own := make([]string, 0, len(handled))
	for id := range handled {
		own = append(own, id)
	}
	unclaimedBefore := now - lockSeconds
	outcome := failedWhatsApp(cause)

	// Archive first: deferring first would bring the messages one short of the cap onto it,
	// and they would be archived an attempt early.
	archived := 0
	lastTry, err := mdb.FindOutgoingWhatsAppOfTemplate(instanceID, msg.TemplateName, msg.Lang, own, unclaimedBefore, maxWhatsAppSendAttempts-1)
	if err != nil {
		logger.Error.Printf("[%s] cannot read the messages of WhatsApp template '%s' (%s) on their last attempt: %v", instanceID, msg.TemplateName, msg.Lang, err)
	}
	for _, last := range lastTry {
		if archiveWhatsApp(mdb, instanceID, last, outcome) == nil {
			archived++
		}
	}

	// The queue hands a message out once its claim is older than lockSeconds.
	deferred, err := mdb.DeferOutgoingWhatsAppOfTemplate(instanceID, msg.TemplateName, msg.Lang, own, unclaimedBefore, maxWhatsAppSendAttempts-1, now+retryDelay-lockSeconds)
	if err != nil {
		logger.Error.Printf("[%s] cannot defer the messages of WhatsApp template '%s' (%s): %v", instanceID, msg.TemplateName, msg.Lang, err)
	}
	logger.Error.Printf("[%s] WhatsApp template '%s' (%s) refused by Meta (code %d, %s): %d message(s) wait %d s before the next try, %d archived as failed after their last attempt; the other templates keep going",
		instanceID, msg.TemplateName, msg.Lang, outcome.ErrorCode, outcome.ErrorClass, deferred, retryDelay, archived)
}
