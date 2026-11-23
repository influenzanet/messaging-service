package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// idPerRecipientSender answers each recipient as scripted: Meta's refusal when there is an
// error for it, otherwise an acceptance with the id given for it, or with none.
type idPerRecipientSender struct {
	mu     sync.Mutex
	ids    map[string]string
	errors map[string]error
}

func (s *idPerRecipientSender) SendTemplateMessage(_ context.Context, to string, _ string, _ string, _ map[string]string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errors[to]; err != nil {
		return "", err
	}
	return s.ids[to], nil
}

// rawArchivedFor reads a recipient's archive row as stored, so that a field can be told
// missing from empty.
func (db *schedulerTestDB) rawArchivedFor(t *testing.T, phone string) bson.M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row bson.M
	if err := db.sent.FindOne(ctx, bson.M{"toPhoneNumber": types.MaskPhone(phone)}).Decode(&row); err != nil {
		t.Fatalf("read the archived WhatsApp message to %s: %v", phone, err)
	}
	return row
}

// Each message Meta accepts is archived with the hash of the id Meta gave it, the key its
// delivery statuses are matched on later; the id itself, which holds the recipient's number,
// is not archived. A message Meta refused, or whose acceptance named no id, is archived with
// no hash field at all: an empty one would make every such row match a status that carries
// an empty id, and fill the index with them.
// Fails if the hash is not stored, if one message's hash lands on another's row, if the raw id
// is archived, or if a row without an id gets an empty hash.
func TestWhatsAppArchiveKeepsTheIDMetaGave(t *testing.T) {
	const (
		first   = "+391111111101"
		second  = "+391111111102"
		noID    = "+391111111103"
		refused = "+391111111104"
	)
	db := newSchedulerTestDB(t)
	for _, phone := range []string{first, second, noID, refused} {
		db.addTo(t, phone, 0)
	}
	sender := &idPerRecipientSender{
		ids: map[string]string{first: "wamid.first", second: "wamid.second"},
		errors: map[string]error{
			refused: &waClient.WhatsAppSendError{StatusCode: http.StatusBadRequest, Code: 131026},
		},
	}

	runWhatsAppHandler(db.service, sender, 60)

	if outgoing, sent := db.counts(t); outgoing != 0 || sent != 4 {
		t.Fatalf("queue after the tick: outgoing=%d sent=%d, want 0/4", outgoing, sent)
	}
	for phone, want := range map[string]string{first: "wamid.first", second: "wamid.second"} {
		row := db.archivedFor(t, phone)
		if row.Status != types.WhatsAppStatusDelivered || row.WAMIDHash != types.WAMIDHash(want) {
			t.Errorf("%s archived as %q with wamidHash %q, want delivered with the hash of %q", phone, row.Status, row.WAMIDHash, want)
		}
		if wamid, ok := db.rawArchivedFor(t, phone)["wamid"]; ok {
			t.Errorf("%s archived with the raw id %v", phone, wamid)
		}
	}
	for phone, wantStatus := range map[string]string{noID: types.WhatsAppStatusDelivered, refused: types.WhatsAppStatusFailed} {
		row := db.rawArchivedFor(t, phone)
		if row["status"] != wantStatus {
			t.Errorf("%s archived as %v, want %q", phone, row["status"], wantStatus)
		}
		for _, field := range []string{"wamid", "wamidHash"} {
			if value, ok := row[field]; ok {
				t.Errorf("%s (%s) archived with a %s field %q, want none", phone, wantStatus, field, value)
			}
		}
	}
}
