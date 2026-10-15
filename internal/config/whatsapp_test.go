package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

// envMap builds a getenv-shaped function over a fixed map, so every case below is explicit
// about exactly which variables are set.
func envMap(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestWhatsAppSettingsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		enabled  string
		token    string
		phoneID  string
		expected bool
	}{
		{"unset switch, no credentials", "", "", "", false},
		{"unset switch, both credentials", "", "tok", "phone", false},
		{"false switch, both credentials", "false", "tok", "phone", false},
		{"true switch, no credentials", "true", "", "", false},
		{"true switch, only token", "true", "tok", "", false},
		{"true switch, only phone id", "true", "", "phone", false},
		{"true switch, both credentials", "true", "tok", "phone", true},
		{"uppercase TRUE, both credentials", "TRUE", "tok", "phone", false},
		{"numeric switch, both credentials", "1", "tok", "phone", false},
		{"yes switch, both credentials", "yes", "tok", "phone", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := NewWhatsAppSettings(envMap(map[string]string{
				"WHATSAPP_ENABLED":         tt.enabled,
				"WHATSAPP_TOKEN":           tt.token,
				"WHATSAPP_PHONE_NUMBER_ID": tt.phoneID,
			}))
			if got := settings.Enabled("some-instance"); got != tt.expected {
				t.Errorf("Enabled() = %v, want %v", got, tt.expected)
			}
			// instanceID is ignored today: any instance gets the same answer.
			if got := settings.Enabled(""); got != tt.expected {
				t.Errorf("Enabled(\"\") = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestWhatsAppSettingsAccessors(t *testing.T) {
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_TOKEN":           "tok-value",
		"WHATSAPP_PHONE_NUMBER_ID": "phone-value",
		"WHATSAPP_API_VERSION":     "v99.0",
	}))
	if got := settings.Token(); got != "tok-value" {
		t.Errorf("Token() = %q, want %q", got, "tok-value")
	}
	if got := settings.PhoneNumberID(); got != "phone-value" {
		t.Errorf("PhoneNumberID() = %q, want %q", got, "phone-value")
	}
	if got := settings.APIVersion(); got != "v99.0" {
		t.Errorf("APIVersion() = %q, want %q", got, "v99.0")
	}
}

func TestWhatsAppSettingsZeroValueIsDisabled(t *testing.T) {
	var settings WhatsAppSettings
	if settings.Enabled("any") {
		t.Error("expected the zero value to be disabled")
	}
	if problems := settings.Problems(); len(problems) != 0 {
		t.Errorf("expected no problems for the zero value, got %q", problems)
	}
}

func TestWhatsAppSettingsProblems(t *testing.T) {
	tests := []struct {
		name         string
		enabled      string
		token        string
		phoneID      string
		wantContains []string
		wantCount    int
	}{
		{
			name:      "off, nothing configured",
			enabled:   "",
			wantCount: 0,
		},
		{
			name:      "explicitly off, nothing configured",
			enabled:   "false",
			wantCount: 0,
		},
		{
			name:      "on, fully configured",
			enabled:   "true",
			token:     "tok",
			phoneID:   "phone",
			wantCount: 0,
		},
		{
			name:         "on, missing token only",
			enabled:      "true",
			phoneID:      "phone",
			wantContains: []string{"WHATSAPP_TOKEN"},
			wantCount:    1,
		},
		{
			name:         "on, missing phone id only",
			enabled:      "true",
			token:        "tok",
			wantContains: []string{"WHATSAPP_PHONE_NUMBER_ID"},
			wantCount:    1,
		},
		{
			name:         "on, missing both credentials",
			enabled:      "true",
			wantContains: []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_NUMBER_ID"},
			wantCount:    1,
		},
		{
			name:         "weird switch value, no credentials",
			enabled:      "TRUE",
			wantContains: []string{"WHATSAPP_ENABLED"},
			wantCount:    1,
		},
		{
			name:         "weird switch value, with credentials",
			enabled:      "1",
			token:        "tok",
			phoneID:      "phone",
			wantContains: []string{"WHATSAPP_ENABLED", "ignored"},
			wantCount:    2,
		},
		{
			name:         "off but credentials present",
			enabled:      "false",
			token:        "tok",
			phoneID:      "phone",
			wantContains: []string{"ignored"},
			wantCount:    1,
		},
		{
			name:         "unset switch but credentials present",
			enabled:      "",
			token:        "tok",
			wantContains: []string{"ignored"},
			wantCount:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := NewWhatsAppSettings(envMap(map[string]string{
				"WHATSAPP_ENABLED":         tt.enabled,
				"WHATSAPP_TOKEN":           tt.token,
				"WHATSAPP_PHONE_NUMBER_ID": tt.phoneID,
			}))
			problems := settings.Problems()
			if len(problems) != tt.wantCount {
				t.Fatalf("Problems() = %q, want %d entries", problems, tt.wantCount)
			}
			joined := strings.Join(problems, " | ")
			for _, want := range tt.wantContains {
				if !strings.Contains(joined, want) {
					t.Errorf("Problems() = %q, expected it to mention %q", problems, want)
				}
			}
		})
	}
}

