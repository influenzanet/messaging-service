package config

import "strings"

// WhatsAppWebhookFromEnv names the business number whose webhook notifications the messaging
// service processes, and the instance its participants belong to: WHATSAPP_PHONE_NUMBER_ID, the
// number the message-scheduler sends from (an identifier, not a credential), and
// WHATSAPP_WEBHOOK_INSTANCE_ID. ok is false unless both are set.
func WhatsAppWebhookFromEnv(getenv func(string) string) (phoneNumberID, instanceID string, ok bool) {
	phoneNumberID = strings.TrimSpace(getenv("WHATSAPP_PHONE_NUMBER_ID"))
	instanceID = strings.TrimSpace(getenv("WHATSAPP_WEBHOOK_INSTANCE_ID"))
	if phoneNumberID == "" || instanceID == "" {
		return "", "", false
	}
	return phoneNumberID, instanceID, true
}
