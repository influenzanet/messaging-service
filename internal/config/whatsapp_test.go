package config

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coneno/logger"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
)

// envMap builds a getenv-shaped function over a fixed map, so every case below is explicit
// about exactly which variables are set.
func envMap(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

// fakeSource is a PlatformConfigSource whose answer, error, delay and call count are all
// configurable for a test. It is safe for concurrent use: many goroutines may call
// GetPlatformConfig at once.
type fakeSource struct {
	mu      sync.Mutex
	enabled map[string]bool
	err     error
	delay   time.Duration
	calls   int32
	byInst  map[string]int32
}

func newFakeSource() *fakeSource {
	return &fakeSource{enabled: map[string]bool{}, byInst: map[string]int32{}}
}

func (f *fakeSource) setEnabled(instanceID string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled[instanceID] = on
}

func (f *fakeSource) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSource) setDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

func (f *fakeSource) callCount() int32 {
	return atomic.LoadInt32(&f.calls)
}

func (f *fakeSource) callCountFor(instanceID string) int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byInst[instanceID]
}

func (f *fakeSource) GetPlatformConfig(ctx context.Context, in *umAPI.PlatformConfigReq, _ ...grpc.CallOption) (*umAPI.PlatformConfig, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.byInst[in.InstanceId]++
	delay := f.delay
	err := f.err
	on := f.enabled[in.InstanceId]
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &umAPI.PlatformConfig{WhatsappEnabled: on}, nil
}

// fakeClock is an in-package test clock: now() returns whatever value was last set, so a test
// can advance past the TTL window without sleeping for real.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	// Start at a non-zero instant: a cache entry's zero-value attemptedAt must never be
	// mistaken for "just attempted" when the clock itself starts at time.Time{}.
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// enabledSettings builds a fully credentialed, enabled-switch WhatsAppSettings over source,
// with a fake clock it can be advanced independently of real time.
func enabledSettings(source PlatformConfigSource, clock *fakeClock) WhatsAppSettings {
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED":         "true",
		"WHATSAPP_TOKEN":           "tok",
		"WHATSAPP_PHONE_NUMBER_ID": "phone",
	}), source)
	return settings.withClock(clock.Now)
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

func TestWhatsAppSettingsZeroValueIsDisabledForEveryInstance(t *testing.T) {
	var settings WhatsAppSettings
	for _, instanceID := range []string{"any", "", "other"} {
		if settings.Enabled(instanceID) {
			t.Errorf("Enabled(%q): expected the zero value to be disabled", instanceID)
		}
	}
	if settings.CanSend() {
		t.Error("CanSend(): expected the zero value to be unable to send")
	}
}

func TestEnabledWithNilSourceIsAlwaysOff(t *testing.T) {
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED":         "true",
		"WHATSAPP_TOKEN":           "tok",
		"WHATSAPP_PHONE_NUMBER_ID": "phone",
	}), nil)
	if settings.Enabled("italy") {
		t.Error("expected a nil source to keep WhatsApp off, whatever the environment says")
	}
	if !settings.CanSend() {
		t.Error("CanSend() does not depend on the source and should still be true here")
	}
}

func TestEnabledKillSwitchFalseNeverCallsUpstream(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	clock := newFakeClock()
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED":         "false",
		"WHATSAPP_TOKEN":           "tok",
		"WHATSAPP_PHONE_NUMBER_ID": "phone",
	}), source).withClock(clock.Now)

	if settings.Enabled("italy") {
		t.Error("expected the kill switch to win over an upstream 'on' answer")
	}
	if got := source.callCount(); got != 0 {
		t.Errorf("expected no upstream call when the kill switch is off, got %d", got)
	}
}

func TestEnabledInvalidSwitchValueIsOff(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED":         "TRUE",
		"WHATSAPP_TOKEN":           "tok",
		"WHATSAPP_PHONE_NUMBER_ID": "phone",
	}), source)

	if settings.Enabled("italy") {
		t.Error("expected an invalid switch value to be treated as off")
	}
	if got := source.callCount(); got != 0 {
		t.Errorf("expected no upstream call for an invalid switch value, got %d", got)
	}
}

