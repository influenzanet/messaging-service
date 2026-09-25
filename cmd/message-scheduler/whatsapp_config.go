package main

import (
	"fmt"
	"strconv"
	"time"
)

// defaultWhatsAppTemplateRetryDelay is the wait, in seconds, before the messages of a template
// Meta refuses are tried again when MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY is not set.
// With the five-attempt cap it keeps such messages for about four hours, which outlasts
// Meta's first pause of a template for quality.
const defaultWhatsAppTemplateRetryDelay int64 = 3600

// parseWhatsAppTemplateRetryDelay reads MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY: empty
// means the default, anything else must be a positive number of seconds.
func parseWhatsAppTemplateRetryDelay(value string) (int64, error) {
	if value == "" {
		return defaultWhatsAppTemplateRetryDelay, nil
	}
	delay, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	if delay <= 0 {
		return 0, fmt.Errorf("%d is not a positive number of seconds", delay)
	}
	return delay, nil
}

// shouldRunOutgoingWhatsApp decides whether the outgoing WhatsApp runner is started, and
// returns the reason when it is not, so that the caller can log a single explicit line.
// The disabled flag wins: WHATSAPP_ENABLED is the switch operators expect to stop WhatsApp
// delivery, whatever the rest of the configuration says.
func shouldRunOutgoingWhatsApp(enabled bool, freq int, hasClient bool) (bool, string) {
	if !enabled {
		return false, "WHATSAPP_ENABLED is not true"
	}
	if freq <= 0 {
		return false, "no period defined, MESSAGE_SCHEDULER_INTERVAL_WHATSAPP is not a positive number of seconds"
	}
	if !hasClient {
		return false, "no WhatsApp client configured, WHATSAPP_TOKEN or WHATSAPP_PHONE_NUMBER_ID is not set"
	}
	return true, ""
}

// checkWhatsAppConfig decides whether this process may generate WhatsApp messages, and
// returns the configuration problems that prevent it from delivering them. Generation and
// delivery are configured separately, so a scheduler that generates without being able to
// deliver silently fills the outgoing-whatsapp queue: in that case generation is refused and
// every missing variable is named, so that the queue does not grow behind an operator's back.
func checkWhatsAppConfig(generationEnabled bool, clientConfigured bool, interval int) (bool, []string) {
	if !generationEnabled {
		return false, nil
	}

	var problems []string
	if !clientConfigured {
		problems = append(problems, "WHATSAPP_ENABLED is true but no WhatsApp client could be built: WHATSAPP_TOKEN or WHATSAPP_PHONE_NUMBER_ID is not set")
	}
	if interval <= 0 {
		problems = append(problems, "WHATSAPP_ENABLED is true but no WhatsApp send interval is defined: MESSAGE_SCHEDULER_INTERVAL_WHATSAPP is not a positive number of seconds")
	}
	if len(problems) > 0 {
		return false, problems
	}
	return true, nil
}

// whatsAppSendWindow is how long after a batch was claimed a send may still start. A send
// waits up to sendTimeout, so a send started later than lockSeconds-sendTimeout can outlive
// the claim it works under and let another tick hand the same message to Meta. The batch
// limit of nine tenths of the lock still applies, and is the only limit left when the lock is
// no longer than the send timeout: no window exists there, and refusing every send would stop
// delivery altogether rather than make it safe.
func whatsAppSendWindow(lockSeconds int64, sendTimeout time.Duration) int64 {
	batchLimit := int64(float64(lockSeconds) * 0.9)
	timeoutSeconds := int64(sendTimeout / time.Second)
	if lockSeconds <= timeoutSeconds {
		return batchLimit
	}
	if window := lockSeconds - timeoutSeconds; window < batchLimit {
		return window
	}
	return batchLimit
}

// whatsAppLockProblem names the configuration in which the claim lock a send interval gives is
// not longer than the send timeout: every send can then outlive its claim, so a message can
// reach Meta twice and the send window is left to the batch limit alone. It returns an empty
// string when the lock is long enough, and otherwise the shortest interval that would do.
func whatsAppLockProblem(freq int, sendTimeout time.Duration) string {
	timeoutSeconds := int64(sendTimeout / time.Second)
	lockSeconds := getThreadLockInterval(freq)
	if lockSeconds > timeoutSeconds {
		return ""
	}
	minimumFreq := freq
	for getThreadLockInterval(minimumFreq) <= timeoutSeconds {
		minimumFreq++
	}
	return fmt.Sprintf(
		"the claim lock lasts %d s, no longer than the %d s send timeout: a send can outlive its claim and the same message can reach Meta twice. Raise MESSAGE_SCHEDULER_INTERVAL_WHATSAPP to at least %d s",
		lockSeconds, timeoutSeconds, minimumFreq,
	)
}
