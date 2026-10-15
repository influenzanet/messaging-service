package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/coneno/logger"
)

// WhatsAppSettings is the process-level WhatsApp configuration read from the environment
// today. Its fields are unexported on purpose: the only way to ask "is WhatsApp on?" is the
// Enabled method below, so that when the configuration moves to the database only the body of
// Enabled (and of the function that builds a WhatsAppSettings) has to change, and nothing that
// already calls Enabled(instanceID) needs to know.
type WhatsAppSettings struct {
	enabledSwitch string
	token         string
	phoneNumberID string
	apiVersion    string
}

// WhatsAppSettingsFromEnv builds a WhatsAppSettings from the process environment. Call it once
// at start-up in each process.
func WhatsAppSettingsFromEnv() WhatsAppSettings {
	return NewWhatsAppSettings(os.Getenv)
}

// NewWhatsAppSettings builds a WhatsAppSettings from any getenv-shaped function. It exists so
// that tests, including in other packages, can build an enabled or disabled value without
// reading the real environment; production code always calls WhatsAppSettingsFromEnv.
func NewWhatsAppSettings(getenv func(string) string) WhatsAppSettings {
	return WhatsAppSettings{
		enabledSwitch: getenv("WHATSAPP_ENABLED"),
		token:         getenv("WHATSAPP_TOKEN"),
		phoneNumberID: getenv("WHATSAPP_PHONE_NUMBER_ID"),
		apiVersion:    getenv("WHATSAPP_API_VERSION"),
	}
}

// Enabled is the single decision of whether WhatsApp is on: WHATSAPP_ENABLED is exactly
// "true" and both Meta credentials are set. instanceID is not read yet - it is the hook where
// a future per-instance, database-backed configuration plugs in, and every caller already
// passes the instance it is working on so that switch, once it happens, needs no caller change.
func (s WhatsAppSettings) Enabled(instanceID string) bool {
	return s.enabledSwitch == "true" && s.token != "" && s.phoneNumberID != ""
}

// Token returns the configured Meta access token, or "" when none was set.
func (s WhatsAppSettings) Token() string {
	return s.token
}

// PhoneNumberID returns the configured Meta phone number id, or "" when none was set.
func (s WhatsAppSettings) PhoneNumberID() string {
	return s.phoneNumberID
}

// APIVersion returns the configured Graph API version override, or "" to let the client
// fall back to its own default.
func (s WhatsAppSettings) APIVersion() string {
	return s.apiVersion
}

// Problems lists the process-level configuration mistakes in s as human-readable lines that
// name the offending variables and never contain the value of a credential. An empty slice
// means the configuration is coherent: fully on, or off with no leftover credentials.
func (s WhatsAppSettings) Problems() []string {
	var problems []string

	if s.enabledSwitch == "true" {
		var missing []string
		if s.token == "" {
			missing = append(missing, "WHATSAPP_TOKEN")
		}
		if s.phoneNumberID == "" {
			missing = append(missing, "WHATSAPP_PHONE_NUMBER_ID")
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"WHATSAPP_ENABLED is \"true\" but %s not set: WhatsApp stays off",
				strings.Join(missing, " and ")))
		}
	}

	if s.enabledSwitch != "" && s.enabledSwitch != "true" && s.enabledSwitch != "false" {
		problems = append(problems, fmt.Sprintf(
			"WHATSAPP_ENABLED is set to %q: WhatsApp stays off because only the exact value \"true\" turns it on",
			s.enabledSwitch))
	}

	if s.enabledSwitch != "true" && (s.token != "" || s.phoneNumberID != "") {
		problems = append(problems, "WHATSAPP_ENABLED is not \"true\": the configured WhatsApp credentials are ignored")
	}

	return problems
}

// LogWhatsAppStartup writes one Info line saying whether WhatsApp is on for process, followed
// by one Warning line per entry of s.Problems(). It never writes a credential value.
func LogWhatsAppStartup(process string, s WhatsAppSettings) {
	if s.Enabled("") {
		logger.Info.Printf("WhatsApp enabled for %s", process)
	} else {
		logger.Info.Printf("WhatsApp disabled for %s", process)
	}
	for _, problem := range s.Problems() {
		logger.Warning.Printf("%s", problem)
	}
}
