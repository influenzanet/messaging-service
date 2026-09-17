package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// slowMongoProxy forwards the MongoDB wire protocol and holds back the commands that claim a
// message, so that the claim loop of FetchOutgoingWhatsApp can be made to outlive the lock it
// works under without waiting for a real overload. Only the claim command is delayed: the
// handshake and the heartbeats travel at full speed, so the driver keeps the connection.
type slowMongoProxy struct {
	listener net.Listener
	target   string
	delay    time.Duration
}

func newSlowMongoProxy(t *testing.T, target string, delay time.Duration) *slowMongoProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the slow MongoDB proxy: %v", err)
	}
	proxy := &slowMongoProxy{listener: listener, target: target, delay: delay}
	go proxy.serve()
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Logf("close the slow MongoDB proxy: %v", err)
		}
	})
	return proxy
}

func (p *slowMongoProxy) address() string { return p.listener.Addr().String() }

func (p *slowMongoProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.forward(client)
	}
}

func (p *slowMongoProxy) forward(client net.Conn) {
	defer client.Close()
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer server.Close()
	go func() { _, _ = io.Copy(client, server) }()

	buffer := make([]byte, 64*1024)
	for {
		read, readErr := client.Read(buffer)
		if read > 0 {
			if bytes.Contains(buffer[:read], []byte("findAndModify")) {
				time.Sleep(p.delay)
			}
			if _, writeErr := server.Write(buffer[:read]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

// throughProxy opens a second database service on the same data, reached through a proxy that
// delays every claim. Its own generous timeout keeps the delayed loop from being cut short by
// the context instead of by the condition under test.
func (db *schedulerTestDB) throughProxy(t *testing.T, delay time.Duration) *messagedb.MessageDBService {
	t.Helper()
	parsed, err := url.Parse(os.Getenv("F04_TEST_MONGODB_URI"))
	if err != nil {
		t.Fatalf("parse F04_TEST_MONGODB_URI: %v", err)
	}
	proxy := newSlowMongoProxy(t, parsed.Host, delay)
	parsed.Host = proxy.address()

	service := messagedb.NewMessageDBService(types.DBConfig{
		URI:             parsed.String(),
		Timeout:         60,
		IdleConnTimeout: 30,
		MaxPoolSize:     10,
		DBNamePrefix:    db.prefix,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.DBClient.Disconnect(ctx); err != nil {
			t.Errorf("disconnect the proxied test database: %v", err)
		}
	})
	return service
}

// A claim loop that lasts longer than the lock must not hand out a message it has already
// claimed in the same call: the caller would send and archive one message once per copy.
// Fails if the filter and the update of FetchOutgoingWhatsApp read the clock again on every
// iteration, so that a message claimed at the start of the loop looks expired before the end.
func TestWhatsAppClaimLoopDoesNotRehandAMessageItHasAlreadyClaimed(t *testing.T) {
	const lockSeconds = 1
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	// Each claim is held back for longer than the lock, so a loop of three claims spans
	// several lock lifetimes.
	slow := db.throughProxy(t, 2500*time.Millisecond)

	messages, err := slow.FetchOutgoingWhatsApp(schedulerTestInstanceID, 3, lockSeconds)
	if err != nil {
		t.Fatalf("fetch through the slow proxy: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("the claim loop handed the same message out %d times in one batch, want 1", len(messages))
	}
}

// A send must not start when the claim cannot cover it: the client waits up to the send
// timeout, and a claim that expires meanwhile lets another tick hand the same message to Meta.
// Fails if the batch guard is measured from after the claim loop instead of from before it, or
// if it ignores how long a send may take.
func TestWhatsAppSendIsSkippedWhenTheClaimCannotCoverIt(t *testing.T) {
	// Lock 35 s: the batch limit is 31 s but a 30 s send leaves a window of 5 s.
	const lockSeconds = 35
	const slowRecipient = "+391111111101"
	const nextRecipient = "+391111111102"
	db := newSchedulerTestDB(t)
	db.addTo(t, slowRecipient, 0)
	db.addTo(t, nextRecipient, 0)
	sender := &perPhoneWhatsAppSender{
		slowPhone: slowRecipient,
		delay:     6 * time.Second,
	}

	runWhatsAppHandler(db.service, sender, lockSeconds)

	if sender.calls[nextRecipient] != 0 {
		t.Fatalf("a message was sent %d times although its claim could not cover the send", sender.calls[nextRecipient])
	}
	if queued := db.countQueued(t, bson.M{"toPhoneNumber": nextRecipient}); queued != 1 {
		t.Fatalf("the skipped message left the queue: %d rows, want 1", queued)
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": nextRecipient, "sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("the skipped message was charged an attempt")
	}
	if claimed := db.countQueued(t, bson.M{"toPhoneNumber": nextRecipient, "lastSendAttempt": bson.M{"$ne": int64(0)}}); claimed != 1 {
		t.Fatalf("the skipped message lost its claim, so the next tick cannot tell it apart")
	}
	if sent := db.countQueued(t, bson.M{"toPhoneNumber": slowRecipient}); sent != 0 {
		t.Fatalf("the slow message was not delivered: %d rows left in the queue", sent)
	}
}

// The send window is the stricter of the two limits, and never disappears: a lock shorter than
// the send timeout offers no window at all, and falls back to the batch limit rather than
// refusing every send and stopping delivery.
func TestWhatsAppSendWindowIsTheStricterOfTheTwoLimits(t *testing.T) {
	const timeout = 30 * time.Second
	for _, tt := range []struct {
		name string
		lock int64
		want int64
	}{
		{name: "lock shorter than the send timeout keeps the batch limit", lock: 2, want: 1},
		{name: "lock equal to the send timeout keeps the batch limit", lock: 30, want: 27},
		{name: "staging lock of 75 s leaves 45 s", lock: 75, want: 45},
		{name: "lock of 60 s leaves 30 s", lock: 60, want: 30},
		{name: "long lock is bounded by the batch limit", lock: 400, want: 360},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := whatsAppSendWindow(tt.lock, timeout); got != tt.want {
				t.Fatalf("whatsAppSendWindow(%d) = %d, want %d", tt.lock, got, tt.want)
			}
		})
	}

	// The scheduler computes the window from the WhatsApp client's own HTTP timeout: a change
	// to that timeout has to be seen here, not silently widen the window.
	if got := whatsAppSendWindow(75, whatsAppSendTimeout); got != 45 {
		t.Fatalf("with the client's HTTP timeout the staging lock of 75 s leaves %d s, want 45", got)
	}
}

// An interval whose lock cannot cover a send is a configuration an operator has to hear about,
// together with the interval that would fix it.
func TestWhatsAppLockProblemNamesTheShortestSafeInterval(t *testing.T) {
	const timeout = 30 * time.Second
	if problem := whatsAppLockProblem(13, timeout); problem != "" {
		t.Fatalf("an interval of 13 s gives a lock of %d s and must not be reported: %s", getThreadLockInterval(13), problem)
	}
	problem := whatsAppLockProblem(12, timeout)
	if problem == "" {
		t.Fatalf("an interval of 12 s gives a lock of %d s, which cannot cover a 30 s send", getThreadLockInterval(12))
	}
	if !strings.Contains(problem, "at least 13 s") {
		t.Fatalf("the warning does not name the shortest safe interval: %s", problem)
	}
}

// When claiming the batch alone takes longer than the send window, every message of that batch
// fails the guard, nothing is sent, charged or removed, and the next fetch of the same tick
// claims the very same rows again. The tick has to end instead of turning into a loop that
// never returns, never releases the wait group and stops WhatsApp delivery for the instance
// while the runner keeps adding one stuck tick per period.
// Fails if the guard only skips the message instead of ending a batch that cannot make any
// progress at all.
func TestWhatsAppTickEndsWhenClaimingTheBatchOutlivesTheWindow(t *testing.T) {
	// Lock 2 s, so the send window is 1 s; each claim is held back for 1.5 s, so the fetch
	// alone outlives the window.
	const lockSeconds = 2
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	slow := db.throughProxy(t, 1500*time.Millisecond)
	sender := &fakeWhatsAppSender{}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runWhatsAppHandler(slow, sender, lockSeconds)
	}()

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the tick never returned: claiming the batch outlived the send window and the loop kept re-claiming the same rows")
	}

	if sender.callCount() != 0 {
		t.Fatalf("a message was sent %d times although claiming its batch had already spent the window", sender.callCount())
	}
	if queued := db.countQueued(t, bson.M{"toPhoneNumber": poisonPhone}); queued != 1 {
		t.Fatalf("the message left the queue: %d rows, want 1", queued)
	}
	if charged := db.countQueued(t, bson.M{"toPhoneNumber": poisonPhone, "sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("a message nobody tried to send was charged an attempt")
	}
}

// delayingWhatsAppSender answers every message the same way after a fixed delay, so that a
// pass over the queue takes a known amount of time.
type delayingWhatsAppSender struct {
	delay time.Duration
	err   error

	mu    sync.Mutex
	calls int
}

func (s *delayingWhatsAppSender) SendTemplateMessage(context.Context, string, string, string, map[string]string) error {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *delayingWhatsAppSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// safeBuffer collects log output written from the goroutines of a tick.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog redirects one of the loggers for the duration of a test, so that a line an
// operator is meant to see can be asserted and not only inferred from the database.
func captureLog(t *testing.T, target *log.Logger) *safeBuffer {
	t.Helper()
	buffer := &safeBuffer{}
	target.SetOutput(buffer)
	t.Cleanup(func() { target.SetOutput(os.Stderr) })
	return buffer
}

func captureErrorLog(t *testing.T) *safeBuffer { return captureLog(t, logger.Error) }

func captureWarningLog(t *testing.T) *safeBuffer { return captureLog(t, logger.Warning) }

// A queue that keeps answering with the per-recipient limit charges no attempt, deletes
// nothing and keeps every claim. Once one pass over the queue outlives the lock, the fetch
// hands back the rows claimed at the start of that pass, and the tick would go round for ever
// sending the same messages: it has to stop as soon as a fetch returns a message it has
// already processed. The claim guard cannot catch this on its own, because every fetch is
// fast and each batch starts with a fresh claim.
// Fails if the tick does not keep track of what it has already handled in this pass.
func TestWhatsAppTickStopsWhenAFetchHandsBackAMessageItAlreadyProcessed(t *testing.T) {
	const lockSeconds = 2
	// A claim written in second S is only eligible again once the clock reads S+3, so one pass
	// over the queue has to last longer than that for the rows claimed at its start to come
	// back while the pass is still running. At 25 ms a send and 20 messages a batch, 200
	// messages take about five seconds, and a single batch still stays inside the send window.
	const queued = 200
	db := newSchedulerTestDB(t)
	db.add(t, queued, 0)
	sender := &delayingWhatsAppSender{
		delay: 25 * time.Millisecond,
		err:   &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131056},
	}
	logged := captureErrorLog(t)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runWhatsAppHandler(db.service, sender, lockSeconds)
	}()

	select {
	case <-returned:
	case <-time.After(15 * time.Second):
		t.Fatalf("the tick never returned: %d sends and counting on a queue of %d recipient-limited messages", sender.callCount(), queued)
	}

	if !strings.Contains(logged.String(), "already") {
		t.Fatalf("the tick stopped without telling an operator why: %q", logged.String())
	}
	if outgoing, sent := db.counts(t); outgoing != queued || sent != 0 {
		t.Fatalf("the recipient-limited queue changed: outgoing=%d sent=%d, want %d/0", outgoing, sent, queued)
	}
	if charged := db.countQueued(t, bson.M{"sendAttempt": bson.M{"$ne": 0}}); charged != 0 {
		t.Fatalf("%d recipient-limited messages were charged an attempt", charged)
	}
}

// A row the claim guard skipped was never sent: it keeps its claim and its attempts, and the
// fetch that returns it once the pass has outlived the lock is a legitimate one. Stopping the
// tick on it would leave a healthy queue undelivered, batch after batch, at exactly the send
// latency where the guard starts skipping.
// Fails if a skipped row counts as one the tick has handled.
func TestWhatsAppSkippedRowsDoNotStopTheTick(t *testing.T) {
	const lockSeconds = 2
	const queued = 100
	db := newSchedulerTestDB(t)
	db.add(t, queued, 0)
	// 150 ms a send with a window of 1 s: the guard skips the tail of every batch, and a pass
	// over the queue outlives the lock, so the skipped rows come back within the same tick.
	sender := &delayingWhatsAppSender{delay: 150 * time.Millisecond}
	logged := captureErrorLog(t)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runWhatsAppHandler(db.service, sender, lockSeconds)
	}()

	select {
	case <-returned:
	case <-time.After(120 * time.Second):
		t.Fatalf("the tick never returned: %d sends and counting", sender.callCount())
	}

	if strings.Contains(logged.String(), "stopping the WhatsApp tick") {
		t.Fatalf("a healthy drain was stopped: %q", logged.String())
	}
	if sender.callCount() != queued {
		t.Fatalf("the tick delivered %d of %d messages", sender.callCount(), queued)
	}
	if outgoing, sent := db.counts(t); outgoing != 0 || sent != queued {
		t.Fatalf("the queue was not drained: outgoing=%d sent=%d, want 0/%d", outgoing, sent, queued)
	}
}