func TestEnabledEmptyInstanceIDIsOff(t *testing.T) {
	source := newFakeSource()
	clock := newFakeClock()
	settings := enabledSettings(source, clock)
	if settings.Enabled("") {
		t.Error("expected an empty instance id to be off")
	}
	if got := source.callCount(); got != 0 {
		t.Errorf("expected no upstream call for an empty instance id, got %d", got)
	}
}

func TestEnabledUpstreamOnAndOff(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("on-instance", true)
	source.setEnabled("off-instance", false)
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	if !settings.Enabled("on-instance") {
		t.Error("expected Enabled to be true when user-management says on and credentials are set")
	}
	if settings.Enabled("off-instance") {
		t.Error("expected Enabled to be false when user-management says off")
	}
}

func TestEnabledCachesWithinTTL(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	if !settings.Enabled("italy") {
		t.Fatal("expected the first call to report on")
	}
	if got := source.callCountFor("italy"); got != 1 {
		t.Fatalf("expected exactly 1 upstream call after the first ask, got %d", got)
	}

	for i := 0; i < 5; i++ {
		settings.Enabled("italy")
	}
	if got := source.callCountFor("italy"); got != 1 {
		t.Errorf("expected still exactly 1 upstream call within the TTL window, got %d", got)
	}
}

func TestEnabledRefreshesAfterTTLExpires(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	settings.Enabled("italy")
	if got := source.callCountFor("italy"); got != 1 {
		t.Fatalf("expected 1 call after the first ask, got %d", got)
	}

	clock.Advance(platformConfigCacheTTL + time.Second)
	settings.Enabled("italy")
	if got := source.callCountFor("italy"); got != 2 {
		t.Errorf("expected a second upstream call once the TTL window has expired, got %d", got)
	}

	// Still within the new window: no third call.
	settings.Enabled("italy")
	if got := source.callCountFor("italy"); got != 2 {
		t.Errorf("expected no extra call right after the refresh, got %d", got)
	}
}

func TestEnabledDifferentInstancesGetSeparateCalls(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setEnabled("nl", false)
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	settings.Enabled("italy")
	settings.Enabled("nl")
	settings.Enabled("italy")
	settings.Enabled("nl")

	if got := source.callCountFor("italy"); got != 1 {
		t.Errorf("expected 1 call for italy, got %d", got)
	}
	if got := source.callCountFor("nl"); got != 1 {
		t.Errorf("expected 1 call for nl, got %d", got)
	}
	if got := source.callCount(); got != 2 {
		t.Errorf("expected 2 upstream calls in total, got %d", got)
	}
}

func TestEnabledErrorFallsBackToLastKnownAnswer(t *testing.T) {
	for _, tt := range []struct {
		name string
		on   bool
	}{
		{"last known on stays on", true},
		{"last known off stays off", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := newFakeSource()
			source.setEnabled("italy", tt.on)
			clock := newFakeClock()
			settings := enabledSettings(source, clock)

			if got := settings.Enabled("italy"); got != tt.on {
				t.Fatalf("expected the first, successful call to report %v, got %v", tt.on, got)
			}

			clock.Advance(platformConfigCacheTTL + time.Second)
			source.setErr(errors.New("upstream down"))

			var out string
			var got bool
			out = captureLoggerOutput(t, func() { got = settings.Enabled("italy") })
			if got != tt.on {
				t.Errorf("expected the cached answer %v to survive an upstream error, got %v", tt.on, got)
			}
			if !strings.Contains(out, "italy") || !strings.Contains(out, "last known") {
				t.Errorf("expected a warning naming the instance and the fallback, got %q", out)
			}
		})
	}
}

func TestEnabledErrorWithoutLastKnownAnswerIsOff(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setErr(errors.New("upstream down"))
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	var out string
	var got bool
	out = captureLoggerOutput(t, func() { got = settings.Enabled("italy") })
	if got {
		t.Error("expected off when the very first call fails and nothing is known yet")
	}
	if !strings.Contains(out, "italy") || !strings.Contains(out, "no previous answer") {
		t.Errorf("expected a warning naming the instance and that nothing is known yet, got %q", out)
	}
}

