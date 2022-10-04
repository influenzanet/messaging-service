package email_client_emulator

import (
	"bufio"
	"context"
	"os"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/protobuf/ptypes/empty"
	api "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
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
		//Schleife über mehrere Empfänger der Mail
		for _, to := range req.To {
			//Verzeichnis mit Emulator Path + Adresse anlegen, falls nicht vorhanden
			filepath := s.EmailClientEmulatorPath + "/" + to
			err = os.MkdirAll(filepath, os.ModePerm)
			if err != nil {
				logger.Error.Printf("error sending mail: err at target path mkdir %v", err.Error())
			}
			//Name Email: Date+subject
			filename := time.Now().Format("2006-01-01 15:04:05") + " " + req.Subject + ".html"
			f, err := os.Create(filepath + "/" + filename)
			if err != nil {
				logger.Error.Printf("error while creating file %v", filename)
			}
			defer f.Close()

			//_, err := f.WriteString(req.Content)
			w := bufio.NewWriter(f)
			_, err = w.WriteString(req.Content)
			if err != nil {
				logger.Error.Printf("error while writing mail to %v", filename)
			}
			w.Flush()
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
