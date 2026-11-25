package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/internal/config"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// The tests below name the alerts by their literal text, not by the constants: the names are
// what the alert rules in the log stack match, so renaming one has to fail here.

// linesWith returns the lines of log in which marker is followed by a space or a colon.
func linesWith(log, marker string) []string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(marker) + `[ :]`)
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if pattern.MatchString(line) {
			lines = append(lines, line)
		}
	}
	return lines
}

// oneLineWith returns the only line of log that carries marker, and fails unless there is
// exactly one.
func oneLineWith(t *testing.T, log, marker string) string {
	t.Helper()
	lines := linesWith(log, marker)
	if len(lines) != 1 {
		t.Fatalf("want exactly one %q line, got %d:\n%s", marker, len(lines), log)
	}
	return lines[0]
}

// noLineWith fails if log carries marker.
func noLineWith(t *testing.T, log, marker string) {
	t.Helper()
	if lines := linesWith(log, marker); len(lines) > 0 {
		t.Fatalf("unexpected %q line(s):\n%s", marker, strings.Join(lines, "\n"))
	}
}

// fieldsOf reads the key=value pairs of an alert or queue line, from the marker up to the
// colon that starts the explanation, unquoting the quoted values. It also returns the
// explanation.
func fieldsOf(t *testing.T, line, marker string) (map[string]string, string) {
	t.Helper()
	start := strings.Index(line, marker)
	if start < 0 {
		t.Fatalf("no %q in %q", marker, line)
	}
	rest := strings.TrimPrefix(line[start+len(marker):], " ")
	fields := map[string]string{}
	for rest != "" && !strings.HasPrefix(rest, ":") {
		eq := strings.Index(rest, "=")
		if eq <= 0 {
			t.Fatalf("not a key=value pair at %q in %q", rest, line)
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var value string
		if strings.HasPrefix(rest, `"`) {
			quoted, err := strconv.QuotedPrefix(rest)
			if err != nil {
				t.Fatalf("bad quoted value at %q in %q: %v", rest, line, err)
			}
			value, _ = strconv.Unquote(quoted)
			rest = rest[len(quoted):]
		} else {
			// An unquoted value ends at a space, or at the colon that starts the explanation;
			// a colon inside it, as in the time of a date, is not followed by a space.
			end := len(rest)
			if i := strings.Index(rest, " "); i >= 0 {
				end = i
			}
			if i := strings.Index(rest, ": "); i >= 0 && i < end {
				end = i
			}
			value = rest[:end]
			rest = rest[end:]
		}
		if _, dup := fields[key]; dup {
			t.Fatalf("field %q twice in %q", key, line)
		}
		fields[key] = value
		rest = strings.TrimPrefix(rest, " ")
	}
	return fields, strings.TrimPrefix(strings.TrimPrefix(rest, ":"), " ")
}

// wantFields fails unless every key of want is in got with the same value.
func wantFields(t *testing.T, line string, got map[string]string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q, in %q", key, got[key], value, line)
		}
	}
}

// intField reads a numeric field.
func intField(t *testing.T, line string, fields map[string]string, key string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(fields[key], 10, 64)
	if err != nil {
		t.Fatalf("%s = %q is not a number in %q", key, fields[key], line)
	}
	return n
}

// An alert is one ERROR line: the prefix, the name, the fields in order, then the explanation.
// A value with a space, a quote or an equals sign, or an empty one, is quoted, so a parser
// reads every pair back intact. Nothing goes to the other levels.
// Fails if the alert is logged at another level, split over lines, or a value breaks the pairs.
func TestWhatsAppAlertIsOneParsableErrorLine(t *testing.T) {
	errorsLog := captureErrorLog(t)
	warnings := captureWarningLog(t)
	infos := captureLog(t, logger.Info)

	logWhatsAppAlert(alertTemplateRefused, []alertField{
		{"instance", "it"}, {"class", "rate limit"}, {"empty", ""}, {"tricky", `a="b"`}, {"count", 3},
	}, "the explanation: with a colon")

	if warnings.String() != "" || infos.String() != "" {
		t.Fatalf("an alert went to another level: warning %q, info %q", warnings.String(), infos.String())
	}
	if lines := strings.Count(strings.TrimRight(errorsLog.String(), "\n"), "\n") + 1; lines != 1 {
		t.Fatalf("want one line, got %d: %q", lines, errorsLog.String())
	}
	line := oneLineWith(t, errorsLog.String(), "WA_ALERT template_refused")
	if !strings.Contains(line, "WA_ALERT template_refused instance=it ") {
		t.Fatalf("the name and the first field do not follow the prefix: %q", line)
	}
	fields, detail := fieldsOf(t, line, "WA_ALERT template_refused")
	wantFields(t, line, fields, map[string]string{"instance": "it", "class": "rate limit", "empty": "", "tricky": `a="b"`, "count": "3"})
	if len(fields) != 5 {
		t.Fatalf("want 5 fields, got %v", fields)
	}
	if detail != "the explanation: with a colon" {
		t.Fatalf("explanation = %q", detail)
	}
}

