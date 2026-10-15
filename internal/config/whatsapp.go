package config

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coneno/logger"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
)

// platformConfigCacheTTL is how long a per-instance answer from user-management is reused
// before it is asked again. It bounds both how stale a decision can be and how often any one
// instance can drive a call to user-management.
const platformConfigCacheTTL = 60 * time.Second

// platformConfigTimeout bounds a single call to user-management: a send decision must never
// wait on a slow or unreachable dependency for longer than this.
const platformConfigTimeout = 2 * time.Second

// PlatformConfigSource is the part of user-management's client this package needs: whether an
// instance has WhatsApp turned on. umAPI.UserManagementApiClient satisfies it as it stands, so
// production code passes its gRPC client straight in.
type PlatformConfigSource interface {
	GetPlatformConfig(ctx context.Context, in *umAPI.PlatformConfigReq, opts ...grpc.CallOption) (*umAPI.PlatformConfig, error)
}

// WhatsAppSettings is the process-level WhatsApp configuration: a local kill switch and the
// Meta credentials read from the environment, plus a pointer to the cache shared by every copy
// of the value that NewWhatsAppSettings built. WhatsAppSettings is cheap to copy - copying it
// never copies the cache, every copy keeps asking the same one - and its zero value has a nil
// decision, which Enabled treats as "WhatsApp is off for every instance" rather than a nil
// pointer dereference. A process that never configures WhatsApp at all (or one that disables
// generation by configuring the zero value explicitly, as the message-scheduler does) gets that
// safe default for free.
type WhatsAppSettings struct {
	enabledSwitch string
	token         string
	phoneNumberID string
	apiVersion    string
	decision      *platformDecision
}

// platformDecision is the cache of user-management's per-instance answer, shared by every copy
// of the WhatsAppSettings value that built it.
type platformDecision struct {
	source PlatformConfigSource
	now    func() time.Time

	mu     sync.Mutex
	states map[string]*instanceState
}

// instanceState is one instance's cached answer, together with the lock that makes a stale
// entry refresh at most once per TTL window however many goroutines ask about that instance at
// once: the first caller after expiry holds this lock for the whole round trip to
// user-management, and every other caller waits for the same answer instead of starting a call
// of its own.
type instanceState struct {
	mu          sync.Mutex
	attempted   bool
	attemptedAt time.Time
	known       bool
	on          bool
}

// WhatsAppSettingsFromEnv builds a WhatsAppSettings from the process environment and source,
// user-management's client for the per-instance question. Call it once at start-up, after the
// user-management client has been connected.
func WhatsAppSettingsFromEnv(source PlatformConfigSource) WhatsAppSettings {
	return NewWhatsAppSettings(os.Getenv, source)
}

// NewWhatsAppSettings builds a WhatsAppSettings from any getenv-shaped function and a
// PlatformConfigSource. It exists so that tests, including in other packages, can build a
// settings value without reading the real environment or dialing a real user-management
// server; production code always calls WhatsAppSettingsFromEnv. A nil source means WhatsApp
// stays off for every instance, whatever the environment says - there is nothing to ask.
func NewWhatsAppSettings(getenv func(string) string, source PlatformConfigSource) WhatsAppSettings {
	settings := WhatsAppSettings{
		enabledSwitch: getenv("WHATSAPP_ENABLED"),
		token:         getenv("WHATSAPP_TOKEN"),
		phoneNumberID: getenv("WHATSAPP_PHONE_NUMBER_ID"),
		apiVersion:    getenv("WHATSAPP_API_VERSION"),
	}
	if source != nil {
		settings.decision = &platformDecision{
			source: source,
			now:    time.Now,
			states: map[string]*instanceState{},
		}
	}
	return settings
}

// withClock returns a copy of s whose cache measures the TTL window against now instead of
// time.Now, for tests in this package that need to control the passage of time without
// sleeping. It has no effect on a WhatsAppSettings built with a nil source, which never reaches
// the cache in the first place.
func (s WhatsAppSettings) withClock(now func() time.Time) WhatsAppSettings {
	if s.decision == nil {
		return s
	}
	s.decision = &platformDecision{
		source: s.decision.source,
		now:    now,
		states: s.decision.states,
	}
	return s
}

// switchAllows reports whether the local kill switch lets WhatsApp run in this process at all.
// Unset (empty) or exactly "true" follow user-management's per-instance answer; "false" is an
// explicit kill switch that turns WhatsApp off whatever user-management says; any other value
// (e.g. "TRUE", "1") is treated as off too, fail-safe, consistent with the previous rule that
// only the exact value "true" ever turned WhatsApp on.
func (s WhatsAppSettings) switchAllows() bool {
	return s.enabledSwitch == "" || s.enabledSwitch == "true"
}

// hasCredentials reports whether this process has the Meta credentials it needs to actually
// send a WhatsApp message, regardless of what the kill switch or user-management say.
func (s WhatsAppSettings) hasCredentials() bool {
	return s.token != "" && s.phoneNumberID != ""
}

// missingCredentials names, as a human-readable and-joined list, the Meta credentials this
// process is missing ("" when both are set).
func (s WhatsAppSettings) missingCredentials() string {
	var missing []string
	if s.token == "" {
		missing = append(missing, "WHATSAPP_TOKEN")
	}
	if s.phoneNumberID == "" {
		missing = append(missing, "WHATSAPP_PHONE_NUMBER_ID")
	}
	return strings.Join(missing, " and ")
}

