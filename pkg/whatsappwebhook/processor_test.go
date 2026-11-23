package whatsappwebhook

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/google/uuid"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	"github.com/influenzanet/messaging-service/test/testenv"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	testInstance      = "italy"
	testPhoneNumberID = "1234567890"
	// The participant's number as Meta writes it, and as user-management expects it.
	senderDigits = "393331234567"
	senderE164   = "+393331234567"
)

// remover stands in for user-management's RemoveWhatsAppChannel. errs are returned by the first
// calls, one each; the later calls succeed.
type remover struct {
	mu    sync.Mutex
	errs  []error
	calls []*umAPI.RemoveWhatsAppChannelReq
	// block makes a call wait until its context ends.
	block bool
}

func (r *remover) RemoveWhatsAppChannel(ctx context.Context, in *umAPI.RemoveWhatsAppChannelReq, _ ...grpc.CallOption) (*umAPI.RemoveWhatsAppChannelResp, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in)
	var err error
	if len(r.errs) > 0 {
		err, r.errs = r.errs[0], r.errs[1:]
	}
	block := r.block
	r.mu.Unlock()
	if block {
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-time.After(2 * time.Second):
			return nil, errors.New("the call had no deadline")
		}
	}
	if err != nil {
		return nil, err
	}
	return &umAPI.RemoveWhatsAppChannelResp{ChannelRemoved: true}, nil
}

func (r *remover) taken() []*umAPI.RemoveWhatsAppChannelReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

func newArchive(t *testing.T) *messagedb.MessageDBService {
	t.Helper()
	uri := testenv.Require(t, "F04_TEST_MONGODB_URI", "the webhook tests need a test-only MongoDB")
	prefix := "webhook_" + uuid.NewString()[:8] + "_"
	service := messagedb.NewMessageDBService(types.DBConfig{URI: uri, Timeout: 30, IdleConnTimeout: 30, MaxPoolSize: 10, DBNamePrefix: prefix})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.DBClient.Database(prefix + testInstance + "_messageDB").Drop(ctx)
		_ = service.DBClient.Disconnect(ctx)
	})
	return service
}

func newTestProcessor(t *testing.T) (*Processor, *messagedb.MessageDBService, *remover) {
	t.Helper()
	archive := newArchive(t)
	r := &remover{}
	return NewProcessor(archive, r, SingleInstance(testPhoneNumberID, testInstance)), archive, r
}

func archiveSent(t *testing.T, archive *messagedb.MessageDBService, wamid, userID string) {
	t.Helper()
	msg := types.OutgoingWhatsApp{MessageType: "weekly", ToPhoneNumber: senderE164, TemplateName: "t", Lang: "it", UserID: userID, WAMID: wamid}
	if _, err := archive.AddToSentWhatsApp(testInstance, msg, types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}); err != nil {
		t.Fatal(err)
	}
}

func archived(t *testing.T, archive *messagedb.MessageDBService, wamid string) types.SentWhatsApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row types.SentWhatsApp
	if err := archive.DBClient.Database(archive.DBNamePrefix+testInstance+"_messageDB").Collection("sent-whatsapp").
		FindOne(ctx, bson.M{"wamidHash": types.WAMIDHash(wamid)}).Decode(&row); err != nil {
		t.Fatal(err)
	}
	return row
}

// Payload builders, in the shape of Meta's notifications.

type obj = map[string]any

func statusOf(wamid, st string, codes ...int) obj {
	s := obj{"id": wamid, "status": st, "timestamp": "1731900000", "recipient_id": senderDigits}
	if len(codes) > 0 {
		var errs []obj
		for _, c := range codes {
			errs = append(errs, obj{"code": c, "title": "Message undeliverable", "message": "Message undeliverable", "error_data": obj{"details": "details"}})
		}
		s["errors"] = errs
	}
	return s
}

func textFrom(from, body string) obj {
	return obj{"from": from, "id": "wamid.in", "timestamp": "1731900000", "type": "text", "text": obj{"body": body}}
}

func changeFor(phoneNumberID string, statuses []obj, messages []obj) obj {
	value := obj{"messaging_product": "whatsapp", "metadata": obj{"display_phone_number": "390000000000", "phone_number_id": phoneNumberID}}
	if statuses != nil {
		value["statuses"] = statuses
	}
	if messages != nil {
		value["messages"] = messages
		value["contacts"] = []obj{{"profile": obj{"name": "Mario Rossi"}, "wa_id": senderDigits}}
	}
	return obj{"field": "messages", "value": value}
}