// The runner raises runner_not_started when it does not start while WhatsApp is not turned off
// here, whatever the reason: no credentials, no interval, a WHATSAPP_ENABLED value that is not
// understood. With WHATSAPP_ENABLED=false it only warns, as before.
// Fails if the kill switch no longer silences the alert, or a missing setting no longer raises it.
func TestWhatsAppRunnerNotStartedAlert(t *testing.T) {
	credentials := map[string]string{"WHATSAPP_TOKEN": "test-token", "WHATSAPP_PHONE_NUMBER_ID": "test-phone-id"}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range credentials {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	for _, tt := range []struct {
		name      string
		env       map[string]string
		freq      int
		wantAlert bool
		mustName  string
	}{
		{"unset switch, no credentials", map[string]string{}, 30, true, "WHATSAPP_TOKEN"},
		{"switch on, no credentials", map[string]string{"WHATSAPP_ENABLED": "true"}, 30, true, "WHATSAPP_TOKEN"},
		{"switch on, no interval", with(map[string]string{"WHATSAPP_ENABLED": "true"}), 0, true, "MESSAGE_SCHEDULER_INTERVAL_WHATSAPP"},
		{"switch value not understood", with(map[string]string{"WHATSAPP_ENABLED": "TRUE"}), 30, true, "runner not started"},
		{"kill switch", with(map[string]string{"WHATSAPP_ENABLED": "false"}), 30, false, ""},
		{"kill switch, nothing configured", map[string]string{"WHATSAPP_ENABLED": "false"}, 0, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errorsLog := captureErrorLog(t)
			warnings := captureWarningLog(t)
			env := tt.env
			settings := config.NewWhatsAppSettings(func(key string) string { return env[key] }, allInstancesPlatformConfigSource(true))
			var client *waClient.WhatsAppClient
			if settings.CanSend() {
				client = waClient.NewWhatsAppClient(settings.Token(), settings.PhoneNumberID(), "")
			}

			// A runner that does not start returns at once; one that started would never return.
			runnerForOutgoingWhatsApp(nil, nil, client, tt.freq, settings.CanSend(), defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, settings)

			if !tt.wantAlert {
				noLineWith(t, errorsLog.String(), "WA_ALERT runner_not_started")
				if !strings.Contains(warnings.String(), "outgoing whatsapp runner not started") {
					t.Fatalf("the kill switch case must still warn; warnings: %q", warnings.String())
				}
				return
			}
			line := oneLineWith(t, errorsLog.String(), "WA_ALERT runner_not_started")
			if !strings.Contains(line, tt.mustName) || !strings.Contains(line, "WHATSAPP_ENABLED=false") {
				t.Fatalf("the alert must name %q and how to silence it: %q", tt.mustName, line)
			}
		})
	}
}

// A runner that starts raises no runner_not_started.
func TestWhatsAppRunnerThatStartsRaisesNoAlert(t *testing.T) {
	gdb := newSchedulerTestGlobalDB(t)
	db := newSchedulerTestDB(t)
	errorsLog := captureErrorLog(t)
	infos := captureLog(t, logger.Info)
	client := waClient.NewWhatsAppClient("test-token", "test-phone-id", "")

	// A period of one hour: the runner starts one tick and then sleeps past the test.
	go runnerForOutgoingWhatsApp(db.service, gdb, client, 3600, true, defaultWhatsAppTemplateRetryDelay, defaultWhatsAppMaxAge, testSchedulerWhatsAppSettings(true))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(infos.String(), "fetching and sending outgoing whatsapp messages...") {
		if time.Now().After(deadline) {
			t.Fatal("the runner did not start its first tick")
		}
		time.Sleep(10 * time.Millisecond)
	}
	noLineWith(t, errorsLog.String(), "WA_ALERT runner_not_started")
}

