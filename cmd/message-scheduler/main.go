package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coneno/logger"
	"github.com/google/uuid"
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
		HighPrio                int
		LowPrio                 int
		AutoMessage             int
		ParticipantMessages     int
		ResearcherNotifications int
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
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_HIGH_PRIO: %v", err)
	}

	lp, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_LOW_PRIO"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_LOW_PRIO: %v", err)
	}

	am, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_AUTO_MESSAGE"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_AUTO_MESSAGE: %v", err)
	}
	pm, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_PARTICIPANT_MESSAGE"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_PARTICIPANT_MESSAGE: %v", err)
	}
	rn, err := strconv.Atoi(os.Getenv("MESSAGE_SCHEDULER_INTERVAL_RESEARCHER_NOTIFICATION"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_RESEARCHER_NOTIFICATION: %v", err)
	}

	conf.LogLevel = config.GetLogLevel()

	conf.Frequencies = struct {
		HighPrio                int
		LowPrio                 int
		AutoMessage             int
		ParticipantMessages     int
		ResearcherNotifications int
	}{
		HighPrio:                hp,
		LowPrio:                 lp,
		AutoMessage:             am,
		ParticipantMessages:     pm,
		ResearcherNotifications: rn,
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
	go runnerForParticipantMessages(messageDBService, globalDBService, clients, conf.Frequencies.ParticipantMessages)
	go runnerForResearcherNotifications(messageDBService, globalDBService, clients, conf.Frequencies.ResearcherNotifications)
	runnerForHighPrioOutgoingEmails(messageDBService, globalDBService, clients, conf.Frequencies.HighPrio)
}

func logInitialLoopStartedMsg(loopName string, period time.Duration) {
	logger.Info.Printf("Starting loop for '%s' with a period of %s", loopName, period)
}

func getThreadLockInterval(freq int) int64 {
	return int64(float64(freq) * 2.5)
}

func runnerForHighPrioOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("high prio outgoing emails", period)

	lastAttemptOlderThan := getThreadLockInterval(freq)
	for {
		go handleOutgoingEmails(mdb, gdb, clients, lastAttemptOlderThan, true)
		time.Sleep(period)
	}
}

func runnerForLowPrioOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("low prio outgoing emails", period)

	olderThan := getThreadLockInterval(freq)
	for {
		go handleOutgoingEmails(mdb, gdb, clients, olderThan, false)
		time.Sleep(period)
	}
}

func runnerForParticipantMessages(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	if freq <= 0 {
		logger.Debug.Println("no period defined for participant messages, loop is skipped.")
		return
	}
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("participant messages", period)
	for {
		go handleParticipantMessages(mdb, gdb, clients)
		time.Sleep(period)
	}
}

func runnerForResearcherNotifications(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	if freq <= 0 {
		logger.Debug.Println("no period defined for researcher notifications, loop is skipped.")
		return
	}
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("researcher notifications", period)
	for {
		go handleResearcherNotifications(mdb, gdb, clients)
		time.Sleep(period)
	}
}

func runnerForAutoMessages(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, freq int) {
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("auto messages", period)
	for {
		go handleAutoMessages(mdb, gdb, clients)
		time.Sleep(period)
	}
}

func handleOutgoingEmails(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients, lastAttemptOlderThan int64, onlyHighPrio bool) {
	threadName := "lpOE"
	taskDescription := "fetching and sending low prio outgoing emails"
	if onlyHighPrio {
		threadName = "hpOE"
		taskDescription = "fetching and sending high prio outgoing emails"
	}

	threadID := generateThreadID(threadName)
	logger.Info.Printf("--> Process <%s> started: %s...", threadID, taskDescription)

	var wg sync.WaitGroup
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("%v", err)
	}
	for _, instance := range instances {
		wg.Add(1)
		go handleOutgoingForInstanceID(mdb, instance.InstanceID, clients, lastAttemptOlderThan, onlyHighPrio, &wg)
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: %s", threadID, taskDescription)
}