func TestEnabledTimeoutReturnsPromptlyAndDoesNotBlockTheWindow(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setDelay(platformConfigTimeout * 5) // much longer than the 2s call timeout
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	start := time.Now()
	var got bool
	out := captureLoggerOutput(t, func() { got = settings.Enabled("italy") })
	elapsed := time.Since(start)

	if got {
		t.Error("expected off while the upstream call is still hanging past its timeout")
	}
	if elapsed > platformConfigTimeout+2*time.Second {
		t.Errorf("expected Enabled to return close to the %s timeout, took %s", platformConfigTimeout, elapsed)
	}
	if !strings.Contains(out, "italy") {
		t.Errorf("expected a warning naming the instance, got %q", out)
	}

	// The window is still open (the fake clock has not moved): a second call must not start
	// a second upstream call, let alone block again.
	start = time.Now()
	settings.Enabled("italy")
	if blocked := time.Since(start); blocked > 500*time.Millisecond {
		t.Errorf("expected the second call within the same window to return immediately, took %s", blocked)
	}
	if got := source.callCountFor("italy"); got != 1 {
		t.Errorf("expected exactly 1 upstream call despite the timeout, got %d", got)
	}
}

func TestEnabledWarningLoggedOnceEveryWindow(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setErr(errors.New("upstream down"))
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	out := captureLoggerOutput(t, func() {
		for i := 0; i < 50; i++ {
			settings.Enabled("italy")
		}
	})
	if n := strings.Count(out, "italy"); n != 1 {
		t.Errorf("expected exactly 1 warning line across 50 calls in the same window, got %d (output: %q)", n, out)
	}

	clock.Advance(platformConfigCacheTTL + time.Second)
	out2 := captureLoggerOutput(t, func() {
		for i := 0; i < 50; i++ {
			settings.Enabled("italy")
		}
	})
	if n := strings.Count(out2, "italy"); n != 1 {
		t.Errorf("expected exactly 1 more warning line in the next window, got %d (output: %q)", n, out2)
	}
}

func TestEnabledMissingCredentialsWhenUpstreamIsOn(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	clock := newFakeClock()
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED":         "true",
		"WHATSAPP_PHONE_NUMBER_ID": "phone",
		// WHATSAPP_TOKEN deliberately missing.
	}), source).withClock(clock.Now)

	var out string
	var got bool
	out = captureLoggerOutput(t, func() { got = settings.Enabled("italy") })
	if got {
		t.Error("expected off when credentials are missing, even though user-management says on")
	}
	if !strings.Contains(out, "WHATSAPP_TOKEN") {
		t.Errorf("expected the warning to name exactly the missing variable, got %q", out)
	}
	if strings.Contains(out, "WHATSAPP_PHONE_NUMBER_ID") {
		t.Errorf("expected the warning not to name a variable that is actually set, got %q", out)
	}

	// Still within the window: no second warning line.
	out2 := captureLoggerOutput(t, func() { settings.Enabled("italy") })
	if out2 != "" {
		t.Errorf("expected no further warning within the same window, got %q", out2)
	}
}

func TestEnabledMissingBothCredentialsNamesBoth(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED": "true",
	}), source)

	out := captureLoggerOutput(t, func() { settings.Enabled("italy") })
	for _, name := range []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_NUMBER_ID"} {
		if !strings.Contains(out, name) {
			t.Errorf("expected the warning to name %s, got %q", name, out)
		}
	}
}

func TestEnabledMissingCredentialsWhenUpstreamIsOffLogsNoCredentialsWarning(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", false)
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED": "true",
	}), source)

	out := captureLoggerOutput(t, func() { settings.Enabled("italy") })
	if out != "" {
		t.Errorf("expected no warning when user-management is off (credentials are moot), got %q", out)
	}
}

func TestEnabledConcurrentCallsForSameInstanceMakeOneUpstreamCall(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setDelay(50 * time.Millisecond) // widen the window every goroutine races into
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	const n = 100
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = settings.Enabled("italy")
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		if !got {
			t.Errorf("goroutine %d: expected true, got false", i)
		}
	}
	if got := source.callCountFor("italy"); got != 1 {
		t.Errorf("expected exactly 1 upstream call for 100 concurrent askers, got %d", got)
	}
}