// Meta refusing the sender raises auth_stop with the status and Meta's codes; a rate limit
// raises throttle_pause with the pause it stored and the count in a row; an outage (two
// transient failures) raises neither. Each event gives one line, however many messages were
// claimed.
// Fails if the classes are mixed up, or the alert is raised per message.
func TestWhatsAppSenderSideStopsRaiseTheirAlert(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.add(t, 3, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 190, Subcode: 463}}, 60)

		line := oneLineWith(t, errorsLog.String(), "WA_ALERT auth_stop")
		fields, _ := fieldsOf(t, line, "WA_ALERT auth_stop")
		wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "status": "400", "code": "190", "subcode": "463"})
		noLineWith(t, errorsLog.String(), "WA_ALERT throttle_pause")
	})
	t.Run("rate limit", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.add(t, 3, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: throttledByMeta(0)}, 60)

		line := oneLineWith(t, errorsLog.String(), "WA_ALERT throttle_pause")
		fields, _ := fieldsOf(t, line, "WA_ALERT throttle_pause")
		notBefore, level := db.throttleState(t)
		wantFields(t, line, fields, map[string]string{
			"instance": schedulerTestInstanceID, "code": "130429", "in_a_row": fmt.Sprint(level),
			"until": time.Unix(notBefore, 0).UTC().Format(time.RFC3339),
		})
		if level != 1 {
			t.Fatalf("throttle level = %d, want 1", level)
		}
		if pause := intField(t, line, fields, "pause_s"); pause < 60 {
			t.Fatalf("pause_s = %d, want at least the 60 s lock", pause)
		}
		noLineWith(t, errorsLog.String(), "WA_ALERT auth_stop")
	})
	t.Run("outage", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.add(t, 3, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: &waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}}, 60)

		if !strings.Contains(errorsLog.String(), "two consecutive transient failures") {
			t.Fatalf("the tick did not stop on the outage: %s", errorsLog.String())
		}
		noLineWith(t, errorsLog.String(), "WA_ALERT auth_stop")
		noLineWith(t, errorsLog.String(), "WA_ALERT throttle_pause")
	})
}

// A rate limit whose pause cannot be stored still raises throttle_pause, saying it was not
// stored.
// Fails if the failure path loses the alert.
func TestWhatsAppThrottlePauseAlertWhenThePauseCannotBeStored(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 1, 0)
	collection := func(name string, actions ...string) bson.M {
		return bson.M{"resource": bson.M{"db": db.database.Name(), "collection": name}, "actions": actions}
	}
	// Everything the tick needs, except writing the throttle state.
	service := db.serviceWithPrivileges(t, "no_throttle_write", bson.A{
		collection("outgoing-whatsapp", "find", "insert", "update", "remove"),
		collection("sent-whatsapp", "find", "insert"),
		collection("whatsapp-throttle", "find"),
	})
	errorsLog := captureErrorLog(t)

	runWhatsAppHandler(service, &fakeWhatsAppSender{err: throttledByMeta(0)}, 60)

	line := oneLineWith(t, errorsLog.String(), "WA_ALERT throttle_pause")
	fields, detail := fieldsOf(t, line, "WA_ALERT throttle_pause")
	wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "code": "130429", "stored": "false"})
	if !strings.Contains(detail, "cannot be stored") {
		t.Fatalf("the alert does not say the pause was not stored: %q", line)
	}
	if notBefore, _ := db.throttleState(t); notBefore != 0 {
		t.Fatalf("the restricted user stored a pause after all (notBefore %d): the test does not test the failure", notBefore)
	}
}

