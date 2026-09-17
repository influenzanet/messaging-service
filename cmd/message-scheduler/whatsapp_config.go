package main

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
