package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/internal/config"
	emailAPI "github.com/influenzanet/messaging-service/pkg/api/email_client_service"
	"github.com/influenzanet/messaging-service/pkg/bulk_messages"
	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	gc "github.com/influenzanet/messaging-service/pkg/grpc/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
)

const (
	outgoingBatchSize = 20
)

// Config is the structure that holds all global configuration data
type Config struct {
	LogLevel    logger.LogLevel
	Frequencies struct {
		HighPrio    int
		LowPrio     int
		AutoMessage int
	}
	MessageDBConfig types.DBConfig
	GlobalDBConfig  types.DBConfig
	ServiceURLs     struct {
		UserManagementService string
		EmailClientService    string
		StudyService          string
	}
}

func initConfig() Config {
	conf := Config{}
	hp, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_HIGH_PRIO"))
	if err != nil {
		logger.Error.Fatal(err)
	}
	lp, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_LOW_PRIO"))
	if err != nil {
		logger.Error.Fatal(err)
	}
	am, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_AUTO_MESSAGE"))
	if err != nil {
		logger.Error.Fatal(err)
	}

	conf.LogLevel = config.GetLogLevel()

	conf.Frequencies = struct {
		HighPrio    int
		LowPrio     int
		AutoMessage int
	}{
		HighPrio:    hp,
		LowPrio:     lp,
		AutoMessage: am,
	}
	conf.ServiceURLs.UserManagementService = os.Getenv("ADDR_USER_MANAGEMENT_SERVICE")
	conf.ServiceURLs.StudyService = os.Getenv("ADDR_STUDY_SERVICE")
	conf.ServiceURLs.EmailClientService = os.Getenv("ADDR_EMAIL_CLIENT_SERVICE")
	conf.MessageDBConfig = config.GetMessageDBConfig()
	conf.GlobalDBConfig = config.GetGlobalDBConfig()
	return conf
}

func main() {
	conf := initConfig()

	logger.SetLevel(conf.LogLevel)

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
	// <---

	messageDBService := messagedb.NewMessageDBService(conf.MessageDBConfig)
	globalDBService := globaldb.NewGlobalDBService(conf.GlobalDBConfig)

	go runnerForLowPrioOutgoingEmails(messageDBService, globalDBService, clients, conf.Frequencies.LowPrio)
	go runnerForAutoMessages(messageDBService, globalDBService, clients, conf.Frequencies.AutoMessage)
	runnerForHighPrioOutgoingEmails(messageDBService, globalDBService, clients, conf.Frequencies.HighPrio)
}

func runnerForHighPrioOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	lastAttemptOlderThan := int64(float64(freq) * 0.8)
	for {
		logger.Debug.Println("Fetch and send high prio outgoing emails.")
		go handleOutgoingEmails(mdb, gdb, clients, lastAttemptOlderThan, true)
		time.Sleep(time.Duration(freq) * time.Second)
	}
}

func runnerForLowPrioOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	olderThan := int64(float64(freq) * 0.8)
	for {
		logger.Debug.Println("Fetch and send low prio outgoing emails.")
		go handleOutgoingEmails(mdb, gdb, clients, olderThan, false)
		time.Sleep(time.Duration(freq) * time.Second)
	}
}

func runnerForAutoMessages(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	for {
		logger.Debug.Println("Fetch and send scheduled bulk messages.")
		go handleAutoMessages(mdb, gdb, clients)
		time.Sleep(time.Duration(freq) * time.Second)
	}
}

func handleOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, lastAttemptOlderThan int64, onlyHighPrio bool) {
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("%v", err)
	}
	for _, instance := range instances {
		go handleOutgoingForInstanceID(mdb, instance.InstanceID, clients, lastAttemptOlderThan, onlyHighPrio)
	}
}

func handleOutgoingForInstanceID(mdb *messagedb.MessageDBService, instanceID string, clients *types.APIClients, lastAttemptOlderThan int64, onlyHighPrio bool) {
	counters := types.InitMessageCounter()
	for {
		emails, err := mdb.FetchOutgoingEmails(instanceID, outgoingBatchSize, lastAttemptOlderThan, onlyHighPrio)
		if err != nil {
			logger.Error.Printf("%s: %v", instanceID, err)
			break
		}
		if len(emails) < 1 {
			break
		}

		for _, email := range emails {
			_, err := clients.EmailClientService.SendEmail(context.Background(), &emailAPI.SendEmailReq{
				To:              email.To,
				HeaderOverrides: email.HeaderOverrides.ToEmailClientAPI(),
				Subject:         email.Subject,
				Content:         email.Content,
				HighPrio:        email.HighPrio,
			})
			if err != nil {
				logger.Error.Printf("Could not send email in instance %s: %v", instanceID, err)
				counters.IncreaseCounter(false)
				continue
			}

			_, err = mdb.AddToSentEmails(instanceID, email)
			if err != nil {
				logger.Error.Printf("Error while saving to sent: %v", err)
				continue
			}
			err = mdb.DeleteOutgoingEmail(instanceID, email.ID.Hex())
			if err != nil {
				logger.Error.Printf("Error while deleting outgoing: %v", err)
			}
			counters.IncreaseCounter(true)
		}
	}
	counters.Stop()
	prioText := ""
	if onlyHighPrio {
		prioText = " with high prio"
	}
	logger.Info.Printf("[%s] Finished processing %d (%d sent, %d failed) messages%s in %d s.", instanceID, counters.Total, counters.Success, counters.Failed, prioText, counters.Duration)
}

func handleAutoMessages(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients) {
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("GetAllInstances: %v", err)
	}
	for _, instance := range instances {
		activeMessages, err := mdb.FindAutoMessages(instance.InstanceID, true)
		if err != nil {
			logger.Error.Printf("FindAutoMessages for %s: %v", instance.InstanceID, err)
			continue
		}
		if len(activeMessages) < 1 {
			continue
		}

		for _, messageDef := range activeMessages {
			go bulk_messages.GenerateAutoMessages(
				clients,
				mdb,
				instance.InstanceID,
				messageDef,
				false,
				messageDef.Label,
			)

			messageDef.NextTime += messageDef.Period
			_, err := mdb.SaveAutoMessage(instance.InstanceID, messageDef)
			if err != nil {
				logger.Error.Printf("%s: %v", instance.InstanceID, err)
				continue
			}
		}
	}
}