// A refused template raises one template_refused for the whole template, with what the
// deferral did: how many messages wait, how many on their last attempt were archived as
// failed, and the delay. Those archived messages raise no archived_failed of their own, and a
// healthy template raises nothing.
// Fails if the counts are wrong, or the template alert is raised per message.
func TestWhatsAppRefusedTemplateRaisesOneAlert(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addWithTemplate(t, "+391111111001", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111002", brokenTemplate, "it", 0)
	db.addWithTemplate(t, "+391111111003", brokenTemplate, "it", maxWhatsAppSendAttempts-1)
	db.addWithTemplate(t, "+392222222001", healthyTemplate, "it", 0)
	sender := &templateSelectiveWhatsAppSender{failName: brokenTemplate, failLang: "it",
		err: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132015}}
	errorsLog := captureErrorLog(t)

	runWhatsAppHandlerWithTemplateDelay(db, sender, 60, 3600)

	// The queue hands out the first message queued, on its first attempt, which Meta refuses;
	// the deferral then archives the one on its last attempt and defers the other two.
	line := oneLineWith(t, errorsLog.String(), "WA_ALERT template_refused")
	fields, _ := fieldsOf(t, line, "WA_ALERT template_refused")
	wantFields(t, line, fields, map[string]string{
		"instance": schedulerTestInstanceID, "template": brokenTemplate, "lang": "it", "code": "132015",
		"retry_delay_s": "3600", "deferred": "2", "archived_failed": "1",
	})
	if queued, _ := db.counts(t); queued != 2 || len(db.queuedWithTemplate(t, brokenTemplate, "it")) != 2 {
		t.Fatalf("want the 2 deferred messages of the broken template left in the queue, got %d queued", queued)
	}
	noLineWith(t, errorsLog.String(), "WA_ALERT archived_failed")
	noLineWith(t, errorsLog.String(), "WA_ALERT permanent_failure")
}

// A permanent failure raises permanent_failure with Meta's code and what the message was, and
// no archived_failed: one event, one alert.
// Fails if the alert is dropped, or doubled by the archive.
func TestWhatsAppPermanentFailureRaisesItsAlert(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	errorsLog := captureErrorLog(t)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: permanentFailure(132001)}, 60)

	line := oneLineWith(t, errorsLog.String(), "WA_ALERT permanent_failure")
	fields, _ := fieldsOf(t, line, "WA_ALERT permanent_failure")
	wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "type": "test", "template": "test_template", "lang": "it", "code": "132001"})
	if strings.Contains(line, poisonPhone) || strings.Contains(line, types.MaskPhone(poisonPhone)) {
		t.Fatalf("the alert names the recipient: %q", line)
	}
	noLineWith(t, errorsLog.String(), "WA_ALERT archived_failed")
}

// A message that uses up its attempts raises archived_failed with the code and class of its
// last failure. A message that still has attempts left raises nothing, and neither does one
// whose archive write fails: it was not archived, it stays queued.
// Fails if the alert fires on every failure, before the archive, or not at all.
func TestWhatsAppMessageArchivedAfterItsAttemptsRaisesArchivedFailed(t *testing.T) {
	perMessage := &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131049}

	t.Run("last attempt", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addTo(t, poisonPhone, maxWhatsAppSendAttempts-1)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: perMessage}, 60)

		line := oneLineWith(t, errorsLog.String(), "WA_ALERT archived_failed")
		fields, _ := fieldsOf(t, line, "WA_ALERT archived_failed")
		wantFields(t, line, fields, map[string]string{
			"instance": schedulerTestInstanceID, "type": "test", "template": "test_template", "lang": "it",
			"code": "131049", "class": "unknown", "attempts": fmt.Sprint(maxWhatsAppSendAttempts),
		})
		if row := db.archivedFor(t, poisonPhone); row.Status != types.WhatsAppStatusFailed {
			t.Fatalf("archived as %q", row.Status)
		}
	})
	t.Run("attempts left", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addTo(t, poisonPhone, maxWhatsAppSendAttempts-2)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: perMessage}, 60)

		if got := db.attemptsOf(t, poisonPhone); got != maxWhatsAppSendAttempts-1 {
			t.Fatalf("attempts = %d, want the failure charged", got)
		}
		noLineWith(t, errorsLog.String(), "WA_ALERT archived_failed")
	})
	t.Run("archive refused", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addTo(t, poisonPhone, maxWhatsAppSendAttempts-1)
		db.blockArchive(t, poisonPhone)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{err: perMessage}, 60)

		if db.queuedFor(t, poisonPhone) != 1 {
			t.Fatal("the message left the queue although its archive write failed")
		}
		noLineWith(t, errorsLog.String(), "WA_ALERT archived_failed")
	})
}

