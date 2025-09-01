package messaging_service

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/protobuf/ptypes/empty"

	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/go-utils/pkg/token_checks"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	emailAPI "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/bulk_messages"
	"github.com/influenzanet/messaging-service/pkg/templates"
	"github.com/influenzanet/messaging-service/pkg/types"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *messagingServer) Status(ctx context.Context, _ *empty.Empty) (*api.ServiceStatus, error) {
	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "service running",
		Version: apiVersion,
	}, nil
}

func (s *messagingServer) SendNotification(ctx context.Context, req *api.SendNotificationReq) (*api.ServiceStatus, error) {
	if req == nil || req.InstanceId == "" || req.UserId == "" || req.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}

	// 1. Get user preferences from user-management-service
	// NOTE: This call requires valid authentication token between services.
	// Assumes token is handled at infrastructure level (e.g., proxy or gRPC headers).
	// For now, pass empty token, but this must be handled in production.
	userRef := &umAPI.UserReference{
		Token: &api_types.TokenInfos{
			InstanceId: req.InstanceId,
			Id:         req.UserId,
		},
	}
	prefs, err := s.clients.UserManagementService.GetUserContactPreferences(ctx, userRef)

	if err != nil {
		logger.Error.Printf("SendNotification: failed to get user preferences for %s: %v", req.UserId, err)
		return nil, status.Error(codes.Internal, "cannot get user preferences")
	}

	// 2. Try sending on preferred channels
	var sent bool
	var lastErr error
	for _, channel := range prefs.PreferredChannels {
		var errChan error
		switch channel {
		case "whatsapp":
			if prefs.PhoneNumber != "" {
				errChan = s.sendWhatsAppWithRetry(ctx, req.InstanceId, prefs.PhoneNumber, req.MessageType, req.UserId, req.ContentParams)
			} else {
				errChan = fmt.Errorf("phone number not available for user %s", req.UserId)
			}
		case "email":
			if prefs.Email != "" {
				// Reuse existing email sending logic
				_, errChan = s.SendInstantEmail(ctx, &api.SendEmailReq{
					InstanceId:   req.InstanceId,
					To:           []string{prefs.Email},
					MessageType:  req.MessageType,
					ContentInfos: req.ContentParams,
				})
			} else {
				errChan = fmt.Errorf("email not available for user %s", req.UserId)
			}
		default:
			logger.Warning.Printf("unsupported channel '%s' for user %s", channel, req.UserId)
			continue
		}

		if errChan == nil {
			sent = true
			logger.Info.Printf("Notification for user %s sent successfully on channel %s", req.UserId, channel)
			break // Message sent successfully, exit loop
		}
		lastErr = errChan
		logger.Warning.Printf("Failed to send notification for user %s on channel %s: %v", req.UserId, channel, errChan)
	}

	if !sent {
		logger.Error.Printf("Failed to send notification for user %s on all preferred channels. Last error: %v", req.UserId, lastErr)
		return nil, status.Error(codes.Internal, "failed to send notification on all channels")
	}

	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "Notification sent successfully",
		Version: apiVersion,
	}, nil
}

func (s *messagingServer) sendWhatsAppWithRetry(ctx context.Context, instanceID, phone, messageType, lang string, params map[string]string) error {
	retryDelays := []time.Duration{1 * time.Second, 5 * time.Second, 10 * time.Second} // Retry strategy
	var lastErr error

	logger.Debug.Printf("Attempting to send WhatsApp to %s for message type %s", phone, messageType)

	sendMessageReq := &umAPI.SendMessageRequest{
		InstanceId:    instanceID,
		ToPhoneNumber: phone,
		MessageType:   messageType, // WhatsApp template name
		Lang:          lang,
		ContentParams: params,
	}

	for i, delay := range retryDelays {
		// gRPC call to user-management-service
		_, err := s.clients.UserManagementService.SendMessage(ctx, sendMessageReq)
		if err == nil {
			logger.Info.Printf("WhatsApp message sent successfully to %s on attempt %d", phone, i+1)
			return nil // Success
		}
		lastErr = err
		logger.Warning.Printf("WhatsApp attempt %d failed for %s: %v. Retrying in %v", i+1, phone, err, delay)
		time.Sleep(delay)
	}

	return fmt.Errorf("failed to send to whatsapp after multiple retries: %w", lastErr)
}

func (s *messagingServer) SendMessageToAllUsers(ctx context.Context, req *api.SendMessageToAllUsersReq) (*api.ServiceStatus, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.Template == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_BULK_MESSAGE_SEND, fmt.Sprintf("permission denied for send %s to all users", req.Template.MessageType))
		return nil, status.Error(codes.PermissionDenied, "no permission to send messages")
	}

	// use go method (don't wait for result since it can take long)
	go bulk_messages.GenerateForAllUsers(
		s.clients,
		s.messageDBservice,
		req.Token.InstanceId,
		types.EmailTemplateFromAPI(req.Template),
		req.IgnoreWeekday,
		"one time message",
	)
	return &api.ServiceStatus{
		Msg:     "message sending triggered",
		Status:  api.ServiceStatus_NORMAL,
		Version: apiVersion,
	}, nil
}

