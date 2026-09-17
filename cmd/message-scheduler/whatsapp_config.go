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
