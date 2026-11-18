// Package whatsappwebhook processes the notifications Meta sends to the WhatsApp webhook: the
// delivery statuses of the messages the scheduler sent, and the messages participants send back.
// It acts on two of them only: a reply "STOP", and a message Meta could not deliver because the
// number is not on WhatsApp (error 131026). Both take WhatsApp out of the participant's
// notification channels, through user-management.
//
// The package does not receive anything itself. The endpoint that Meta calls checks the
// signature of the request and hands its body to Processor.Process; that endpoint may run in
// another service or outside the cluster.
package whatsappwebhook

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StopKeyword is the only reply that opts a participant out. Case and surrounding spaces are
// ignored; any other text changes nothing.
const StopKeyword = "STOP"

// UndeliverableErrorCode is Meta's error for a message to a number that is not on WhatsApp or
// cannot receive it. It removes the channel on its first occurrence.
const UndeliverableErrorCode = 131026

// DefaultCallTimeout bounds each call to user-management.
const DefaultCallTimeout = 5 * time.Second

// ErrMalformedPayload reports a body that is not a webhook payload. Delivering it again cannot
// change that, so it is not to be retried.
var ErrMalformedPayload = errors.New("not a WhatsApp webhook payload")

// DeliveryStatusStore records Meta's delivery statuses on the archived messages.
type DeliveryStatusStore interface {
	RecordWhatsAppDeliveryStatus(instanceID, wamid, status string, at int64, errorCode int) (userID string, found bool, err error)
}

// ChannelRemover is the user-management call that takes WhatsApp out of a participant's channels.
type ChannelRemover interface {
	RemoveWhatsAppChannel(ctx context.Context, in *umAPI.RemoveWhatsAppChannelReq, opts ...grpc.CallOption) (*umAPI.RemoveWhatsAppChannelResp, error)
}

// InstanceResolver names the instance a business phone number id belongs to; ok is false for a
// number that is not one of this platform's.
type InstanceResolver func(phoneNumberID string) (instanceID string, ok bool)

// SingleInstance resolves exactly one phone number id to one instance. An empty id resolves
// nothing.
func SingleInstance(phoneNumberID, instanceID string) InstanceResolver {
	return func(id string) (string, bool) {
		if phoneNumberID == "" || id != phoneNumberID {
			return "", false
		}
		return instanceID, true
	}
}

// Processor handles webhook payloads.
type Processor struct {
	archive   DeliveryStatusStore
	channels  ChannelRemover
	instances InstanceResolver
	// CallTimeout bounds each call to user-management.
	CallTimeout time.Duration
}

// NewProcessor returns a Processor that records statuses in archive and removes channels
// through channels, for the business numbers instances knows.
func NewProcessor(archive DeliveryStatusStore, channels ChannelRemover, instances InstanceResolver) *Processor {
	return &Processor{archive: archive, channels: channels, instances: instances, CallTimeout: DefaultCallTimeout}
}

// Process handles one webhook payload, every status and message in it.
//
// Meta repeats a notification it is not sure was received, and the same payload may come more
// than once; processing it again changes nothing that the first time did not. This is what makes
// a retry safe: Process returns an error when part of the payload could not be handled for a
// reason that may pass (the database or user-management unreachable), after handling the rest,
// and the endpoint then answers Meta with an error so that Meta sends the payload again. A
// payload that cannot be read gives ErrMalformedPayload, which is not to be retried. Anything
// else that cannot be acted on (an unknown business number, an unknown message, a status this
// processing does not use) is logged and skipped.
//
// The text of a message is compared with the stop keyword and then dropped: it is never stored
// or logged, as it may hold health data. Phone numbers are logged masked only.
func (p *Processor) Process(ctx context.Context, body []byte) error {
	var payload envelope
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.Warning.Printf("WhatsApp webhook: payload of %d bytes is not readable, dropped", len(body))
		return ErrMalformedPayload
	}
	if payload.Object != whatsAppBusinessAccount {
		logger.Warning.Printf("WhatsApp webhook: notification for object %q ignored", payload.Object)
		return nil
	}

	var retry error
	for _, e := range payload.Entry {
		for _, c := range e.Changes {
			if c.Field != messagesField {
				logger.Debug.Printf("WhatsApp webhook: field %q ignored", c.Field)
				continue
			}
			instanceID, ok := p.instances(c.Value.Metadata.PhoneNumberID)
			if !ok {
				logger.Warning.Printf("WhatsApp webhook: business number id %q is not this platform's, %d statuses and %d messages ignored",
					c.Value.Metadata.PhoneNumberID, len(c.Value.Statuses), len(c.Value.Messages))
				continue
			}
			for _, s := range c.Value.Statuses {
				if err := p.handleStatus(ctx, instanceID, s); err != nil {
					retry = err
				}
			}
			for _, m := range c.Value.Messages {
				if err := p.handleMessage(ctx, instanceID, m); err != nil {
					retry = err
				}
			}
		}
	}
	return retry
}

