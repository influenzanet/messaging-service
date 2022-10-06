package messaging_service

import (
	"context"
	"fmt"

	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/go-utils/pkg/token_checks"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/templates"
	"github.com/influenzanet/messaging-service/pkg/types"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *messagingServer) GetEmailTemplates(ctx context.Context, req *api.GetEmailTemplatesReq) (*api.EmailTemplates, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_GET_EMAIL_TEMPLATES, "permission denied")
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	templates, err := s.messageDBservice.FindAllEmailTempates(req.Token.InstanceId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	resp := &api.EmailTemplates{
		Templates: make([]*api.EmailTemplate, len(templates)),
	}
	for i, v := range templates {
		resp.Templates[i] = v.ToAPI()
	}
	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_GET_EMAIL_TEMPLATES, "")
	return resp, nil
}

func (s *messagingServer) SaveEmailTemplate(ctx context.Context, req *api.SaveEmailTemplateReq) (*api.EmailTemplate, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.Template.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_SAVE_EMAIL_TEMPLATE, fmt.Sprintf("permission denied for template %s:%s", req.Template.MessageType, req.Template.StudyKey))
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	templ := types.EmailTemplateFromAPI(req.Template)
	err := templates.ResolveTemplateByLang(
		templ,
		req.Token.InstanceId,
		make(map[string]string),
	)
	//TODO: improve err handling/ reuse content???
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	templ, err = s.messageDBservice.SaveEmailTemplate(req.Token.InstanceId, templ)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_REMOVE_EMAIL_TEMPLATE, fmt.Sprintf("for template %s:%s", req.Template.MessageType, req.Template.StudyKey))
	return templ.ToAPI(), nil
}

func (s *messagingServer) DeleteEmailTemplate(ctx context.Context, req *api.DeleteEmailTemplateReq) (*api.ServiceStatus, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_REMOVE_EMAIL_TEMPLATE, fmt.Sprintf("permission denied for template %s:%s", req.MessageType, req.StudyKey))
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	err := s.messageDBservice.DeleteEmailTemplate(req.Token.InstanceId, req.MessageType, req.StudyKey)
	if err != nil {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_ERROR, constants.LOG_EVENT_REMOVE_EMAIL_TEMPLATE, fmt.Sprintf("for template %s:%s", req.MessageType, req.StudyKey))
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_REMOVE_EMAIL_TEMPLATE, fmt.Sprintf("for template %s:%s", req.MessageType, req.StudyKey))
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "template deleted",
	}, nil
}
