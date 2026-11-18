package main

import (
	"context"
	"os"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/internal/config"
	"github.com/influenzanet/messaging-service/pkg/bulk_messages"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"

	gc "github.com/influenzanet/messaging-service/pkg/grpc/clients"
	"github.com/influenzanet/messaging-service/pkg/grpc/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/whatsappwebhook"
)

// Config is the structure that holds all global configuration data
type Config struct {
	Port            string
	MessageDBConfig types.DBConfig
	ServiceURLs     struct {
		UserManagementService string
		EmailClientService    string
		StudyService          string
		LoggingService        string
	}
}

func initConfig() Config {
	conf := Config{}
	conf.Port = os.Getenv("MESSAGING_SERVICE_LISTEN_PORT")
	conf.ServiceURLs.UserManagementService = os.Getenv("ADDR_USER_MANAGEMENT_SERVICE")
	conf.ServiceURLs.StudyService = os.Getenv("ADDR_STUDY_SERVICE")
	conf.ServiceURLs.LoggingService = os.Getenv("ADDR_LOGGING_SERVICE")
	conf.ServiceURLs.EmailClientService = os.Getenv("ADDR_EMAIL_CLIENT_SERVICE")
	conf.MessageDBConfig = config.GetMessageDBConfig()
	return conf
}

func main() {
	conf := initConfig()

	// ---> client connections
	clients := &types.APIClients{}
	umClient, close := gc.ConnectToUserManagementService(conf.ServiceURLs.UserManagementService)
	defer close()
	clients.UserManagementService = umClient

	emailClient, close := gc.ConnectToEmailClientService(conf.ServiceURLs.EmailClientService)
	defer close()
	clients.EmailClientService = emailClient

	studyClient, close := gc.ConnectToStudyService(conf.ServiceURLs.StudyService)
	defer close()
	clients.StudyService = studyClient

	loggingClient, close := gc.ConnectToLoggingService(conf.ServiceURLs.LoggingService)
	defer close()
	clients.LoggingService = loggingClient
	// <---

	// Single place that decides "is WhatsApp on?" for this process; bulk_messages reads it
	// through Configure instead of checking an environment variable itself. Built after the
	// client connections above: the per-instance half of the decision (Enabled) asks
	// user-management through clients.UserManagementService.
	whatsAppSettings := serverWhatsAppSettings(clients.UserManagementService)
	config.LogWhatsAppStartup("messaging-service", whatsAppSettings)
	bulk_messages.Configure(whatsAppSettings)

	messageDBService := messagedb.NewMessageDBService(conf.MessageDBConfig)

	ctx := context.Background()

	if err := messaging_service.RunServer(
		ctx,
		conf.Port,
		clients,
		messageDBService,
		webhookProcessor(os.Getenv, messageDBService, clients.UserManagementService),
	); err != nil {
		logger.Error.Fatal(err)
	}
}

// serverWhatsAppSettings is the WhatsApp decision of the gRPC server. The server only queues
// WhatsApp messages and the message-scheduler sends them, so it follows the kill switch and
// user-management without the Meta credentials: the token that can send paid messages stays
// out of this process.
func serverWhatsAppSettings(source config.PlatformConfigSource) config.WhatsAppSettings {
	return config.QueueingWhatsAppSettingsFromEnv(source)
}

// webhookProcessor builds the processing of the WhatsApp webhook notifications the gateway
// forwards, for the business number and instance named in the environment. It is nil, and the
// gateway's calls are refused, when they are not named. The processing does not follow the
// WhatsApp switch: an opt-out that arrives while WhatsApp is off is still applied.
func webhookProcessor(getenv func(string) string, archive whatsappwebhook.DeliveryStatusStore, channels whatsappwebhook.ChannelRemover) messaging_service.WebhookProcessor {
	phoneNumberID, instanceID, ok := config.WhatsAppWebhookFromEnv(getenv)
	if !ok {
		logger.Info.Println("WhatsApp webhook processing off: WHATSAPP_PHONE_NUMBER_ID or WHATSAPP_WEBHOOK_INSTANCE_ID is not set")
		return nil
	}
	logger.Info.Printf("WhatsApp webhook processing on for instance %s", instanceID)
	return whatsappwebhook.NewProcessor(archive, channels, whatsappwebhook.SingleInstance(phoneNumberID, instanceID))
}