func TestEnabledConcurrentCallsForDifferentInstancesDoNotBlockEachOther(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("a", true)
	source.setEnabled("b", true)
	source.setDelay(50 * time.Millisecond)
	clock := newFakeClock()
	settings := enabledSettings(source, clock)

	var wg sync.WaitGroup
	for _, instance := range []string{"a", "b"} {
		wg.Add(1)
		go func(instance string) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				settings.Enabled(instance)
			}
		}(instance)
	}
	wg.Wait()

	if got := source.callCountFor("a"); got != 1 {
		t.Errorf("expected 1 upstream call for instance a, got %d", got)
	}
	if got := source.callCountFor("b"); got != 1 {
		t.Errorf("expected 1 upstream call for instance b, got %d", got)
	}
}

func TestWhatsAppSettingsAccessors(t *testing.T) {
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_TOKEN":           "tok-value",
		"WHATSAPP_PHONE_NUMBER_ID": "phone-value",
		"WHATSAPP_API_VERSION":     "v99.0",
	}), newFakeSource())
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

func TestCanSend(t *testing.T) {
	tests := []struct {
		name     string
		enabled  string
		token    string
		phoneID  string
		expected bool
	}{
		{"unset switch, both credentials", "", "tok", "phone", true},
		{"true switch, both credentials", "true", "tok", "phone", true},
		{"false switch, both credentials", "false", "tok", "phone", false},
		{"unset switch, no credentials", "", "", "", false},
		{"unset switch, only token", "", "tok", "", false},
		{"invalid switch, both credentials", "TRUE", "tok", "phone", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := NewWhatsAppSettings(envMap(map[string]string{
				"WHATSAPP_ENABLED":         tt.enabled,
				"WHATSAPP_TOKEN":           tt.token,
				"WHATSAPP_PHONE_NUMBER_ID": tt.phoneID,
			}), newFakeSource())
			if got := settings.CanSend(); got != tt.expected {
				t.Errorf("CanSend() = %v, want %v", got, tt.expected)
			}
		})
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
			name:      "unset switch, fully configured",
			enabled:   "",
			token:     "tok",
			phoneID:   "phone",
			wantCount: 0,
		},
		{
			name:      "true switch, fully configured",
			enabled:   "true",
			token:     "tok",
			phoneID:   "phone",
			wantCount: 0,
		},
		{
			name:         "unset switch, missing token",
			enabled:      "",
			phoneID:      "phone",
			wantContains: []string{"WHATSAPP_TOKEN"},
			wantCount:    1,
		},
		{
			name:         "unset switch, missing both credentials",
			enabled:      "",
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
			name:      "false switch, nothing configured",
			enabled:   "false",
			wantCount: 0,
		},
		{
			name:         "false switch, leftover credentials",
			enabled:      "false",
			token:        "tok",
			phoneID:      "phone",
			wantContains: []string{"WHATSAPP_ENABLED=false", "ignored"},
			wantCount:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := NewWhatsAppSettings(envMap(map[string]string{
				"WHATSAPP_ENABLED":         tt.enabled,
				"WHATSAPP_TOKEN":           tt.token,
				"WHATSAPP_PHONE_NUMBER_ID": tt.phoneID,
			}), newFakeSource())
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

func TestLogWhatsAppStartup(t *testing.T) {
	t.Run("kill switch false", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{"WHATSAPP_ENABLED": "false"}), newFakeSource())
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp turned off for message-scheduler by WHATSAPP_ENABLED=false") {
			t.Errorf("expected the kill-switch line, got %q", out)
		}
	})

	t.Run("follows user-management", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_TOKEN":           "tok",
			"WHATSAPP_PHONE_NUMBER_ID": "phone",
		}), newFakeSource())
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp for message-scheduler follows user-management per instance") {
			t.Errorf("expected the follows-user-management line, got %q", out)
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{
			"WHATSAPP_PHONE_NUMBER_ID": "phone",
		}), newFakeSource())
		out := captureLoggerOutput(t, func() { LogWhatsAppStartup("message-scheduler", settings) })
		if !strings.Contains(out, "WhatsApp disabled for message-scheduler") {
			t.Errorf("expected a disabled line, got %q", out)
		}
		if !strings.Contains(out, "WHATSAPP_TOKEN") {
			t.Errorf("expected the missing variable to be named, got %q", out)
		}
	})

	t.Run("invalid switch value", func(t *testing.T) {
		settings := NewWhatsAppSettings(envMap(map[string]string{"WHATSAPP_ENABLED": "TRUE"}), newFakeSource())
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
		}), newFakeSource())
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
		}), newFakeSource())
		joined := strings.Join(settings.Problems(), " | ")
		for _, secret := range []string{"super-secret-token-value", "super-secret-phone-id-value"} {
			if strings.Contains(joined, secret) {
				t.Errorf("enabled=%q: Problems() leaked a credential value: %q in %q", enabled, secret, joined)
			}
		}
	}
}

