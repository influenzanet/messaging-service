package bulk_messages

// WhatsAppGenerationEnabled reports whether the generators are allowed to queue WhatsApp
// messages, that is whether WHATSAPP_ENABLED was "true" when this package was initialised.
// The message-scheduler reads it to keep delivery aligned with generation.
func WhatsAppGenerationEnabled() bool {
	return whatsAppEnabled
}