// A tick that expires messages raises one expired alert with the count and the max age; a
// tick that expires none raises nothing.
func TestWhatsAppExpiredAlert(t *testing.T) {
	t.Run("two expired", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addAged(t, "+391111111101", testMaxAge+60, 0)
		db.addAged(t, "+391111111102", testMaxAge+120, 0)
		db.addAged(t, "+391111111103", testMaxAge-60, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandlerWithMaxAge(db, &perPhoneWhatsAppSender{}, testMaxAge)

		line := oneLineWith(t, errorsLog.String(), "WA_ALERT expired")
		fields, _ := fieldsOf(t, line, "WA_ALERT expired")
		wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "count": "2", "max_age_s": fmt.Sprint(testMaxAge)})
	})
	t.Run("none expired", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addAged(t, "+391111111103", testMaxAge-60, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandlerWithMaxAge(db, &perPhoneWhatsAppSender{}, testMaxAge)

		noLineWith(t, errorsLog.String(), "WA_ALERT expired")
	})
}

// queueLine returns the fields of the only WA_QUEUE line of a tick.
func queueLine(t *testing.T, infos string) (string, map[string]string) {
	t.Helper()
	line := oneLineWith(t, infos, "WA_QUEUE")
	fields, _ := fieldsOf(t, line, "WA_QUEUE")
	return line, fields
}

// A tick states the queue once, at its end, after every batch: one line for a tick that went
// through three fetches, and an emptied queue reads as zero with no backlog.
// Fails if the line moves into the batch loop, or is written before the tick's work.
func TestWhatsAppTickReportsTheQueueOnceAtItsEnd(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.add(t, 2*outgoingBatchSize+5, 0)
	infos := captureLog(t, logger.Info)
	errorsLog := captureErrorLog(t)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{}, 60)

	line, fields := queueLine(t, infos.String())
	wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "queued": "0", "waiting": "0", "deferred": "0", "oldest_waiting_s": "0"})
	noLineWith(t, errorsLog.String(), "WA_ALERT queue_backlog")
}

// A paused tick sends nothing and still states the queue. A message waiting more than an hour
// raises queue_backlog; an old message held back by a refused template is counted as deferred,
// never as stale, and does not date the oldest waiting message.
// Fails if the paused tick skips the report, or deferred messages count towards the backlog.
func TestWhatsAppPausedTickReportsTheQueueAndTheBacklog(t *testing.T) {
	db := newSchedulerTestDB(t)
	now := time.Now().Unix()
	if _, err := db.service.RecordWhatsAppThrottle(schedulerTestInstanceID, now+600, now); err != nil {
		t.Fatal(err)
	}
	db.addAged(t, "+391111111201", 7200, 0)
	db.addAged(t, "+391111111202", 60, 0)
	deferredRow := db.addAged(t, "+391111111203", 3*3600, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateByID(ctx, deferredRow.ID, bson.M{"$set": bson.M{"lastSendAttempt": now + 3000}}); err != nil {
		t.Fatal(err)
	}
	infos := captureLog(t, logger.Info)
	errorsLog := captureErrorLog(t)
	sender := &fakeWhatsAppSender{}

	runWhatsAppHandler(db.service, sender, 60)

	if sender.callCount() != 0 {
		t.Fatalf("a paused tick sent %d message(s)", sender.callCount())
	}
	line, fields := queueLine(t, infos.String())
	wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "queued": "3", "waiting": "2", "deferred": "1"})
	if age := intField(t, line, fields, "oldest_waiting_s"); age < 7200 || age > 7200+30 {
		t.Fatalf("oldest_waiting_s = %d, want about 7200", age)
	}

	alert := oneLineWith(t, errorsLog.String(), "WA_ALERT queue_backlog")
	alertFields, _ := fieldsOf(t, alert, "WA_ALERT queue_backlog")
	wantFields(t, alert, alertFields, map[string]string{"instance": schedulerTestInstanceID, "stale": "1", "threshold_s": "3600", "oldest_waiting_s": fields["oldest_waiting_s"]})
}

