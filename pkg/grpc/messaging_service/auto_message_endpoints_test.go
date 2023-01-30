package messaging_service

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/influenzanet/go-utils/pkg/api_types"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/types"
	loggingMock "github.com/influenzanet/messaging-service/test/mocks/logging_service"
)

func TestGetAutoMessagesEndpoint(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := messagingServer{
		messageDBservice: testMessageDBService,
		clients: &types.APIClients{
			LoggingService: mockLoggingClient,
		},
	}

	_, err := s.messageDBservice.SaveAutoMessage(testInstanceID, types.AutoMessage{Type: "B"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}
	_, err = s.messageDBservice.SaveAutoMessage(testInstanceID, types.AutoMessage{Type: "A", NextTime: time.Now().Unix() + 10})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.GetAutoMessages(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.GetAutoMessages(context.Background(), &api.GetAutoMessagesReq{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with valid arguments", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		resp, err := s.GetAutoMessages(context.Background(), &api.GetAutoMessagesReq{
			Token: &api_types.TokenInfos{
				Id:         "uid",
				InstanceId: testInstanceID,
				Payload: map[string]string{
					"roles":    "PARTICIPANT,RESEARCHER",
					"username": "testuser",
				},
			},
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}

		if len(resp.AutoMessages) != 2 {
			t.Errorf("unexpected number of templates: %d", len(resp.AutoMessages))
			return
		}
	})
}

func TestSaveAutoMessageEndpoint(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := messagingServer{
		messageDBservice: testMessageDBService,
		clients: &types.APIClients{
			LoggingService: mockLoggingClient,
		},
	}

	userToken := &api_types.TokenInfos{
		Id:         "uid",
		InstanceId: testInstanceID,
		Payload: map[string]string{
			"roles":    "PARTICIPANT,RESEARCHER",
			"username": "testuser",
		},
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.SaveAutoMessage(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.SaveAutoMessage(context.Background(), &api.SaveAutoMessageReq{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	id := ""
	t.Run("with new message", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		resp, err := s.SaveAutoMessage(context.Background(), &api.SaveAutoMessageReq{
			Token: userToken,
			AutoMessage: &api.AutoMessage{
				Type: "test1",
				Template: &api.EmailTemplate{
					Translations: []*api.LocalizedTemplate{
						{Lang: "de", TemplateDef: "test", Subject: ""},
					},
				},
			},
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		id = resp.Id
	})

	t.Run("with existing message", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		_, err := s.SaveAutoMessage(context.Background(), &api.SaveAutoMessageReq{
			Token: userToken,
			AutoMessage: &api.AutoMessage{
				Id:   id,
				Type: "test1",
				Template: &api.EmailTemplate{
					Translations: []*api.LocalizedTemplate{
						{Lang: "de", TemplateDef: "test", Subject: ""},
					},
				},
			},
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})
}

func TestDeleteAutoMessageEndpoint(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := messagingServer{
		messageDBservice: testMessageDBService,
		clients: &types.APIClients{
			LoggingService: mockLoggingClient,
		},
	}
	userToken := &api_types.TokenInfos{
		Id:         "uid",
		InstanceId: testInstanceID,
		Payload: map[string]string{
			"roles":    "PARTICIPANT,RESEARCHER",
			"username": "testuser",
		},
	}

	testAutoMessage, err := s.messageDBservice.SaveAutoMessage(testInstanceID, types.AutoMessage{Type: "B"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.DeleteAutoMessage(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.DeleteAutoMessage(context.Background(), &api.DeleteAutoMessageReq{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with not existing message", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		_, err := s.DeleteAutoMessage(context.Background(), &api.DeleteAutoMessageReq{
			Token:         userToken,
			AutoMessageId: "wrong"})
		ok, msg := shouldHaveGrpcErrorStatus(err, "")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with existing message", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		_, err := s.DeleteAutoMessage(context.Background(), &api.DeleteAutoMessageReq{
			Token:         userToken,
			AutoMessageId: testAutoMessage.ID.Hex(),
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})
}