func (s *messagingServer) SendMessageToStudyParticipants(ctx context.Context, req *api.SendMessageToStudyParticipantsReq) (*api.ServiceStatus, error) {
	if req == nil || token_checks.IsTokenEmpty(req.Token) || req.StudyKey == "" || req.Template == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	if !token_checks.CheckIfAnyRolesInToken(req.Token, []string{constants.USER_ROLE_RESEARCHER, constants.USER_ROLE_ADMIN}) {
		s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_BULK_MESSAGE_SEND, fmt.Sprintf("permission denied for send %s to study %s", req.Template.MessageType, req.StudyKey))
		return nil, status.Error(codes.PermissionDenied, "no permission to send messages")
	}
	req.Template.StudyKey = req.StudyKey

	// use go method (don't wait for result since it can take long)
	go bulk_messages.GenerateForStudyParticipants(
		s.clients,
		s.messageDBservice,
		req.Token.InstanceId,
		types.EmailTemplateFromAPI(req.Template),
		req.Condition,
		req.IgnoreWeekday,
		"one time message",
	)
	return &api.ServiceStatus{
		Msg:     "message sending triggered",
		Status:  api.ServiceStatus_NORMAL,
		Version: apiVersion,
	}, nil
}

func (s *messagingServer) SendInstantEmail(ctx context.Context, req *api.SendEmailReq) (*api.ServiceStatus, error) {
	if req == nil || req.InstanceId == "" || len(req.To) < 1 || req.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	templateDef, err := s.messageDBservice.FindEmailTemplateByType(req.InstanceId, req.MessageType, req.StudyKey)
	if err != nil {
		return nil, status.Error(codes.Internal, "template not found")
	}

	translation := templates.GetTemplateTranslation(templateDef, req.PreferredLanguage)

	decodedTemplate, err := base64.StdEncoding.DecodeString(translation.TemplateDef)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if req.ContentInfos == nil {
		req.ContentInfos = map[string]string{}
	}
	globalTemplateInfos := templates.LoadGlobalEmailTemplateConstants()
	for k, v := range globalTemplateInfos {
		req.ContentInfos[k] = v
	}

	req.ContentInfos["language"] = req.PreferredLanguage
	// execute template
	templateName := req.InstanceId + req.MessageType + req.PreferredLanguage
	content, err := templates.ResolveTemplate(
		templateName,
		string(decodedTemplate),
		req.ContentInfos,
	)
	if err != nil {
		return nil, status.Error(codes.Internal, "content could not be generated")
	}

	outgoingEmail := types.OutgoingEmail{
		MessageType:     req.MessageType,
		To:              req.To,
		HeaderOverrides: templateDef.HeaderOverrides,
		Subject:         translation.Subject,
		Content:         content,
		HighPrio:        !req.UseLowPrio,
	}

	_, err = s.clients.EmailClientService.SendEmail(ctx, &emailAPI.SendEmailReq{
		To:              outgoingEmail.To,
		HeaderOverrides: outgoingEmail.HeaderOverrides.ToEmailClientAPI(),
		Subject:         outgoingEmail.Subject,
		Content:         content,
		HighPrio:        !req.UseLowPrio,
	})
	if err != nil {
		_, errS := s.messageDBservice.AddToOutgoingEmails(req.InstanceId, outgoingEmail)
		if errS != nil {
			logger.Error.Printf("Error while saving to outgoing: %v", errS)
		}
		return &api.ServiceStatus{
			Version: apiVersion,
			Msg:     "failed sending message, added to outgoing",
			Status:  api.ServiceStatus_PROBLEM,
		}, nil
	}

	_, err = s.messageDBservice.AddToSentEmails(req.InstanceId, outgoingEmail)
	if err != nil {
		logger.Error.Printf("Saving to sent: %v", err)
	}

	return &api.ServiceStatus{
		Version: apiVersion,
		Msg:     "message sent",
		Status:  api.ServiceStatus_NORMAL,
	}, nil
}

func (s *messagingServer) QueueEmailTemplateForSending(ctx context.Context, req *api.SendEmailReq) (*api.ServiceStatus, error) {
	if req == nil || req.InstanceId == "" || len(req.To) < 1 || req.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	templateDef, err := s.messageDBservice.FindEmailTemplateByType(req.InstanceId, req.MessageType, req.StudyKey)
	if err != nil {
		return nil, status.Error(codes.Internal, "template not found")
	}

	translation := templates.GetTemplateTranslation(templateDef, req.PreferredLanguage)

	decodedTemplate, err := base64.StdEncoding.DecodeString(translation.TemplateDef)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if req.ContentInfos == nil {
		req.ContentInfos = map[string]string{}
	}
	globalTemplateInfos := templates.LoadGlobalEmailTemplateConstants()
	for k, v := range globalTemplateInfos {
		req.ContentInfos[k] = v
	}

	req.ContentInfos["language"] = req.PreferredLanguage
	// execute template
	templateName := req.InstanceId + req.MessageType + req.PreferredLanguage
	content, err := templates.ResolveTemplate(
		templateName,
		string(decodedTemplate),
		req.ContentInfos,
	)
	if err != nil {
		return nil, status.Error(codes.Internal, "content could not be generated")
	}

	outgoingEmail := types.OutgoingEmail{
		MessageType:     req.MessageType,
		To:              req.To,
		HeaderOverrides: templateDef.HeaderOverrides,
		Subject:         translation.Subject,
		Content:         content,
		HighPrio:        !req.UseLowPrio,
	}

	_, err = s.messageDBservice.AddToOutgoingEmails(req.InstanceId, outgoingEmail)
	if err != nil {
		logger.Error.Printf("Error while saving to outgoing: %v", err)
		return &api.ServiceStatus{
			Version: apiVersion,
			Msg:     "failed adding message to outgoing",
			Status:  api.ServiceStatus_PROBLEM,
		}, nil
	}

	return &api.ServiceStatus{
		Version: apiVersion,
		Msg:     "message added to ougoing",
		Status:  api.ServiceStatus_NORMAL,
	}, nil
}
