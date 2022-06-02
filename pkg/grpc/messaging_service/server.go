package messaging_service

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"

	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"
	"google.golang.org/grpc"
)

const (
	// apiVersion is version of API is provided by server
	apiVersion = "v1"
)

type messagingServer struct {
	api.UnimplementedMessagingServiceApiServer
	clients          *types.APIClients
	messageDBservice *messagedb.MessageDBService
	//globalDBService  *globaldb.GlobalDBService
}

// NewMessagingServiceServer creates a new service instance
func NewMessagingServiceServer(
	clients *types.APIClients,
	messageDBservice *messagedb.MessageDBService,
) api.MessagingServiceApiServer {
	return &messagingServer{
		clients:          clients,
		messageDBservice: messageDBservice,
	}
}

// RunServer runs gRPC service to publish ToDo service
func RunServer(ctx context.Context, port string,
	clients *types.APIClients,
	messageDBservice *messagedb.MessageDBService,
) error {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	// register service
	server := grpc.NewServer()
	api.RegisterMessagingServiceApiServer(server, NewMessagingServiceServer(
		clients,
		messageDBservice,
	))

	// graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			// sig is a ^C, handle it
			log.Println("shutting down gRPC server...")
			server.GracefulStop()
			<-ctx.Done()
		}
	}()

	// start gRPC server
	log.Println("starting gRPC server...")
	log.Println("wait connections on port " + port)
	return server.Serve(lis)
}
