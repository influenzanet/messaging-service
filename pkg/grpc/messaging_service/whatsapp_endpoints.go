package messaging_service

import (
	"context"

	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeleteOutgoingWhatsAppForUser is called by user-management whenever a participant withdraws
// WhatsApp: account deletion, phone deletion or change, channel removal, unsubscribe. It is
// safe to repeat, which the caller relies on to retry.
func (s *messagingServer) DeleteOutgoingWhatsAppForUser(ctx context.Context, req *api.DeleteOutgoingWhatsAppForUserReq) (*api.DeleteOutgoingWhatsAppForUserResp, error) {
	if req == nil || req.InstanceId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if req.PurgeArchive && len(req.MessageTypes) > 0 {
		// An account deletion removes everything; a type filter here means the caller is
		// confused about which withdrawal it is handling.
		return nil, status.Error(codes.InvalidArgument, "purge_archive cannot be limited to message types")
	}

	if req.PurgeArchive {
		queued, archived, err := s.messageDBservice.PurgeWhatsAppForUser(req.InstanceId, req.UserId)
		if err != nil {
			logger.Error.Printf("[%s] cannot purge the WhatsApp messages of a deleted account: %v", req.InstanceId, err)
			return nil, status.Error(codes.Internal, "cannot delete the whatsapp messages")
		}
		logger.Info.Printf("[%s] account deletion: %d queued and %d archived WhatsApp messages deleted", req.InstanceId, queued, archived)
		return &api.DeleteOutgoingWhatsAppForUserResp{RemovedFromQueue: queued, RemovedFromArchive: archived}, nil
	}

	cancelled, err := s.messageDBservice.CancelOutgoingWhatsAppForUser(req.InstanceId, req.UserId, req.MessageTypes)
	if err != nil {
		logger.Error.Printf("[%s] cannot cancel queued WhatsApp messages (%d cancelled before the error): %v", req.InstanceId, cancelled, err)
		return nil, status.Error(codes.Internal, "cannot cancel the whatsapp messages")
	}
	if cancelled > 0 {
		logger.Info.Printf("[%s] WhatsApp withdrawn: %d queued messages cancelled", req.InstanceId, cancelled)
	}
	return &api.DeleteOutgoingWhatsAppForUserResp{RemovedFromQueue: cancelled}, nil
}
