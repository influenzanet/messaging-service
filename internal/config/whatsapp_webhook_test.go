package config

import "testing"

// The webhook is processed only when both the business number and its instance are named.
// Fails if one of them may be missing, or if a value padded with spaces is used as it is.
func TestWhatsAppWebhookFromEnv(t *testing.T) {
	for _, c := range []struct {
		name               string
		phone, instance    string
		wantOK             bool
		wantPhone, wantIns string
	}{
		{"both set", "123", "italy", true, "123", "italy"},
		{"spaces around", " 123 ", "\titaly\n", true, "123", "italy"},
		{"no number", "", "italy", false, "", ""},
		{"no instance", "123", "", false, "", ""},
		{"only spaces", " ", " ", false, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"WHATSAPP_PHONE_NUMBER_ID": c.phone, "WHATSAPP_WEBHOOK_INSTANCE_ID": c.instance}
			phone, instance, ok := WhatsAppWebhookFromEnv(func(k string) string { return env[k] })
			if ok != c.wantOK || phone != c.wantPhone || instance != c.wantIns {
				t.Errorf("got %q %q %v, want %q %q %v", phone, instance, ok, c.wantPhone, c.wantIns, c.wantOK)
			}
		})
	}
}
