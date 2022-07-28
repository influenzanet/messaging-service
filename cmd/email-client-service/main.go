package main

import (
	"context"
	"os"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/grpc/email_client_service"
	sc "github.com/influenzanet/messaging-service/pkg/smtp_client"
)

// Config is the structure that holds all global configuration data
type Config struct {
	Port                     string
	ServerConfigPath         string
	HighPrioServerConfigPath string
}

func initConfig() Config {
	conf := Config{}
	conf.Port = os.Getenv("EMAIL_CLIENT_SERVICE_LISTEN_PORT")
	conf.ServerConfigPath = os.Getenv("MESSAGING_CONFIG_FOLDER") + "/smtp-servers.yaml"
	conf.HighPrioServerConfigPath = os.Getenv("MESSAGING_CONFIG_FOLDER") + "/high-prio-smtp-servers.yaml"
	return conf
}

func main() {
	conf := initConfig()

	smtpClients, err := sc.NewSmtpClients(conf.ServerConfigPath)
	if err != nil {
		logger.Error.Fatal(err)
	}
	highPrioSmtpClients, err := sc.NewSmtpClients(conf.HighPrioServerConfigPath)
	if err != nil {
		logger.Error.Fatal(err)
	}

	ctx := context.Background()
	if err := email_client_service.RunServer(
		ctx,
		conf.Port,
		highPrioSmtpClients,
		smtpClients,
	); err != nil {
		logger.Error.Fatal(err)
	}
}
