package email_client_service

import (
	"context"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/protobuf/ptypes/empty"
	api "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	"github.com/influenzanet/messaging-service/pkg/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxRetry = 5
)

func (s *emailClientServer) Status(ctx context.Context, _ *empty.Empty) (*api.ServiceStatus, error) {
	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "service running",
		Version: apiVersion,
	}, nil
}

func (s *emailClientServer) SendEmail(ctx context.Context, req *api.SendEmailReq) (*api.ServiceStatus, error) {
	if req == nil || len(req.To) < 1 {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	retryCounter := 0
	for {
		var err error
		if req.HighPrio {
			err = s.HighPrioStmpClients.SendMail(
				req.To,
				req.Subject,
				req.Content,
				types.HeaderOverridesFromEmailClientAPI(req.HeaderOverrides),
			)
		} else {
			err = s.StmpClients.SendMail(
				req.To,
				req.Subject,
				req.Content,
				types.HeaderOverridesFromEmailClientAPI(req.HeaderOverrides),
			)
		}
		if err != nil {
			if retryCounter >= maxRetry {
				return nil, status.Error(codes.Internal, err.Error())
			}
			retryCounter += 1
			logger.Error.Printf("SendEmail attempt #%d %v", retryCounter, err)
			time.Sleep(time.Duration(retryCounter) * time.Second)
		} else {
			break
		}
	}

	return &api.ServiceStatus{
		Version: apiVersion,
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "email sent",
	}, nil
}