// captureLoggerOutput redirects coneno/logger's Info and Warning writers to a buffer for the
// duration of fn, and restores the previous writers afterwards.
func captureLoggerOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevInfoOut := logger.Info.Writer()
	prevWarningOut := logger.Warning.Writer()
	logger.Info.SetOutput(&buf)
	logger.Warning.SetOutput(&buf)
	t.Cleanup(func() {
		logger.Info.SetOutput(prevInfoOut)
		logger.Warning.SetOutput(prevWarningOut)
	})
	fn()
	return buf.String()
}

func TestLogWhatsAppStartup(t *testing.T) {
	t.Run("switch off", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{}))
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp disabled for message-scheduler") {
			t.Errorf("expected a disabled line, got %q", out)
		}
	})

	t.Run("switch on, complete", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_ENABLED":         "true",
			"WHATSAPP_TOKEN":           "tok",
			"WHATSAPP_PHONE_NUMBER_ID": "phone",
		}))
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp enabled for message-scheduler") {
			t.Errorf("expected an enabled line, got %q", out)
		}
	})

	t.Run("switch on, missing token", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_ENABLED":         "true",
			"WHATSAPP_PHONE_NUMBER_ID": "phone",
		}))
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp disabled for message-scheduler") {
			t.Errorf("expected a disabled line, got %q", out)
		}
		if !strings.Contains(out, "WHATSAPP_TOKEN") {
			t.Errorf("expected the missing variable to be named, got %q", out)
		}
	})

	t.Run("switch TRUE", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_ENABLED": "TRUE",
		}))
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp disabled for message-scheduler") {
			t.Errorf("expected a disabled line, got %q", out)
		}
		if !strings.Contains(out, "WHATSAPP_ENABLED") || !strings.Contains(out, "TRUE") {
			t.Errorf("expected the switch value to be named, got %q", out)
		}
	})

	t.Run("never logs the credential values", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_ENABLED":         "weird",
			"WHATSAPP_TOKEN":           "super-secret-token-value",
			"WHATSAPP_PHONE_NUMBER_ID": "super-secret-phone-id-value",
		}))
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		for _, secret := range []string{"super-secret-token-value", "super-secret-phone-id-value"} {
			if strings.Contains(out, secret) {
				t.Errorf("startup log leaked a credential value: %q in %q", secret, out)
			}
		}
	})
}

func TestWhatsAppSettingsProblemsNeverContainCredentialValues(t *testing.T) {
	for _, enabled := range []string{"", "true", "false", "TRUE", "1"} {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_ENABLED":         enabled,
			"WHATSAPP_TOKEN":           "super-secret-token-value",
			"WHATSAPP_PHONE_NUMBER_ID": "super-secret-phone-id-value",
		}))
		joined := strings.Join(settings.Problems(), " | ")
		for _, secret := range []string{"super-secret-token-value", "super-secret-phone-id-value"} {
			if strings.Contains(joined, secret) {
				t.Errorf("enabled=%q: Problems() leaked a credential value: %q in %q", enabled, secret, joined)
			}
		}
	}
}