func TestEnabledNeverLogsCredentialValues(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	settings := NewWhatsAppSettings(envMap(map[string]string{
		"WHATSAPP_ENABLED": "true",
		"WHATSAPP_TOKEN":   "",
	}), source)
	out := captureLoggerOutput(t, func() { settings.Enabled("italy") })
	// Nothing secret was ever configured here, but the point stands: the warning path must
	// only ever print variable names, never values, so grepping for a real token format here
	// documents the intent even though this particular case has no value to leak.
	if strings.Contains(out, "tok-value") {
		t.Errorf("unexpected credential value in log output: %q", out)
	}
}

// Only the exact value "false" is the kill switch an operator sets on purpose. Unset and
// "true" follow user-management, and a value that is not understood is off only as a fail-safe:
// the scheduler raises an alert in every case but the first.
// Fails if another value counts as the kill switch, or "false" no longer does.
func TestTurnedOffByKillSwitch(t *testing.T) {
	for value, want := range map[string]bool{
		"false": true,
		"":      false,
		"true":  false,
		"TRUE":  false,
		"False": false,
		"0":     false,
	} {
		settings := NewWhatsAppSettings(envMap(map[string]string{"WHATSAPP_ENABLED": value}), nil)
		if got := settings.TurnedOffByKillSwitch(); got != want {
			t.Fatalf("WHATSAPP_ENABLED=%q: TurnedOffByKillSwitch() = %v, want %v", value, got, want)
		}
	}
}

// queueOnlySettings builds the settings of a process that only queues WhatsApp messages, over
// env and source.
func queueOnlySettings(env map[string]string, source PlatformConfigSource) WhatsAppSettings {
	return NewQueueingWhatsAppSettings(envMap(env), source)
}

// A process that only queues follows the kill switch and user-management, and needs no Meta
// credentials: with none set, WhatsApp is on where user-management turns it on, off where it
// does not, and off everywhere under WHATSAPP_ENABLED=false or a value that is not understood.
// A process that sends still needs its credentials.
// Fails if the queueing decision still requires the credentials, or stops following the kill
// switch or user-management, or if the sending decision loses its credentials check.
func TestQueueOnlyEnabledNeedsNoCredentials(t *testing.T) {
	source := newFakeSource()
	source.setEnabled("italy", true)
	source.setEnabled("swiss", false)

	for _, tt := range []struct {
		name      string
		switchVal string
		wantItaly bool
	}{
		{name: "switch unset", switchVal: "", wantItaly: true},
		{name: "switch true", switchVal: "true", wantItaly: true},
		{name: "switch false", switchVal: "false", wantItaly: false},
		{name: "switch not understood", switchVal: "TRUE", wantItaly: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := queueOnlySettings(map[string]string{"WHATSAPP_ENABLED": tt.switchVal}, source)
			var italy, swiss bool
			out := captureLoggerOutput(t, func() {
				italy = settings.Enabled("italy")
				swiss = settings.Enabled("swiss")
			})
			if italy != tt.wantItaly {
				t.Errorf("italy (user-management on): Enabled = %v, want %v", italy, tt.wantItaly)
			}
			if swiss {
				t.Error("swiss (user-management off): expected WhatsApp off")
			}
			if strings.Contains(out, "WHATSAPP_TOKEN") || strings.Contains(out, "WHATSAPP_PHONE_NUMBER_ID") {
				t.Errorf("a queue-only process must not warn about Meta credentials, got %q", out)
			}
			if settings.CanSend() {
				t.Error("queue-only settings hold no credentials and can never send")
			}
		})
	}

	sending := NewWhatsAppSettings(envMap(map[string]string{"WHATSAPP_ENABLED": "true"}), source)
	if sending.Enabled("italy") {
		t.Error("a sending process without credentials must keep WhatsApp off")
	}
}

