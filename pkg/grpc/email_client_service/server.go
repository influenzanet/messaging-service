package email_client_service

import (
	"context"
	"net"
	"os"
	"os/signal"

	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	sc "github.com/influenzanet/messaging-service/pkg/smtp_client"
	"google.golang.org/grpc"
)

const (
	// apiVersion is version of API is provided by server
	apiVersion = "v1"
)

type emailClientServer struct {
	api.UnimplementedEmailClientServiceApiServer
	HighPrioStmpClients *sc.SmtpClients
	StmpClients         *sc.SmtpClients
}

// NewEmailClientServiceServer creates a new service instance
func NewEmailClientServiceServer(
	hpsClients *sc.SmtpClients,
	sClients *sc.SmtpClients,
) api.EmailClientServiceApiServer {
	return &emailClientServer{
		HighPrioStmpClients: hpsClients,
		StmpClients:         sClients,
	}
}

// RunServer runs gRPC service to publish ToDo service
func RunServer(
	ctx context.Context, port string,
	highPrioClients *sc.SmtpClients,
	sClients *sc.SmtpClients,
) error {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.Error.Fatalf("failed to listen: %v", err)
	}

	// register service
	server := grpc.NewServer()
	api.RegisterEmailClientServiceApiServer(server, NewEmailClientServiceServer(
		highPrioClients, sClients,
	))

	// graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			// sig is a ^C, handle it
			logger.Info.Println("shutting down gRPC server...")
			server.GracefulStop()
			<-ctx.Done()
		}
	}()

	// start gRPC server
	logger.Info.Println("starting gRPC server...")
	logger.Info.Println("wait connections on port " + port)
	return server.Serve(lis)
}
