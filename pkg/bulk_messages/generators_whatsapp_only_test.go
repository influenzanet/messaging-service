package bulk_messages

// A participant who chose WhatsApp only (preferred_channels == ["whatsapp"]) must get the
// WhatsApp message and no e-mail, in each of the three bulk generators. The e-mail is still
// the fallback when the WhatsApp message cannot be prepared. These tests reuse the helpers of
// generators_whatsapp_fallback_test.go, but with a working outgoing-whatsapp collection. They
// need MESSAGE_DB_CONNECTION_STR and are skipped otherwise, and must not run in parallel
// because the log capture swaps a package-level writer.

import (
	"context"
	"sync"
	"testing"

	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"go.uber.org/mock/gomock"
)

// whatsAppOnlyTemplate is the fallback test template, without a WhatsApp template when
// withWhatsApp is false (so the WhatsApp message cannot be prepared).
func whatsAppOnlyTemplate(studyKey string, withWhatsApp bool) types.EmailTemplate {
	tpl := fallbackTemplate(studyKey)
	if !withWhatsApp {
		tpl.WhatsAppTemplateName = ""
	}
	return tpl
}

func insertWhatsAppOnlyTemplate(t *testing.T, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
	t.Helper()
	if _, err := db.DBClient.Database(db.DBNamePrefix+instanceID+"_messageDB").
		Collection("email-templates").InsertOne(context.Background(), whatsAppOnlyTemplate(fallbackStudy, withWhatsApp)); err != nil {
		t.Fatalf("insert template: %v", err)
	}
}

type whatsAppOnlyGenerator struct {
	name string
	run  func(t *testing.T, clients fallbackClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool)
}

var whatsAppOnlyGenerators = []whatsAppOnlyGenerator{
	{
		name: "GenerateForAllUsers",
		run: func(t *testing.T, clients fallbackClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			GenerateForAllUsers(clients.api, db, instanceID, whatsAppOnlyTemplate("", withWhatsApp), true, "whatsapp only test")
		},
	},
	{
		name: "GenerateForStudyParticipants",
		run: func(t *testing.T, clients fallbackClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			GenerateForStudyParticipants(clients.api, db, instanceID, whatsAppOnlyTemplate(fallbackStudy, withWhatsApp), nil, true, "whatsapp only test")
		},
	},
	{
		name: "GenerateParticipantMessages",
		run: func(t *testing.T, clients fallbackClients, db *messagedb.MessageDBService, instanceID string, withWhatsApp bool) {
			insertWhatsAppOnlyTemplate(t, db, instanceID, withWhatsApp)
			var wg sync.WaitGroup
			wg.Add(1)
			GenerateParticipantMessages(clients.api, db, instanceID, "whatsapp only test", &wg)
			wg.Wait()
		},
	},
}

func TestWhatsAppOnlyUserGetsNoEmail(t *testing.T) {
	for _, g := range whatsAppOnlyGenerators {
		g := g
		t.Run(g.name, func(t *testing.T) {
			configureWhatsAppForTest(t, true)
			db, instanceID, cleanup := fallbackDBService(t)
			defer cleanup()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			clients := newFallbackClients(t, ctrl)
			logs := captureLogs(t)

			g.run(t, clients, db, instanceID, true)

			rows := countQueues(t, db, instanceID)
			t.Logf("%s: emails=%v whatsapp=%v", g.name, rows.emails, rows.whatsapp)
			if rows.whatsapp["waonly"] != 1 || rows.emails["waonly"] != 0 {
				t.Errorf("whatsapp-only user: expected 1 WhatsApp and 0 e-mails, got %d / %d",
					rows.whatsapp["waonly"], rows.emails["waonly"])
			}
			if rows.whatsapp["emailwa"] != 1 || rows.emails["emailwa"] != 1 {
				t.Errorf("e-mail and whatsapp user: expected 1 WhatsApp and 1 e-mail, got %d / %d",
					rows.whatsapp["emailwa"], rows.emails["emailwa"])
			}
			if rows.waN != 2 || rows.emailsN != 1 {
				t.Errorf("expected 2 WhatsApp and 1 e-mail rows in total, got %d / %d", rows.waN, rows.emailsN)
			}
			total, failed := logs.counters(t)
			if total != len(fallbackUsers) || failed != 0 {
				t.Errorf("expected total=%d failed=0, got total=%d failed=%d", len(fallbackUsers), total, failed)
			}
			if logs.fallbackLines() != 0 {
				t.Errorf("no fallback expected, got %d fallback log lines", logs.fallbackLines())
			}
			if g.name == "GenerateParticipantMessages" && len(*clients.deleted) != len(fallbackUsers) {
				t.Errorf("expected the source message to be consumed once per user, got %v", *clients.deleted)
			}
		})
	}
}

// With no WhatsApp template the message cannot be prepared: the whatsapp-only user must still
// be reached, by e-mail.
func TestWhatsAppOnlyUserFallsBackToEmailWhenWhatsAppCannotBePrepared(t *testing.T) {
	for _, g := range whatsAppOnlyGenerators {
		g := g
		t.Run(g.name, func(t *testing.T) {
			configureWhatsAppForTest(t, true)
			db, instanceID, cleanup := fallbackDBService(t)
			defer cleanup()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			clients := newFallbackClients(t, ctrl)
			logs := captureLogs(t)

			g.run(t, clients, db, instanceID, false)

			rows := countQueues(t, db, instanceID)
			t.Logf("%s: emails=%v whatsapp=%v", g.name, rows.emails, rows.whatsapp)
			for _, c := range fallbackUsers {
				if rows.emails[c.id] != 1 || rows.whatsapp[c.id] != 0 {
					t.Errorf("user %s: expected 1 e-mail and 0 WhatsApp, got %d / %d",
						c.id, rows.emails[c.id], rows.whatsapp[c.id])
				}
			}
			total, failed := logs.counters(t)
			if total != len(fallbackUsers) || failed != 0 {
				t.Errorf("expected total=%d failed=0, got total=%d failed=%d", len(fallbackUsers), total, failed)
			}
			if g.name == "GenerateParticipantMessages" && len(*clients.deleted) != len(fallbackUsers) {
				t.Errorf("expected the source message to be consumed once per user, got %v", *clients.deleted)
			}
		})
	}
}

func TestUserPrefersChannel(t *testing.T) {
	withChannels := func(ch []string) *umAPI.User {
		return &umAPI.User{ContactPreferences: &umAPI.ContactPreferences{PreferredChannels: ch}}
	}
	cases := []struct {
		name      string
		user      *umAPI.User
		wantEmail bool
		wantWA    bool
	}{
		{"whatsapp only", withChannels([]string{"whatsapp"}), false, true},
		{"email only", withChannels([]string{"email"}), true, false},
		{"both", withChannels([]string{"email", "whatsapp"}), true, true},
		{"empty defaults to email", withChannels([]string{}), true, false},
		{"nil channels default to email", withChannels(nil), true, false},
		{"no contact preferences default to email", &umAPI.User{}, true, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := userPrefersChannel(c.user, channelEmail); got != c.wantEmail {
				t.Errorf("email: expected %v, got %v", c.wantEmail, got)
			}
			if got := userPrefersChannel(c.user, channelWhatsApp); got != c.wantWA {
				t.Errorf("whatsapp: expected %v, got %v", c.wantWA, got)
			}
		})
	}
}