// CanSend is the local precondition for sending WhatsApp in this process: the kill switch
// allows it and this process has its own Meta credentials. It answers no question about any
// particular instance - that is Enabled(instanceID) below, which also asks user-management -
// and exists for the moments before any instance is known, such as building this process's
// Meta client at start-up or deciding whether the scheduler's delivery runner is worth starting
// at all.
func (s WhatsAppSettings) CanSend() bool {
	return s.switchAllows() && s.hasCredentials()
}

// Enabled is the per-instance decision of whether WhatsApp is on, shared by the gRPC server and
// the scheduler: the local kill switch allows it, user-management says this instance has
// WhatsApp turned on, and this process has the Meta credentials to actually send. The
// user-management answer is cached per instance for platformConfigCacheTTL and refreshed with
// at most one upstream call per instance per window, whatever the concurrency; a failed or
// timed-out refresh falls back to the last known answer for that instance, or to off when there
// is none yet.
func (s WhatsAppSettings) Enabled(instanceID string) bool {
	if !s.switchAllows() || instanceID == "" || s.decision == nil || s.decision.source == nil {
		return false
	}
	return s.decision.enabledFor(instanceID, s.hasCredentials(), s.missingCredentials())
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
// means the configuration is coherent.
func (s WhatsAppSettings) Problems() []string {
	var problems []string

	if s.enabledSwitch != "" && s.enabledSwitch != "true" && s.enabledSwitch != "false" {
		problems = append(problems, fmt.Sprintf(
			"WHATSAPP_ENABLED is set to %q: only an unset variable or the exact value \"true\" follow user-management, any other value turns WhatsApp off here",
			s.enabledSwitch))
	}

	if s.enabledSwitch == "false" && (s.token != "" || s.phoneNumberID != "") {
		problems = append(problems, "WHATSAPP_ENABLED=false turns WhatsApp off locally: the configured WhatsApp credentials are ignored")
	}

	if s.switchAllows() {
		if missing := s.missingCredentials(); missing != "" {
			problems = append(problems, fmt.Sprintf(
				"%s not set: instances where user-management enables WhatsApp get e-mail instead", missing))
		}
	}

	return problems
}

// LogWhatsAppStartup writes one Info line stating the WhatsApp policy for process, followed by
// one Warning line per entry of s.Problems(). It never writes a credential value.
func LogWhatsAppStartup(process string, s WhatsAppSettings) {
	switch {
	case s.enabledSwitch == "false":
		logger.Info.Printf("WhatsApp turned off for %s by WHATSAPP_ENABLED=false", process)
	case s.CanSend():
		logger.Info.Printf("WhatsApp for %s follows user-management per instance", process)
	default:
		logger.Info.Printf("WhatsApp disabled for %s", process)
	}
	for _, problem := range s.Problems() {
		logger.Warning.Printf("%s", problem)
	}
}

// stateFor returns the cache entry for instanceID, creating it on first use. It holds d.mu only
// for the lookup/insert, never for the upstream call: a slow call to user-management for one
// instance must never block a concurrent decision about a different instance.
func (d *platformDecision) stateFor(instanceID string) *instanceState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.states[instanceID]
	if !ok {
		st = &instanceState{}
		d.states[instanceID] = st
	}
	return st
}

// enabledFor resolves whether user-management allows WhatsApp for instanceID right now,
// consulting the cache or, when it is missing or stale, user-management itself. It holds the
// instance's own lock for the whole refresh, so that under concurrency only one goroutine per
// instance ever crosses the network boundary in a TTL window and every warning below is logged
// at most once per instance per window. credentialsOK and missingCredentials let the "this
// process has no credentials" case share that same once-per-window slot instead of a warning on
// every call.
func (d *platformDecision) enabledFor(instanceID string, credentialsOK bool, missingCredentials string) bool {
	st := d.stateFor(instanceID)
	st.mu.Lock()
	defer st.mu.Unlock()

	now := d.now()
	if st.attempted && now.Sub(st.attemptedAt) < platformConfigCacheTTL {
		return st.known && st.on && credentialsOK
	}

	// Record the attempt before the call, not after: a slow or timed-out call must not leave
	// the window open for a second goroutine to start a call of its own once it returns.
	st.attempted = true
	st.attemptedAt = now

	ctx, cancel := context.WithTimeout(context.Background(), platformConfigTimeout)
	resp, err := d.source.GetPlatformConfig(ctx, &umAPI.PlatformConfigReq{InstanceId: instanceID})
	cancel()

	if err != nil {
		if st.known {
			logger.Warning.Printf("instance %q: could not reach user-management for its WhatsApp setting, keeping the last known answer (whatsapp %s) for now: %v", instanceID, onOffWord(st.on), err)
		} else {
			logger.Warning.Printf("instance %q: could not reach user-management for its WhatsApp setting and no previous answer is known, treating WhatsApp as off for now: %v", instanceID, err)
		}
		return st.known && st.on && credentialsOK
	}

	st.known = true
	st.on = resp.GetWhatsappEnabled()

	if st.on && !credentialsOK {
		logger.Warning.Printf("instance %q: user-management enables WhatsApp but %s is not set here: instances where user-management enables WhatsApp get e-mail instead", instanceID, missingCredentials)
	}

	return st.on && credentialsOK
}

// onOffWord renders a boolean WhatsApp answer the way the log lines above state it.
func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
