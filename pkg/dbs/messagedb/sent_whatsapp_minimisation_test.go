package messagedb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
)

// Every row of sent-whatsapp, whatever took the message out of the queue, keeps only the
// masked recipient and none of the content parameters. The rows are read back raw, so a field
// added later that copies the number or a parameter fails here too.
func TestSentWhatsAppKeepsNoFullNumberAndNoParameters(t *testing.T) {
	const (
		fullNumber    = "+393331234567"
		nationalPart  = "3331234567"
		loginToken    = "login-token-value"
		participantID = "minimisation-user"
	)
	outcomes := []types.WhatsAppSendOutcome{
		{Status: types.WhatsAppStatusDelivered},
		{Status: types.WhatsAppStatusFailed, ErrorCode: 131026, ErrorClass: "permanent"},
		{Status: types.WhatsAppStatusCancelled},
	}
	for _, outcome := range outcomes {
		t.Run(outcome.Status, func(t *testing.T) {
			messageType := "minimisation-" + outcome.Status
			queued := types.OutgoingWhatsApp{
				MessageType:   messageType,
				ToPhoneNumber: fullNumber,
				TemplateName:  "influenzanet_weekly_v1",
				Lang:          "it",
				ContentParams: map[string]string{"loginToken": loginToken, "name": "Mario"},
				UserID:        participantID,
				SendAttempt:   2,
			}
			if _, err := testDBService.AddToSentWhatsApp(testInstanceID, queued, outcome); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var raw bson.M
			if err := testDBService.collectionRefSentWhatsApp(testInstanceID).FindOne(ctx, bson.M{"messageType": messageType}).Decode(&raw); err != nil {
				t.Fatalf("read the archived row: %v", err)
			}

			if got := raw["toPhoneNumber"]; got != "+39***4567" {
				t.Errorf("archived recipient = %v, want the masked +39***4567", got)
			}
			if _, ok := raw["contentParams"]; ok {
				t.Errorf("the archived row has a contentParams field: %v", raw["contentParams"])
			}
			dump, err := bson.MarshalExtJSON(raw, false, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{nationalPart, loginToken, "Mario"} {
				if strings.Contains(string(dump), secret) {
					t.Errorf("the archived row holds %q: %s", secret, dump)
				}
			}
			// What the archive is for is still there.
			if raw["status"] != outcome.Status || raw["userId"] != participantID || raw["templateName"] != "influenzanet_weekly_v1" {
				t.Errorf("archived row lost its outcome, user or template: %s", dump)
			}
		})
	}
}