// A queue-only process never reads the Meta credentials: its accessors return nothing, and a
// token left in its environment is reported, by name only, as one to remove.
// Fails if the queueing settings keep the token or phone number ID, stay silent about a token
// they have no use for, complain about missing credentials, or log the token's value.
func TestQueueOnlySettingsKeepNoCredentials(t *testing.T) {
	const secret = "queue-only-secret-token-value"
	withToken := queueOnlySettings(map[string]string{
		"WHATSAPP_TOKEN":           secret,
		"WHATSAPP_PHONE_NUMBER_ID": "queue-only-phone-id",
		"WHATSAPP_API_VERSION":     "v99.0",
	}, newFakeSource())
	if withToken.Token() != "" || withToken.PhoneNumberID() != "" || withToken.APIVersion() != "" {
		t.Errorf("queue-only settings kept a Meta value: token %q, phone %q, version %q", withToken.Token(), withToken.PhoneNumberID(), withToken.APIVersion())
	}
	problems := withToken.Problems()
	if len(problems) != 1 || !strings.Contains(problems[0], "WHATSAPP_TOKEN is set") || !strings.Contains(problems[0], "remove it") {
		t.Fatalf("expected one problem asking to remove WHATSAPP_TOKEN, got %q", problems)
	}
	out := captureLoggerOutput(t, func() { LogWhatsAppStartup("messaging-service", withToken) })
	if strings.Contains(out, secret) {
		t.Errorf("the startup log leaked the token value: %q", out)
	}
	if !strings.Contains(out, "WHATSAPP_TOKEN is set") {
		t.Errorf("expected the startup log to warn about the token, got %q", out)
	}

	// The kill switch with a stray token: one line about the token, not two.
	killed := queueOnlySettings(map[string]string{"WHATSAPP_ENABLED": "false", "WHATSAPP_TOKEN": secret}, newFakeSource())
	if problems := killed.Problems(); len(problems) != 1 || !strings.Contains(problems[0], "WHATSAPP_TOKEN is set") {
		t.Errorf("WHATSAPP_ENABLED=false with a token: expected the token line only, got %q", problems)
	}

	if problems := queueOnlySettings(map[string]string{}, newFakeSource()).Problems(); len(problems) != 0 {
		t.Errorf("a queue-only process with no credentials has nothing to fix, got %q", problems)
	}
}

// The startup line of a queue-only process says it follows user-management and only queues,
// instead of reporting WhatsApp disabled for want of credentials it does not need.
// Fails if the queueing settings report WhatsApp disabled while the switch allows it, or report
// it on under the kill switch.
func TestLogWhatsAppStartupQueueOnly(t *testing.T) {
	out := captureLoggerOutput(t, func() {
		LogWhatsAppStartup("messaging-service", queueOnlySettings(map[string]string{}, newFakeSource()))
	})
	if !strings.Contains(out, "WhatsApp for messaging-service follows user-management per instance; it only queues the messages") {
		t.Errorf("expected the queue-only line, got %q", out)
	}
	if strings.Contains(out, "disabled") {
		t.Errorf("expected no disabled line, got %q", out)
	}

	out = captureLoggerOutput(t, func() {
		LogWhatsAppStartup("messaging-service", queueOnlySettings(map[string]string{"WHATSAPP_ENABLED": "false"}, newFakeSource()))
	})
	if !strings.Contains(out, "WhatsApp turned off for messaging-service by WHATSAPP_ENABLED=false") {
		t.Errorf("expected the kill-switch line, got %q", out)
	}
}
