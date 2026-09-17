package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
)

func TestShouldRunOutgoingWhatsApp(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		freq          int
		hasClient     bool
		wantRun       bool
		reasonMustSay string
	}{
		{
			name:      "everything configured",
			enabled:   true,
			freq:      60,
			hasClient: true,
			wantRun:   true,
		},
		{
			name:          "disabled wins over a configured period and client",
			enabled:       false,
			freq:          60,
			hasClient:     true,
			wantRun:       false,
			reasonMustSay: "WHATSAPP_ENABLED",
		},
		{
			name:          "disabled with nothing else configured",
			enabled:       false,
			freq:          0,
			hasClient:     false,
			wantRun:       false,
			reasonMustSay: "WHATSAPP_ENABLED",
		},
		{
			name:          "no period",
			enabled:       true,
			freq:          0,
			hasClient:     true,
			wantRun:       false,
			reasonMustSay: "MESSAGE_SCHEDULER_INTERVAL_WHATSAPP",
		},
		{
			name:          "negative period",
			enabled:       true,
			freq:          -1,
			hasClient:     true,
			wantRun:       false,
			reasonMustSay: "MESSAGE_SCHEDULER_INTERVAL_WHATSAPP",
		},
		{
			name:          "no client",
			enabled:       true,
			freq:          60,
			hasClient:     false,
			wantRun:       false,
			reasonMustSay: "WHATSAPP_TOKEN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, reason := shouldRunOutgoingWhatsApp(tt.enabled, tt.freq, tt.hasClient)
			if run != tt.wantRun {
				t.Fatalf("shouldRunOutgoingWhatsApp(%v, %d, %v) = %v, want %v", tt.enabled, tt.freq, tt.hasClient, run, tt.wantRun)
			}
			if tt.wantRun {
				if reason != "" {
					t.Fatalf("expected no reason when the runner starts, got %q", reason)
				}
				return
			}
			if !strings.Contains(reason, tt.reasonMustSay) {
				t.Fatalf("reason %q does not name %q", reason, tt.reasonMustSay)
			}
		})
	}
}

// newSchedulerTestGlobalDB connects to the test MongoDB with a prefix of its own, so that a
// runner that wrongly starts reads an empty instance list instead of dereferencing nil.
func newSchedulerTestGlobalDB(t *testing.T) *globaldb.GlobalDBService {
	t.Helper()
	uri := os.Getenv("F04_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("F04_TEST_MONGODB_URI is not configured")
	}

	service := globaldb.NewGlobalDBService(types.DBConfig{
		URI:             uri,
		Timeout:         5,
		IdleConnTimeout: 30,
		MaxPoolSize:     10,
		DBNamePrefix:    "f04_scheduler_" + uuid.NewString() + "_",
	})

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.DBClient.Disconnect(ctx); err != nil {
			t.Errorf("disconnect scheduler test global database: %v", err)
		}
	})

	return service
}

func TestOutgoingWhatsAppRunnerIsNotStartedWhenWhatsAppIsDisabled(t *testing.T) {
	gdb := newSchedulerTestGlobalDB(t)
	db := newSchedulerTestDB(t)
	client := waClient.NewWhatsAppClient("test-token", "test-phone-id", "")
	if client == nil {
		t.Fatal("expected a WhatsApp client for this test")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A period of one hour: a runner that enters its loop does not return in time.
		runnerForOutgoingWhatsApp(db.service, gdb, client, 3600, false)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("outgoing WhatsApp runner started although WhatsApp is disabled")
	}
}

func TestCheckWhatsAppConfig(t *testing.T) {
	tests := []struct {
		name              string
		generationEnabled bool
		clientConfigured  bool
		interval          int
		wantGenerate      bool
		wantProblems      []string
	}{
		{
			name:              "generation off, nothing configured",
			generationEnabled: false,
			clientConfigured:  false,
			interval:          0,
			wantGenerate:      false,
		},
		{
			name:              "generation off, delivery configured",
			generationEnabled: false,
			clientConfigured:  true,
			interval:          60,
			wantGenerate:      false,
		},
		{
			name:              "generation on, delivery configured",
			generationEnabled: true,
			clientConfigured:  true,
			interval:          60,
			wantGenerate:      true,
		},
		{
			name:              "generation on, no credentials",
			generationEnabled: true,
			clientConfigured:  false,
			interval:          60,
			wantGenerate:      false,
			wantProblems:      []string{"WHATSAPP_TOKEN"},
		},
		{
			name:              "generation on, no send interval",
			generationEnabled: true,
			clientConfigured:  true,
			interval:          0,
			wantGenerate:      false,
			wantProblems:      []string{"MESSAGE_SCHEDULER_INTERVAL_WHATSAPP"},
		},
		{
			name:              "generation on, nothing configured for delivery",
			generationEnabled: true,
			clientConfigured:  false,
			interval:          -1,
			wantGenerate:      false,
			wantProblems:      []string{"WHATSAPP_TOKEN", "MESSAGE_SCHEDULER_INTERVAL_WHATSAPP"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generate, problems := checkWhatsAppConfig(tt.generationEnabled, tt.clientConfigured, tt.interval)
			if generate != tt.wantGenerate {
				t.Fatalf("checkWhatsAppConfig(%v, %v, %d) generate = %v, want %v", tt.generationEnabled, tt.clientConfigured, tt.interval, generate, tt.wantGenerate)
			}
			if len(problems) != len(tt.wantProblems) {
				t.Fatalf("got %d problems %q, want %d", len(problems), problems, len(tt.wantProblems))
			}
			for i, want := range tt.wantProblems {
				if !strings.Contains(problems[i], want) {
					t.Fatalf("problem %d %q does not name %q", i, problems[i], want)
				}
			}
		})
	}
}

func TestCheckWhatsAppConfigNamesBothCredentialVariables(t *testing.T) {
	_, problems := checkWhatsAppConfig(true, false, 60)
	if len(problems) != 1 {
		t.Fatalf("expected one problem, got %q", problems)
	}
	for _, name := range []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_NUMBER_ID"} {
		if !strings.Contains(problems[0], name) {
			t.Fatalf("problem %q does not name %q", problems[0], name)
		}
	}
}
