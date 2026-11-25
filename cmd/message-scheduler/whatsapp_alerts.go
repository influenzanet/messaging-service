package main

import (
	"fmt"
	"strings"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
)

// whatsAppAlertPrefix starts every log line an operator has to act on, so that one rule in the
// log stack can match them all and the word after it tells them apart.
const whatsAppAlertPrefix = "WA_ALERT"

// whatsAppQueuePrefix starts the line that states an instance's queue size after each tick.
const whatsAppQueuePrefix = "WA_QUEUE"

// whatsAppAlert names one kind of event an operator has to hear about. The names are part of
// the alert rules and of the docs: change one only together with them.
type whatsAppAlert string

const (
	// The runner did not start although WhatsApp is not turned off here: queued messages are
	// never sent.
	alertRunnerNotStarted whatsAppAlert = "runner_not_started"
	// Meta refused the sender's credentials or account: the tick stopped.
	alertAuthStop whatsAppAlert = "auth_stop"
	// Meta is limiting the sender: sending pauses.
	alertThrottlePause whatsAppAlert = "throttle_pause"
	// Meta refused a template: its messages wait for the retry delay.
	alertTemplateRefused whatsAppAlert = "template_refused"
	// Meta refused a message for good at its first attempt: it is archived as failed.
	alertPermanentFailure whatsAppAlert = "permanent_failure"
	// A message used up its attempts: it is archived as failed.
	alertArchivedFailed whatsAppAlert = "archived_failed"
	// The archive write of a message that was sent, or given up on, failed: the message stays
	// queued until a later tick archives it, and the delivery statuses Meta reports for it in
	// the meantime are not recorded.
	alertArchiveWriteFailed whatsAppAlert = "archive_write_failed"
	// Messages waited longer than the maximum age: they are archived as expired, unsent.
	alertExpired whatsAppAlert = "expired"
	// Messages that are not held back by a refused template have waited longer than
	// whatsAppBacklogAge.
	alertQueueBacklog whatsAppAlert = "queue_backlog"
)

// whatsAppBacklogAge is how long, in seconds, a message may wait in the queue, not counting a
// refused template's deferral, before the queue is reported as backed up. In normal operation a
// message goes out on the tick after it was queued; a throttle pause lasts about an hour at
// most.
const whatsAppBacklogAge int64 = 3600

// alertField is one key=value pair of an alert line.
type alertField struct {
	key   string
	value interface{}
}

// logWhatsAppAlert writes one ERROR line: the prefix, the alert name, the fields as key=value
// pairs in the order given, then a colon and the explanation for the operator. A value that
// holds a space or is empty is quoted, so the pairs stay machine-readable. Never pass a phone
// number, a message parameter or a token in a field or in the detail.
func logWhatsAppAlert(alert whatsAppAlert, fields []alertField, detail string) {
	var line strings.Builder
	line.WriteString(whatsAppAlertPrefix)
	line.WriteString(" ")
	line.WriteString(string(alert))
	for _, f := range fields {
		line.WriteString(" ")
		line.WriteString(f.key)
		line.WriteString("=")
		line.WriteString(alertValue(f.value))
	}
	line.WriteString(": ")
	line.WriteString(detail)
	logger.Error.Print(line.String())
}

// alertValue renders a field value, quoted when it holds a space, a quote or an equals sign,
// or is empty.
func alertValue(value interface{}) string {
	s := fmt.Sprint(value)
	if s == "" || strings.ContainsAny(s, " \t\"=") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// reportWhatsAppQueue writes the instance's queue size at now as one INFO line, and raises
// queue_backlog when messages that are not deferred have waited longer than whatsAppBacklogAge.
// A failed read is logged and changes nothing else.
func reportWhatsAppQueue(mdb *messagedb.MessageDBService, instanceID string, now int64) {
	depth, err := mdb.GetWhatsAppQueueDepth(instanceID, now, now-whatsAppBacklogAge)
	if err != nil {
		logger.Error.Printf("[%s] cannot read the size of the WhatsApp queue: %v", instanceID, err)
		return
	}
	oldestAge := int64(0)
	if depth.Waiting() > 0 {
		oldestAge = now - depth.OldestWaitingAddedAt
	}
	logger.Info.Printf("%s instance=%s queued=%d waiting=%d deferred=%d oldest_waiting_s=%d",
		whatsAppQueuePrefix, alertValue(instanceID), depth.Queued, depth.Waiting(), depth.Deferred, oldestAge)
	if depth.Stale > 0 {
		logWhatsAppAlert(alertQueueBacklog, []alertField{
			{"instance", instanceID}, {"stale", depth.Stale}, {"oldest_waiting_s", oldestAge}, {"threshold_s", whatsAppBacklogAge},
		}, fmt.Sprintf("%d WhatsApp message(s) have waited in the queue more than %d s without being sent; messages of a refused template are not counted", depth.Stale, whatsAppBacklogAge))
	}
}