// A transient failure is held until another message answers, but the message it belongs to
// keeps only the claim it was fetched under. When the answer comes back so late that the claim
// may already have expired, the held failure must be left uncharged: the message may have been
// claimed again elsewhere, and charging this copy could archive it as failed while the other
// one is being delivered. The failure that came back late is charged as usual.
// Fails if the age of the held failure stops being checked before it is charged.
func TestWhatsAppHeldFailureIsLeftUnchargedWhenTheAnswerComesBackTooLate(t *testing.T) {
	const lockSeconds = 2
	const late = "+391111111101"
	db := newSchedulerTestDB(t)
	db.addTo(t, poisonPhone, 0)
	db.addTo(t, late, 0)
	sender := &perPhoneWhatsAppSender{
		script: map[string][]error{
			// held: nothing decides it until the message behind it answers
			poisonPhone: {&waClient.WhatsAppSendError{StatusCode: http.StatusServiceUnavailable}},
			// a failure of its own, but answered so late that the held claim is past nine
			// tenths of the lock by the time it decides anything
			late: {&waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 132012}},
		},
		slowPhone: late,
		delay:     2500 * time.Millisecond,
	}
	logged := captureWarningLog(t)

	runWhatsAppHandler(db.service, sender, lockSeconds)

	if !strings.Contains(logged.String(), "held too long to be decided") {
		t.Fatalf("the held failure was decided without a word about its age: %q", logged.String())
	}
	if attempts := db.attemptsOf(t, poisonPhone); attempts != 0 {
		t.Fatalf("the held failure was charged %d attempts although its claim may have expired", attempts)
	}
	if attempts := db.attemptsOf(t, late); attempts != 1 {
		t.Fatalf("the late failure was charged %d attempts, want 1", attempts)
	}
}