func (p *Processor) handleStatus(ctx context.Context, instanceID string, s statusUpdate) error {
	at, _ := strconv.ParseInt(s.Timestamp, 10, 64)
	errorCode := 0
	undeliverable := false
	for _, e := range s.Errors {
		if errorCode == 0 {
			errorCode = e.Code
		}
		if e.Code == UndeliverableErrorCode {
			undeliverable = true
		}
	}

	userID, found, err := p.archive.RecordWhatsAppDeliveryStatus(instanceID, s.ID, s.Status, at, errorCode)
	if err != nil {
		logger.Error.Printf("WhatsApp status: instance=%s could not be recorded: %v", instanceID, err)
		return err
	}
	if !found {
		// Not a message the scheduler sent (a verification code, for one), one already purged, or
		// a status the archive does not record.
		logger.Debug.Printf("WhatsApp status: instance=%s status=%q not recorded: unknown message or status", instanceID, s.Status)
		return nil
	}
	if s.Status != types.MetaStatusFailed {
		return nil
	}
	logger.Info.Printf("WhatsApp status: instance=%s user=%s failed code=%d", instanceID, userID, errorCode)
	if !undeliverable {
		return nil
	}
	if userID == "" {
		logger.Warning.Printf("WhatsApp status: instance=%s undeliverable message without an account, no channel to remove", instanceID)
		return nil
	}
	// Removing the channel is asked again for a status seen before, whether or not this call
	// recorded it: a retry after user-management was unreachable must still remove it, and
	// user-management changes nothing the second time.
	return p.removeChannel(ctx, instanceID, &umAPI.RemoveWhatsAppChannelReq{
		InstanceId: instanceID,
		Target:     &umAPI.RemoveWhatsAppChannelReq_UserId{UserId: userID},
		Reason:     umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNDELIVERABLE,
	}, "user="+userID)
}

func (p *Processor) handleMessage(ctx context.Context, instanceID string, m inboundMessage) error {
	phone, ok := senderNumber(m.From)
	if !ok {
		logger.Warning.Printf("WhatsApp message: instance=%s type=%q without a usable sender number, ignored", instanceID, m.Type)
		return nil
	}
	masked := types.MaskPhone(phone)
	if !isStop(m) {
		logger.Info.Printf("WhatsApp message: instance=%s phone=%s type=%q outcome=ignored", instanceID, masked, m.Type)
		return nil
	}
	return p.removeChannel(ctx, instanceID, &umAPI.RemoveWhatsAppChannelReq{
		InstanceId: instanceID,
		Target:     &umAPI.RemoveWhatsAppChannelReq_PhoneNumber{PhoneNumber: phone},
		Reason:     umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_STOP_KEYWORD,
	}, "phone="+masked)
}

// removeChannel asks user-management to remove the channel. who names the participant in the
// logs, masked. A failure that may pass is returned, so that Meta delivers the notification
// again; one that cannot is an alert, since the opt-out is then lost.
func (p *Processor) removeChannel(ctx context.Context, instanceID string, req *umAPI.RemoveWhatsAppChannelReq, who string) error {
	reason := reasonName(req.Reason)
	callCtx, cancel := context.WithTimeout(ctx, p.callTimeout())
	defer cancel()
	resp, err := p.channels.RemoveWhatsAppChannel(callCtx, req)
	if err != nil {
		code := status.Code(err)
		if retryable(code) {
			logger.Error.Printf("WhatsApp opt-out: instance=%s reason=%s %s not applied yet, code=%s; left to Meta's retry", instanceID, reason, who, code)
			return err
		}
		logger.Error.Printf("WhatsApp opt-out: instance=%s reason=%s %s refused, code=%s", instanceID, reason, who, code)
		logger.Error.Printf("WA_ALERT opt_out_failed instance=%s reason=%s code=%s: user-management refused the removal of the WhatsApp channel; the participant is still reached on WhatsApp", instanceID, reason, code)
		return nil
	}
	outcome := "unchanged"
	if resp.GetChannelRemoved() {
		outcome = "removed"
	}
	logger.Info.Printf("WhatsApp opt-out: instance=%s reason=%s %s outcome=%s", instanceID, reason, who, outcome)
	return nil
}

func (p *Processor) callTimeout() time.Duration {
	if p.CallTimeout > 0 {
		return p.CallTimeout
	}
	return DefaultCallTimeout
}

// retryable tells a failure that a later delivery may get past from one it cannot. Unimplemented
// is retried: it means user-management runs a version without the call, which the deployment
// order rules out and which an update fixes, and Meta keeps the notification meanwhile.
func retryable(code codes.Code) bool {
	switch code {
	case codes.InvalidArgument, codes.PermissionDenied, codes.Unauthenticated,
		codes.FailedPrecondition, codes.NotFound, codes.OutOfRange, codes.AlreadyExists:
		return false
	}
	return true
}

func reasonName(r umAPI.WhatsAppRemovalReason) string {
	switch r {
	case umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_STOP_KEYWORD:
		return "stop_keyword"
	case umAPI.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNDELIVERABLE:
		return "undeliverable"
	}
	return "unknown"
}

// isStop tells whether the message is the stop keyword: a text message whose body, without the
// spaces around it, is STOP in any case.
func isStop(m inboundMessage) bool {
	return m.Type == "text" && m.Text != nil && strings.EqualFold(strings.TrimSpace(m.Text.Body), StopKeyword)
}

// senderNumber turns the sender as Meta writes it (digits, without "+") into the international
// form user-management expects. ok is false when it is not a number: empty, too long for E.164,
// or holding anything but digits after an optional "+".
func senderNumber(from string) (string, bool) {
	digits := strings.TrimPrefix(from, "+")
	if digits == "" || len(digits) > 15 {
		return "", false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return "+" + digits, true
}
