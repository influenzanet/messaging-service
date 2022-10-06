package email_client_emulator

import (
	"context"
	"net"
	"os"
	"os/signal"

	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	"google.golang.org/grpc"
)

const (
	// apiVersion is version of API is provided by server
	apiVersion = "v1"
)

type emailClientServer struct {
	api.UnimplementedEmailClientServiceApiServer
	EmailClientEmulatorPath string //path as sring here
}

// NewEmailClientServiceServer creates a new service instance
func NewEmailClientServiceServer(
	emulatorPath string,
) api.EmailClientServiceApiServer {
	return &emailClientServer{
		//define String Pfad here und überall wo clients gelöscht wurden
		EmailClientEmulatorPath: emulatorPath,
	}
}

// RunServer runs gRPC service to publish ToDo service
func RunServer(
	ctx context.Context, port string, emulatorPath string,
) error {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.Error.Fatalf("failed to listen: %v", err)
	}

	// register service
	server := grpc.NewServer()
	api.RegisterEmailClientServiceApiServer(server, NewEmailClientServiceServer(
		emulatorPath,
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