func handleOutgoingForInstanceID(mdb *messagedb.MessageDBService, instanceID string, clients *types.APIClients, lastAttemptOlderThan int64, onlyHighPrio bool, wg *sync.WaitGroup) {
	defer wg.Done()
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
			if counters.Duration > int64(float64(lastAttemptOlderThan)*0.9) {
				// if process takes too long, skip remaining messages of this batch
				logger.Warning.Printf("Skip sending message ('%s') in instance %s because batch duration was too long (%d)", email.MessageType, instanceID, counters.Duration)
				counters.IncreaseCounter(false)

				err = mdb.ResetLastSendAttemptForOutgoing(instanceID, email.ID.Hex())
				if err != nil {
					logger.Error.Printf("Error while resetting lastSendAttempt for a message ('%s') in instance %s: %v", email.MessageType, instanceID, err)
				}
				continue
			}

			_, err := clients.EmailClientService.SendEmail(context.Background(), &emailAPI.SendEmailReq{
				To:              email.To,
				HeaderOverrides: email.HeaderOverrides.ToEmailClientAPI(),
				Subject:         email.Subject,
				Content:         email.Content,
				HighPrio:        email.HighPrio,
			})
			if err != nil {
				logger.Error.Printf("Could not send email ('%s') in instance %s: %v", email.MessageType, instanceID, err)
				counters.IncreaseCounter(false)

				err = mdb.ResetLastSendAttemptForOutgoing(instanceID, email.ID.Hex())
				if err != nil {
					logger.Error.Printf("Error while resetting lastSendAttempt for a message ('%s') in instance %s: %v", email.MessageType, instanceID, err)
				}
				continue
			}

			_, err = mdb.AddToSentEmails(instanceID, email)
			if err != nil {
				logger.Error.Printf("Error while saving to sent: %v", err)
				continue
			}
			err = mdb.DeleteOutgoingEmail(instanceID, email.ID.Hex())
			if err != nil {
				logger.Error.Printf("Error while deleting outgoing email of type '%s': %v", email.MessageType, err)
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
	threadID := generateThreadID("BM")
	logger.Info.Printf("--> Process <%s> started: fetching and sending scheduled auto messages...", threadID)

	var wg sync.WaitGroup
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
			wg.Add(1)
			go bulk_messages.GenerateAutoMessages(
				clients,
				mdb,
				instance.InstanceID,
				messageDef,
				false,
				messageDef.Label,
				&wg,
			)

			messageDef.NextTime += messageDef.Period
			var flagNextTimeInPast = false
			for messageDef.NextTime < time.Now().Unix() {
				flagNextTimeInPast = true
				messageDef.NextTime += messageDef.Period
			}
			if flagNextTimeInPast {
				logger.Warning.Printf("MessageID: %s (%s) - `nextTime` for sending auto messsages was outdated - updated value: %d", messageDef.ID, messageDef.Label, messageDef.NextTime)
			}
			if 0 < messageDef.Until && messageDef.Until < messageDef.NextTime {
				logger.Info.Printf("MessageID: %s (%s) - Termination date for auto message schedule is reached, schedule will be deleted", messageDef.ID, messageDef.Label)
				err = mdb.DeleteAutoMessage(instance.InstanceID, messageDef.ID.Hex())
				if err != nil {
					logger.Error.Printf("%s: %v", instance.InstanceID, err)
				}
				return
			}
			_, err := mdb.SaveAutoMessage(instance.InstanceID, messageDef)
			if err != nil {
				logger.Error.Printf("%s: %v", instance.InstanceID, err)
				continue
			}
		}
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: fetching and sending scheduled auto messages", threadID)
}

func handleParticipantMessages(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients) {
	threadID := generateThreadID("PM")
	logger.Info.Printf("--> Process <%s> started: fetching and sending scheduled participant messages...", threadID)
	var wg sync.WaitGroup
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("GetAllInstances: %v", err)
	}
	for _, instance := range instances {
		wg.Add(1)
		go bulk_messages.GenerateParticipantMessages(
			clients,
			mdb,
			instance.InstanceID,
			fmt.Sprintf("`%s`", instance.InstanceID),
			&wg,
		)
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: fetching and sending scheduled participant messages.", threadID)
}

func handleResearcherNotifications(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, clients *types.APIClients) {
	threadID := generateThreadID("RN")
	logger.Info.Printf("--> Process <%s> started: fetching and sending researcher notifications", threadID)

	var wg sync.WaitGroup
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("GetAllInstances: %v", err)
	}
	for _, instance := range instances {
		wg.Add(1)
		go bulk_messages.GenerateResearcherNotificationMessages(
			clients,
			mdb,
			instance.InstanceID,
			fmt.Sprintf("Schedule for researcher notifications for `%s`", instance.InstanceID),
			&wg,
		)
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: fetching and sending researcher notifications", threadID)
}

func generateThreadID(threadName string) string {
	newID := uuid.New().String()
	threadID := threadName + "-" + newID[:8]
	return threadID
}
