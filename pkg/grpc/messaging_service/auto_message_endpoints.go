package messaging_service

import (
	"context"
	"fmt"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/go-utils/pkg/token_checks"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/templates"
	"github.com/influenzanet/messaging-service/pkg/types"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *messagingServer) GetAutoMessages(ctx context.Context, req *api.GetAutoMessagesReq) (*api.AutoMessages, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_GET_AUTO_MESSAGES, "permission denied for auto message")
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	autoMessages, err := s.messageDBservice.FindAutoMessages(req.Token.InstanceId, false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_GET_AUTO_MESSAGES, "")
	resp := &api.AutoMessages{
		AutoMessages: make([]*api.AutoMessage, len(autoMessages)),
	}
	for i, v := range autoMessages {
		resp.AutoMessages[i] = v.ToAPI()
	}
	return resp, nil
}

func (s *messagingServer) SaveAutoMessage(ctx context.Context, req *api.SaveAutoMessageReq) (*api.AutoMessage, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.AutoMessage == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_SAVE_AUTO_MESSAGE, fmt.Sprintf("permission denied for auto message %s", req.AutoMessage.Id))
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	reqMsg := types.AutoMessageFromAPI(req.AutoMessage)
	err := templates.CheckAllTranslationsParsable(
		reqMsg.Template,
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if 0 < reqMsg.Until {
		if reqMsg.Until < time.Now().Unix() {
			logger.Error.Println("Termination Date of auto message schedule ", time.Unix(reqMsg.Until, 0), " is outdated")
			return nil, status.Error(codes.InvalidArgument, "invalid termination date of auto message schedule, is in past")
		}
		if reqMsg.Until < reqMsg.NextTime {
			logger.Error.Println("Termination Date of auto message schedule ", time.Unix(reqMsg.Until, 0), " is earlier than Start Date ", time.Unix(reqMsg.NextTime, 0))
			return nil, status.Error(codes.InvalidArgument, "invalid termination date of auto message schedule, earlier than start date")
		}
	}
	autoMsg, err := s.messageDBservice.SaveAutoMessage(req.Token.InstanceId, *reqMsg)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_SAVE_AUTO_MESSAGE, autoMsg.ID.Hex())
	return autoMsg.ToAPI(), nil
}

func (s *messagingServer) DeleteAutoMessage(ctx context.Context, req *api.DeleteAutoMessageReq) (*api.ServiceStatus, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.AutoMessageId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_REMOVE_AUTO_MESSAGE, fmt.Sprintf("permission denied for  %s", req.AutoMessageId))
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	err := s.messageDBservice.DeleteAutoMessage(req.Token.InstanceId, req.AutoMessageId)
	if err != nil {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_ERROR, constants.LOG_EVENT_REMOVE_AUTO_MESSAGE, req.AutoMessageId)
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_REMOVE_AUTO_MESSAGE, req.AutoMessageId)
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "auto message deleted",
	}, nil
}