// Old messages that only wait for a refused template's retry raise no backlog: their template
// already raised its own alert.
func TestWhatsAppDeferredMessagesRaiseNoBacklog(t *testing.T) {
	db := newSchedulerTestDB(t)
	now := time.Now().Unix()
	row := db.addAged(t, "+391111111301", 3*3600, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.outgoing.UpdateByID(ctx, row.ID, bson.M{"$set": bson.M{"lastSendAttempt": now + 3000}}); err != nil {
		t.Fatal(err)
	}
	infos := captureLog(t, logger.Info)
	errorsLog := captureErrorLog(t)

	runWhatsAppHandler(db.service, &fakeWhatsAppSender{}, 60)

	line, fields := queueLine(t, infos.String())
	wantFields(t, line, fields, map[string]string{"queued": "1", "waiting": "0", "deferred": "1", "oldest_waiting_s": "0"})
	noLineWith(t, errorsLog.String(), "WA_ALERT queue_backlog")
}

// When the queue cannot be read, the tick says so and writes neither the queue line nor a
// backlog alert.
func TestWhatsAppQueueReportWhenTheQueueCannotBeRead(t *testing.T) {
	db := newSchedulerTestDB(t)
	db.addAged(t, "+391111111401", 7200, 0)
	// A user with no right on the message database: every read of the tick fails.
	service := db.serviceWithPrivileges(t, "no_read", bson.A{bson.M{
		"resource": bson.M{"db": "scheduler_unrelated", "collection": ""},
		"actions":  bson.A{"find"},
	}})
	infos := captureLog(t, logger.Info)
	errorsLog := captureErrorLog(t)

	runWhatsAppHandler(service, &fakeWhatsAppSender{}, 60)

	if !strings.Contains(errorsLog.String(), "cannot read the size of the WhatsApp queue") {
		t.Fatalf("the failed read is not logged: %s", errorsLog.String())
	}
	noLineWith(t, infos.String(), "WA_QUEUE")
	noLineWith(t, errorsLog.String(), "WA_ALERT queue_backlog")
}

// A failed archive write raises archive_write_failed, once, with the outcome it could not
// store, whether the message was delivered or given up on; the message stays queued. The alert
// carries neither the number, masked or not, nor the database error, which can quote the
// document it refused. A message archived normally raises nothing.
// Fails if the alert is dropped, fires on a successful archive, or carries the database error.
func TestWhatsAppArchiveWriteFailureRaisesItsAlert(t *testing.T) {
	for _, tt := range []struct {
		name       string
		sender     *fakeWhatsAppSender
		wantStatus string
	}{
		{"delivered", &fakeWhatsAppSender{wamid: "wamid.archive-refused"}, types.WhatsAppStatusDelivered},
		{"failed", &fakeWhatsAppSender{err: permanentFailure(132001)}, types.WhatsAppStatusFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newSchedulerTestDB(t)
			db.addTo(t, poisonPhone, 0)
			db.blockArchive(t, poisonPhone)
			errorsLog := captureErrorLog(t)

			runWhatsAppHandler(db.service, tt.sender, 60)

			line := oneLineWith(t, errorsLog.String(), "WA_ALERT archive_write_failed")
			fields, _ := fieldsOf(t, line, "WA_ALERT archive_write_failed")
			wantFields(t, line, fields, map[string]string{"instance": schedulerTestInstanceID, "type": "test", "status": tt.wantStatus})
			if strings.Contains(line, poisonPhone) || strings.Contains(line, types.MaskPhone(poisonPhone)) || strings.Contains(line, "dup key") {
				t.Fatalf("the alert carries the number or the database error: %q", line)
			}
			if db.queuedFor(t, poisonPhone) != 1 {
				t.Fatal("the message left the queue although its archive write failed")
			}
		})
	}
	t.Run("archived", func(t *testing.T) {
		db := newSchedulerTestDB(t)
		db.addTo(t, poisonPhone, 0)
		errorsLog := captureErrorLog(t)

		runWhatsAppHandler(db.service, &fakeWhatsAppSender{wamid: "wamid.archived"}, 60)

		if db.archivedRowsFor(t, poisonPhone) != 1 {
			t.Fatal("the message was not archived")
		}
		noLineWith(t, errorsLog.String(), "WA_ALERT archive_write_failed")
	})
}