func payload(t *testing.T, changes ...obj) []byte {
	t.Helper()
	body, err := json.Marshal(obj{"object": "whatsapp_business_account", "entry": []obj{{"id": "waba", "changes": changes}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func statuses(t *testing.T, s ...obj) []byte {
	return payload(t, changeFor(testPhoneNumberID, s, nil))
}

func messages(t *testing.T, m ...obj) []byte {
	return payload(t, changeFor(testPhoneNumberID, nil, m))
}

func process(t *testing.T, p *Processor, body []byte) {
	t.Helper()
	if err := p.Process(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireCalls(t *testing.T, r *remover, want ...*umAPI.RemoveWhatsAppChannelReq) {
	t.Helper()
	got := r.taken()
	if len(got) != len(want) {
		t.Fatalf("%d call(s) to RemoveWhatsAppChannel, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].String() != want[i].String() {
			t.Errorf("call %d: %v, want %v", i, got[i], want[i])
		}
	}
}

func byUser(userID string) *umAPI.RemoveWhatsAppChannelReq {
	return &umAPI.RemoveWhatsAppChannelReq{InstanceId: testInstance, Target: &umAPI.RemoveWhatsAppChannelReq_UserId{UserId: userID},
		Reason: umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNDELIVERABLE}
}

func byPhone(phone string) *umAPI.RemoveWhatsAppChannelReq {
	return &umAPI.RemoveWhatsAppChannelReq{InstanceId: testInstance, Target: &umAPI.RemoveWhatsAppChannelReq_PhoneNumber{PhoneNumber: phone},
		Reason: umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_STOP_KEYWORD}
}

func TestStatusesAreRecorded(t *testing.T) {
	p, archive, r := newTestProcessor(t)
	archiveSent(t, archive, "wamid.A", "user-a")

	process(t, p, statuses(t, statusOf("wamid.A", "read"), statusOf("wamid.A", "delivered")))

	row := archived(t, archive, "wamid.A")
	if row.MetaStatus != "read" || row.MetaStatusAt != 1731900000 {
		t.Errorf("row: metaStatus=%q metaStatusAt=%d, want read at 1731900000 (a late delivered does not undo read)", row.MetaStatus, row.MetaStatusAt)
	}
	if row.Status != types.WhatsAppStatusDelivered {
		t.Errorf("the queue outcome changed to %q", row.Status)
	}
	requireCalls(t, r)

	t.Run("a failure that is not 131026 is recorded and removes nothing", func(t *testing.T) {
		for _, c := range []struct {
			wamid string
			codes []int
		}{{"wamid.B", []int{131049}}, {"wamid.C", []int{131047}}, {"wamid.D", nil}} {
			archiveSent(t, archive, c.wamid, "user-b")
			process(t, p, statuses(t, statusOf(c.wamid, "failed", c.codes...)))
			want := 0
			if len(c.codes) > 0 {
				want = c.codes[0]
			}
			if row := archived(t, archive, c.wamid); row.MetaStatus != "failed" || row.MetaErrorCode != want {
				t.Errorf("%s: metaStatus=%q code=%d, want failed %d", c.wamid, row.MetaStatus, row.MetaErrorCode, want)
			}
		}
		requireCalls(t, r)
	})

	t.Run("a status this processing does not use changes nothing", func(t *testing.T) {
		archiveSent(t, archive, "wamid.E", "user-e")
		process(t, p, statuses(t, statusOf("wamid.E", "played"), statusOf("wamid.E", "deleted")))
		if row := archived(t, archive, "wamid.E"); row.MetaStatus != "" {
			t.Errorf("metaStatus=%q, want none", row.MetaStatus)
		}
	})
}

// realisticWAMID builds an id in the shape of Meta's: the recipient's number and a random part,
// base64-encoded, so that a test can tell whether the number leaks through the id.
func realisticWAMID(digits, random string) string {
	raw := "\x1c\x18" + string(rune(len(digits))) + digits + "\x15\x02\x00\x11\x18\x12" + random + "\x00"
	return "wamid." + base64.StdEncoding.EncodeToString([]byte(raw))
}

// Each outcome of a status has its own line, at DEBUG, naming the message by its id reference:
// a reader can tell a status recorded on a newsletter from one about a message the scheduler
// never sent, which is expected and not a fault. The id itself, which holds the number, is never
// logged.
// Fails if two outcomes share a line again, if a line loses the reference, if any of them is
// logged above DEBUG (only a failure on a known message is), or if the raw id or the number
// reaches the logs.
func TestStatusLogs(t *testing.T) {
	p, archive, r := newTestProcessor(t)
	known := realisticWAMID(senderDigits, "ABCDEF0123456789AB")
	unknown := realisticWAMID(senderDigits, "ZYXWVU9876543210ZY")
	archiveSent(t, archive, known, "user-k")
	knownRef, unknownRef := types.WAMIDLogRef(known), types.WAMIDLogRef(unknown)

	for _, c := range []struct {
		name    string
		status  obj
		debug   string
		aboveOK string
	}{
		{"recorded", statusOf(known, "delivered"),
			"wamid_ref=" + knownRef + ` status="delivered" recorded`, ""},
		{"unchanged", statusOf(known, "sent"),
			"wamid_ref=" + knownRef + ` status="sent" already recorded, or a later one`, ""},
		{"not tracked", statusOf(known, "played"),
			"wamid_ref=" + knownRef + ` status="played" is not a delivery status this service tracks, ignored`, ""},
		{"a message the scheduler did not send", statusOf(unknown, "delivered"),
			"wamid_ref=" + unknownRef + ` status="delivered" for a message the message-scheduler did not send (a verification code, a message sent outside the platform, or one already purged): nothing to record, this is expected`, ""},
		{"a failure the scheduler did not send", statusOf(unknown, "failed", 131026),
			"wamid_ref=" + unknownRef + ` status="failed" for a message the message-scheduler did not send`, ""},
		{"a failure on a known message", statusOf(known, "failed", 131049),
			"wamid_ref=" + knownRef + ` status="failed" recorded`,
			"WhatsApp status: instance=italy wamid_ref=" + knownRef + " user=user-k failed code=131049"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var above string
			debug := captureFrom(t, []*log.Logger{logger.Debug}, func() {
				above = captureFrom(t, []*log.Logger{logger.Error, logger.Warning, logger.Info}, func() {
					process(t, p, statuses(t, c.status))
				})
			})
			if !strings.Contains(debug, "WhatsApp status: instance=italy "+c.debug) {
				t.Errorf("DEBUG log %q, want %q", debug, c.debug)
			}
			if c.aboveOK == "" && above != "" {
				t.Errorf("logged above DEBUG: %q", above)
			}
			if c.aboveOK != "" && !strings.Contains(above, c.aboveOK) {
				t.Errorf("log above DEBUG %q, want %q", above, c.aboveOK)
			}
			for _, leaked := range []string{known, unknown, strings.TrimPrefix(known, "wamid.")[:16], senderDigits} {
				if strings.Contains(debug+above, leaked) {
					t.Errorf("the logs hold %q: %q", leaked, debug+above)
				}
			}
		})
	}
	requireCalls(t, r)

	t.Run("a status that cannot be recorded names its message", func(t *testing.T) {
		p, _, _ := newTestProcessor(t)
		broken := newArchive(t)
		if err := broken.DBClient.Disconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
		p.archive = broken
		logged := captureLogs(t, func() {
			_ = p.Process(context.Background(), statuses(t, statusOf(known, "read")))
		})
		if !strings.Contains(logged, "wamid_ref="+knownRef+` status="read" could not be recorded`) {
			t.Errorf("the error does not name the message: %q", logged)
		}
		if strings.Contains(logged, known) {
			t.Errorf("the error holds the raw id: %q", logged)
		}
	})

	t.Run("an undeliverable message without an account names its message", func(t *testing.T) {
		nouser := realisticWAMID(senderDigits, "NOUSER0123456789AB")
		archiveSent(t, archive, nouser, "")
		logged := captureFrom(t, []*log.Logger{logger.Warning}, func() {
			process(t, p, statuses(t, statusOf(nouser, "failed", 131026)))
		})
		if !strings.Contains(logged, "wamid_ref="+types.WAMIDLogRef(nouser)+" undeliverable message without an account") {
			t.Errorf("warning %q does not name the message", logged)
		}
		requireCalls(t, r)
	})
}

func TestUndeliverableRemovesTheChannel(t *testing.T) {
	p, archive, r := newTestProcessor(t)
	archiveSent(t, archive, "wamid.U", "user-u")
	body := statuses(t, statusOf("wamid.U", "failed", 131026))

	process(t, p, body)
	requireCalls(t, r, byUser("user-u"))
	if row := archived(t, archive, "wamid.U"); row.MetaStatus != "failed" || row.MetaErrorCode != UndeliverableErrorCode {
		t.Errorf("row: metaStatus=%q code=%d, want failed 131026", row.MetaStatus, row.MetaErrorCode)
	}

	// Meta repeats the notification: the removal is asked again (user-management changes nothing
	// the second time), so that a repeat after a failed call still gets it done.
	process(t, p, body)
	requireCalls(t, r, byUser("user-u"))

	t.Run("131026 after another error code", func(t *testing.T) {
		archiveSent(t, archive, "wamid.U2", "user-u2")
		process(t, p, statuses(t, statusOf("wamid.U2", "failed", 131000, 131026)))
		requireCalls(t, r, byUser("user-u2"))
		if row := archived(t, archive, "wamid.U2"); row.MetaErrorCode != 131000 {
			t.Errorf("code=%d, want the first one Meta listed, 131000", row.MetaErrorCode)
		}
	})

	// Verification codes are sent by user-management and never archived here, so their statuses
	// come in unknown: they change nothing and stay out of the logs above DEBUG.
	t.Run("an unknown message", func(t *testing.T) {
		logged := captureFrom(t, []*log.Logger{logger.Error, logger.Warning, logger.Info}, func() {
			process(t, p, statuses(t, statusOf("wamid.unknown", "failed", 131026), statusOf("wamid.unknown", "read")))
		})
		requireCalls(t, r)
		if logged != "" {
			t.Errorf("statuses of an unknown message logged above DEBUG: %q", logged)
		}
	})

	t.Run("a message without an account", func(t *testing.T) {
		archiveSent(t, archive, "wamid.nouser", "")
		process(t, p, statuses(t, statusOf("wamid.nouser", "failed", 131026)))
		requireCalls(t, r)
	})

	t.Run("131026 on a status that is not a failure", func(t *testing.T) {
		archiveSent(t, archive, "wamid.sent", "user-s")
		process(t, p, statuses(t, statusOf("wamid.sent", "sent", 131026)))
		requireCalls(t, r)
	})
}

func TestStopRemovesTheChannel(t *testing.T) {
	p, _, r := newTestProcessor(t)
	for _, body := range []string{"STOP", "stop", "Stop", "sToP", "  STOP  ", "\tstop\n", " STOP "} {
		t.Run(body, func(t *testing.T) {
			process(t, p, messages(t, textFrom(senderDigits, body)))
			requireCalls(t, r, byPhone(senderE164))
		})
	}

	t.Run("a sender already written with +", func(t *testing.T) {
		process(t, p, messages(t, textFrom(senderE164, "STOP")))
		requireCalls(t, r, byPhone(senderE164))
	})

	t.Run("any other text changes nothing", func(t *testing.T) {
		for _, body := range []string{"", " ", "STOP.", "STOP!", "STOP please", "please stop", "S T O P", "STOPP", "basta", "unsubscribe", "ANNULLA"} {
			process(t, p, messages(t, textFrom(senderDigits, body)))
		}
		requireCalls(t, r)
	})

	t.Run("a message that is not text changes nothing", func(t *testing.T) {
		process(t, p, messages(t,
			obj{"from": senderDigits, "type": "button", "button": obj{"text": "STOP", "payload": "STOP"}},
			obj{"from": senderDigits, "type": "interactive", "interactive": obj{"type": "button_reply", "button_reply": obj{"id": "STOP", "title": "STOP"}}},
			obj{"from": senderDigits, "type": "unsupported", "errors": []obj{{"code": 131051}}},
			obj{"from": senderDigits, "type": "image", "text": obj{"body": "STOP"}},
		))
		requireCalls(t, r)
	})

	t.Run("a sender that is not a number changes nothing", func(t *testing.T) {
		for _, from := range []string{"", "+", "39 333 1234567", "39333abc4567", "++393331234567", "1234567890123456"} {
			process(t, p, messages(t, textFrom(from, "STOP")))
		}
		requireCalls(t, r)
	})
}

func TestPayloadsNotForThisProcessing(t *testing.T) {
	p, archive, r := newTestProcessor(t)
	archiveSent(t, archive, "wamid.X", "user-x")

	if err := p.Process(context.Background(), []byte("{not json")); !errors.Is(err, ErrMalformedPayload) {
		t.Errorf("malformed payload: %v, want ErrMalformedPayload", err)
	}

	other, _ := json.Marshal(obj{"object": "page", "entry": []obj{{"changes": []obj{changeFor(testPhoneNumberID, []obj{statusOf("wamid.X", "failed", 131026)}, []obj{textFrom(senderDigits, "STOP")})}}}})
	process(t, p, other)

	wrongField := changeFor(testPhoneNumberID, []obj{statusOf("wamid.X", "failed", 131026)}, []obj{textFrom(senderDigits, "STOP")})
	wrongField["field"] = "account_update"
	process(t, p, payload(t, wrongField))

	// Another business number on the same account: not this platform's participants.
	process(t, p, payload(t, changeFor("999", []obj{statusOf("wamid.X", "failed", 131026)}, []obj{textFrom(senderDigits, "STOP")})))

	requireCalls(t, r)
	if row := archived(t, archive, "wamid.X"); row.MetaStatus != "" {
		t.Errorf("metaStatus=%q, want none", row.MetaStatus)
	}

	t.Run("every entry and change of a batch is processed", func(t *testing.T) {
		body, _ := json.Marshal(obj{"object": "whatsapp_business_account", "entry": []obj{
			{"changes": []obj{changeFor(testPhoneNumberID, []obj{statusOf("wamid.X", "delivered")}, nil)}},
			{"changes": []obj{changeFor("999", nil, []obj{textFrom("393330000001", "STOP")}), changeFor(testPhoneNumberID, nil, []obj{textFrom(senderDigits, "STOP")})}},
		}})
		process(t, p, body)
		requireCalls(t, r, byPhone(senderE164))
		if row := archived(t, archive, "wamid.X"); row.MetaStatus != "delivered" {
			t.Errorf("metaStatus=%q, want delivered", row.MetaStatus)
		}
	})
}

func TestSingleInstance(t *testing.T) {
	resolve := SingleInstance("123", "italy")
	if got, ok := resolve("123"); !ok || got != "italy" {
		t.Errorf("123: %q %v, want italy", got, ok)
	}
	for _, id := range []string{"", "1234", "12"} {
		if _, ok := resolve(id); ok {
			t.Errorf("%q resolved", id)
		}
	}
	if _, ok := SingleInstance("", "italy")(""); ok {
		t.Error("an unconfigured number id resolves an empty one")
	}
}

// Failures that may pass are returned, so that the endpoint lets Meta deliver the payload again;
// the rest of the payload is handled all the same.
func TestFailuresLeftToMetasRetry(t *testing.T) {
	t.Run("user-management unreachable, then back", func(t *testing.T) {
		p, archive, r := newTestProcessor(t)
		archiveSent(t, archive, "wamid.R", "user-r")
		r.errs = []error{status.Error(codes.Unavailable, "down"), status.Error(codes.Unavailable, "down")}
		body := payload(t, changeFor(testPhoneNumberID, []obj{statusOf("wamid.R", "failed", 131026)}, []obj{textFrom(senderDigits, "STOP")}))

		silence(t)
		if err := p.Process(context.Background(), body); status.Code(err) != codes.Unavailable {
			t.Fatalf("error %v, want the Unavailable to be returned", err)
		}
		requireCalls(t, r, byUser("user-r"), byPhone(senderE164))
		if row := archived(t, archive, "wamid.R"); row.MetaStatus != "failed" {
			t.Errorf("metaStatus=%q: the status was not recorded before the call failed", row.MetaStatus)
		}

		process(t, p, body)
		requireCalls(t, r, byUser("user-r"), byPhone(senderE164))
	})

	t.Run("user-management without the call yet is retried", func(t *testing.T) {
		p, _, r := newTestProcessor(t)
		r.errs = []error{status.Error(codes.Unimplemented, "unknown method")}
		silence(t)
		if err := p.Process(context.Background(), messages(t, textFrom(senderDigits, "STOP"))); status.Code(err) != codes.Unimplemented {
			t.Fatalf("error %v, want Unimplemented returned", err)
		}
	})

	t.Run("a call that hangs is cut by the timeout", func(t *testing.T) {
		p, _, r := newTestProcessor(t)
		r.block = true
		p.CallTimeout = 50 * time.Millisecond
		silence(t)
		if err := p.Process(context.Background(), messages(t, textFrom(senderDigits, "STOP"))); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("error %v, want DeadlineExceeded", err)
		}
	})

	t.Run("a refusal is an alert, not a retry", func(t *testing.T) {
		p, _, r := newTestProcessor(t)
		r.errs = []error{status.Error(codes.InvalidArgument, "phone not valid")}
		logged := captureLogs(t, func() {
			if err := p.Process(context.Background(), messages(t, textFrom(senderDigits, "STOP"))); err != nil {
				t.Fatalf("error %v: a refusal cannot pass, it must not be retried", err)
			}
		})
		if !strings.Contains(logged, "WA_ALERT opt_out_failed instance=italy reason=stop_keyword code=InvalidArgument") {
			t.Errorf("no alert: %q", logged)
		}
		// The alert carries no number; the line before it names the participant, masked.
		if !strings.Contains(logged, "phone="+types.MaskPhone(senderE164)+" refused, code=InvalidArgument") {
			t.Errorf("the refusal does not name the participant: %q", logged)
		}
		requireCalls(t, r, byPhone(senderE164))
	})

	t.Run("the archive unreachable", func(t *testing.T) {
		p, _, r := newTestProcessor(t)
		broken := newArchive(t)
		if err := broken.DBClient.Disconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
		p.archive = broken
		silence(t)
		err := p.Process(context.Background(), payload(t, changeFor(testPhoneNumberID, []obj{statusOf("wamid.Z", "failed", 131026)}, []obj{textFrom(senderDigits, "STOP")})))
		if err == nil || errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("error %v, want the database error returned", err)
		}
		// The STOP in the same payload is still handled.
		requireCalls(t, r, byPhone(senderE164))
	})
}

// What participants write is never kept: not in the logs, not in the database. Numbers appear
// masked only, and the sender's profile name not at all.
func TestNothingOfTheMessageIsKept(t *testing.T) {
	p, archive, r := newTestProcessor(t)
	archiveSent(t, archive, "wamid.L", "user-l")
	database := archive.DBClient.Database(archive.DBNamePrefix + testInstance + "_messageDB")
	before := snapshot(t, database)

	secret := "ho la febbre a 39 e la tosse"
	logged := captureLogs(t, func() {
		process(t, p, payload(t, changeFor(testPhoneNumberID,
			[]obj{statusOf("wamid.L", "failed", 131026)},
			[]obj{textFrom(senderDigits, secret), textFrom(senderDigits, " stop "), textFrom("393339999999", "STOP")})))
		r.errs = []error{status.Error(codes.InvalidArgument, "x"), status.Error(codes.Unavailable, "y")}
		_ = p.Process(context.Background(), messages(t, textFrom(senderDigits, "STOP"), textFrom(senderDigits, "STOP")))
	})
	r.taken()

	for _, leaked := range []string{secret, "febbre", "Mario Rossi", senderDigits, "393339999999"} {
		if strings.Contains(logged, leaked) {
			t.Errorf("the logs carry %q: %q", leaked, logged)
		}
	}
	if !strings.Contains(logged, "phone="+types.MaskPhone(senderE164)) {
		t.Errorf("the logs do not name the masked sender: %q", logged)
	}

	if after := snapshot(t, database); after != before {
		t.Errorf("the database changed beyond the status of the archived row:\n before %s\n after  %s", before, after)
	}
}

// snapshot describes every collection and document of the database, as text, leaving out the
// delivery status fields of the archive.
func snapshot(t *testing.T, database *mongo.Database) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	names, err := database.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		cur, err := database.Collection(name).Find(ctx, bson.M{},
			options.Find().SetSort(bson.M{"_id": 1}).SetProjection(bson.M{"metaStatus": 0, "metaStatusAt": 0, "metaErrorCode": 0}))
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		if err := cur.All(ctx, &docs); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s:%v;", name, docs)
	}
	return out.String()
}

func silence(t *testing.T) {
	t.Helper()
	captureLogs(t, nil)
}

var logMu sync.Mutex

// captureLogs runs fn with every logger writing to a buffer and returns what was written. With
// a nil fn, the loggers stay redirected (and discarded) until the test ends.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	return captureFrom(t, []*log.Logger{logger.Error, logger.Warning, logger.Info, logger.Debug}, fn)
}

// captureFrom is captureLogs for the given loggers only.
func captureFrom(t *testing.T, loggers []*log.Logger, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	w := &lockedWriter{buf: &buf}
	originals := make([]io.Writer, len(loggers))
	restore := func() {
		for i, l := range loggers {
			l.SetOutput(originals[i])
		}
	}
	for i, l := range loggers {
		originals[i] = l.Writer()
		l.SetOutput(w)
	}
	if fn == nil {
		t.Cleanup(restore)
		return ""
	}
	defer restore()
	fn()
	return w.String()
}

type lockedWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
