package bulk_messages

// WhatsAppGenerationEnabled reports whether the generators are allowed to queue WhatsApp
// messages, that is whether WHATSAPP_ENABLED was "true" when this package was initialised.
// The message-scheduler reads it to keep delivery aligned with generation.
func WhatsAppGenerationEnabled() bool {
	return whatsAppEnabled
}

// SetWhatsAppGenerationEnabled overrides that decision for the running process. It exists for
// the message-scheduler, which stops generating WhatsApp messages it would not be able to
// deliver. Call it at start-up, before any generator runs: the flag is not synchronised.
func SetWhatsAppGenerationEnabled(enabled bool) {
	whatsAppEnabled = enabled
}
