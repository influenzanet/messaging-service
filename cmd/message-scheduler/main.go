package main

import (
	"context"
	"errors"
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
	waClient "github.com/influenzanet/messaging-service/pkg/http/clients"
	"github.com/influenzanet/messaging-service/pkg/types"
)

const (
	outgoingBatchSize       = 20
	maxWhatsAppSendAttempts = 5
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
		WhatsApp                int
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

	wa := 0
	waStr := os.Getenv("MESSAGE_SCHEDULER_INTERVAL_WHATSAPP")
	if waStr != "" {
		wa, err = strconv.Atoi(waStr)
		if err != nil {
			logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_INTERVAL_WHATSAPP: %v", err)
		}
	}

	conf.LogLevel = config.GetLogLevel()

	conf.Frequencies = struct {
		HighPrio                int
		LowPrio                 int
		AutoMessage             int
		ParticipantMessages     int
		ResearcherNotifications int
		WhatsApp                int
	}{
		HighPrio:                hp,
		LowPrio:                 lp,
		AutoMessage:             am,
		ParticipantMessages:     pm,
		ResearcherNotifications: rn,
		WhatsApp:                wa,
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

	// WhatsApp client for direct HTTP delivery (optional, like C-3 pattern)
	whatsAppClient := waClient.NewWhatsAppClient(
		os.Getenv("WHATSAPP_TOKEN"),
		os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		os.Getenv("WHATSAPP_API_VERSION"),
	)
	if whatsAppClient != nil {
		logger.Info.Println("WhatsApp direct delivery enabled for message-scheduler")
	} else {
		logger.Warning.Println("WhatsApp direct delivery disabled: missing WHATSAPP_TOKEN or WHATSAPP_PHONE_NUMBER_ID")
	}

	// WHATSAPP_ENABLED is read once by pkg/bulk_messages: take the value the generators use,
	// so that delivery cannot keep running on a channel that no longer generates messages.
	whatsAppEnabled := bulk_messages.WhatsAppGenerationEnabled()

	// Generating WhatsApp messages this process cannot deliver only fills the queue: refuse
	// generation instead, and name what is missing. Never fatal, the e-mail channel goes on.
	generateWhatsApp, whatsAppProblems := checkWhatsAppConfig(whatsAppEnabled, whatsAppClient != nil, conf.Frequencies.WhatsApp)
	for _, problem := range whatsAppProblems {
		logger.Error.Println(problem)
	}
	if !generateWhatsApp && whatsAppEnabled {
		logger.Error.Println("WhatsApp message generation is disabled in the message-scheduler until the WhatsApp delivery configuration is complete")
		bulk_messages.SetWhatsAppGenerationEnabled(false)
	}

	go runnerForLowPrioOutgoingEmails(messageDBService, globalDBService, clients, conf.Frequencies.LowPrio)
	go runnerForAutoMessages(messageDBService, globalDBService, clients, conf.Frequencies.AutoMessage)
	go runnerForParticipantMessages(messageDBService, globalDBService, clients, conf.Frequencies.ParticipantMessages)
	go runnerForResearcherNotifications(messageDBService, globalDBService, clients, conf.Frequencies.ResearcherNotifications)
	go runnerForOutgoingWhatsApp(messageDBService, globalDBService, whatsAppClient, conf.Frequencies.WhatsApp, whatsAppEnabled)
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
		lastFetch := time.Now().Unix()

		for _, email := range emails {
			batchDuration := time.Now().Unix() - lastFetch
			if batchDuration > int64(float64(lastAttemptOlderThan)*0.9) {
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
	logger.Info.Printf("[%s] Finished processing %d messages%s in %d s.", instanceID, counters.Success, prioText, counters.Duration)
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
			// The scheduler only advances the schedule of a message it read from the database, so
			// it never carries an intent to change the WhatsApp binding.
			_, err := mdb.SaveAutoMessage(instance.InstanceID, messageDef, true)
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

func runnerForOutgoingWhatsApp(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, wac *waClient.WhatsAppClient, freq int, enabled bool) {
	if run, reason := shouldRunOutgoingWhatsApp(enabled, freq, wac != nil); !run {
		logger.Warning.Printf("outgoing whatsapp runner not started: %s. Messages already in outgoing-whatsapp stay queued and are not delivered.", reason)
		return
	}
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("outgoing whatsapp", period)

	olderThan := getThreadLockInterval(freq)
	for {
		go handleOutgoingWhatsApp(mdb, gdb, wac, olderThan)
		time.Sleep(period)
	}
}

func handleOutgoingWhatsApp(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, wac *waClient.WhatsAppClient, lastAttemptOlderThan int64) {
	threadID := generateThreadID("WA")
	logger.Info.Printf("--> Process <%s> started: fetching and sending outgoing whatsapp messages...", threadID)

	var wg sync.WaitGroup
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("%v", err)
	}
	for _, instance := range instances {
		wg.Add(1)
		go handleOutgoingWhatsAppForInstance(mdb, instance.InstanceID, wac, lastAttemptOlderThan, &wg)
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: fetching and sending outgoing whatsapp messages", threadID)
}

type whatsAppSender interface {
	SendTemplateMessage(context.Context, string, string, string, map[string]string) error
}

// deliveredWhatsApp is the outcome of a message Meta accepted.
func deliveredWhatsApp() types.WhatsAppSendOutcome {
	return types.WhatsAppSendOutcome{Status: types.WhatsAppStatusDelivered}
}

// failedWhatsApp is the outcome of a message the scheduler gives up on: Meta's numeric code and
// the class the retry policy read from the last failure. A failure that never reached Meta, or
// that the client could not attribute, keeps code 0 and the unknown class. Meta's free-form
// message is never stored.
func failedWhatsApp(err error) types.WhatsAppSendOutcome {
	outcome := types.WhatsAppSendOutcome{
		Status:     types.WhatsAppStatusFailed,
		ErrorClass: waClient.WhatsAppErrorUnknown.String(),
	}
	var sendErr *waClient.WhatsAppSendError
	if errors.As(err, &sendErr) {
		outcome.ErrorCode = sendErr.Code
		outcome.ErrorClass = sendErr.Class().String()
	}
	return outcome
}

// archiveWhatsApp records the outcome of a message in sent-whatsapp and removes it from the
// queue. The archive row is written first and the queue row deleted only once it is stored: a
// message still in the queue can be finished by a later tick, a lost archive row cannot be
// recovered. It returns the error of the archive write, already logged.
func archiveWhatsApp(mdb *messagedb.MessageDBService, instanceID string, msg types.OutgoingWhatsApp, outcome types.WhatsAppSendOutcome) error {
	if _, err := mdb.AddToSentWhatsApp(instanceID, msg, outcome); err != nil {
		logger.Error.Printf("Error archiving whatsapp ('%s') in instance %s as %s: %v", msg.MessageType, instanceID, outcome.Status, err)
		return err
	}
	if err := mdb.DeleteOutgoingWhatsApp(instanceID, msg.ID.Hex()); err != nil {
		logger.Error.Printf("Error deleting outgoing whatsapp ('%s') in instance %s: %v", msg.MessageType, instanceID, err)
	}
	return nil
}

// recordFailedWhatsAppAttempt charges one failed attempt to the message: it is archived with
// the failure that exhausted it once the attempts are spent, otherwise the counter moves and
// the lock provides the backoff.
func recordFailedWhatsAppAttempt(mdb *messagedb.MessageDBService, instanceID string, msg types.OutgoingWhatsApp, cause error) {
	if msg.SendAttempt+1 >= maxWhatsAppSendAttempts {
		// Permanently failed — archive and remove from queue.
		logger.Warning.Printf("WhatsApp message '%s' in instance %s exceeded max attempts (%d), archiving", msg.MessageType, instanceID, maxWhatsAppSendAttempts)
		_ = archiveWhatsApp(mdb, instanceID, msg, failedWhatsApp(cause))
		return
	}
	// Increment counter; lastSendAttempt stays at lock value, providing natural backoff.
	if incErr := mdb.IncrementSendAttemptForOutgoingWhatsApp(instanceID, msg.ID.Hex()); incErr != nil {
		logger.Error.Printf("Error incrementing sendAttempt for whatsapp ('%s') in instance %s: %v", msg.MessageType, instanceID, incErr)
	}
}

func handleOutgoingWhatsAppForInstance(mdb *messagedb.MessageDBService, instanceID string, wac whatsAppSender, lastAttemptOlderThan int64, wg *sync.WaitGroup) {
	defer wg.Done()
	counters := types.InitMessageCounter()

	// A transient failure on one message is not proof of an outage: a timeout or a "retry
	// later" from Meta can belong to that message alone. Such a failure is held until the next
	// message that gets an answer from the sender: a second transient failure in a row means an
	// outage and stops the tick with nothing charged; anything else means the held message was
	// at fault and it is charged like any other failure. A failure left undecided at the end
	// of the tick, or whose claim has meanwhile expired, is not charged.
	var undecided *types.OutgoingWhatsApp
	var undecidedErr error
	var undecidedClaimedAt int64
	chargeUndecided := func() {
		if undecided == nil {
			return
		}
		if time.Now().Unix()-undecidedClaimedAt > int64(float64(lastAttemptOlderThan)*0.9) {
			// The claim may have expired and the message be back in the queue under a new one:
			// charging this copy could archive it while the other is being sent.
			logger.Warning.Printf("[%s] WhatsApp message '%s' held too long to be decided, leaving it uncharged", instanceID, undecided.MessageType)
		} else {
			recordFailedWhatsAppAttempt(mdb, instanceID, *undecided, undecidedErr)
		}
		undecided = nil
		undecidedErr = nil
	}
processQueue:
	for {
		// Taken before the claims are written: the guard on a held failure must measure from
		// the earliest moment one of this batch's claims can have been stored.
		claimStart := time.Now().Unix()
		messages, err := mdb.FetchOutgoingWhatsApp(instanceID, outgoingBatchSize, lastAttemptOlderThan)
		if err != nil {
			logger.Error.Printf("%s: %v", instanceID, err)
			break
		}
		if len(messages) < 1 {
			break
		}
		lastFetch := time.Now().Unix()

		for _, msg := range messages {
			batchDuration := time.Now().Unix() - lastFetch
			if batchDuration > int64(float64(lastAttemptOlderThan)*0.9) {
				// Lock expires naturally after olderThan — no reset needed.
				logger.Warning.Printf("Skip sending whatsapp ('%s') in instance %s because batch duration was too long", msg.MessageType, instanceID)
				counters.IncreaseCounter(false)
				continue
			}

			if msg.Delivered {
				// Meta accepted this message on an earlier tick and only the archive write
				// failed. Sending it again would post it twice: finish the archive instead.
				logger.Warning.Printf("[%s] WhatsApp message '%s' was already delivered, archiving it instead of sending it again", instanceID, msg.MessageType)
				if err := archiveWhatsApp(mdb, instanceID, msg, deliveredWhatsApp()); err == nil {
					counters.IncreaseCounter(true)
				}
				continue
			}

			// Direct HTTP call to Meta API (C-5 fix: eliminates gRPC hop, adds timeout via context)
			sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := wac.SendTemplateMessage(sendCtx, msg.ToPhoneNumber, msg.TemplateName, msg.Lang, msg.ContentParams)
			cancel()
			if err != nil {
				logger.Error.Printf("Could not send whatsapp ('%s') in instance %s (attempt %d): %v", msg.MessageType, instanceID, msg.SendAttempt+1, err)
				counters.IncreaseCounter(false)

				var sendErr *waClient.WhatsAppSendError
				if errors.As(err, &sendErr) {
					switch class := sendErr.Class(); class {
					case waClient.WhatsAppErrorAuth, waClient.WhatsAppErrorThrottled, waClient.WhatsAppErrorTemplate:
						// Nothing a single message can cause: a sender/API outage or a paused
						// template must not exhaust the whole queue's retries. Keep all claimed
						// locks until expiry; stop this instance's tick.
						logger.Error.Printf("[%s] stopping the WhatsApp tick on a sender-side failure (%s) at '%s'; nothing charged, locks left to expire", instanceID, class, msg.MessageType)
						if undecided != nil {
							logger.Warning.Printf("[%s] WhatsApp message '%s' was held as a transient failure and is left undecided by the stop", instanceID, undecided.MessageType)
						}
						break processQueue
					case waClient.WhatsAppErrorTransient:
						if undecided != nil {
							logger.Error.Printf("[%s] stopping the WhatsApp tick: two consecutive transient failures ('%s', '%s'); nothing charged, locks left to expire", instanceID, undecided.MessageType, msg.MessageType)
							break processQueue
						}
						held := msg
						undecided = &held
						undecidedErr = err
						undecidedClaimedAt = claimStart
						continue
					case waClient.WhatsAppErrorRecipientThrottled:
						// Keep this message's lock, but let other recipients proceed.
						continue
					}
				}

				chargeUndecided()
				recordFailedWhatsAppAttempt(mdb, instanceID, msg, err)
				continue
			}

			chargeUndecided()
			// Meta has accepted the message: record that on the queued row before archiving
			// it, so that a failed archive write leaves a row the next tick finishes rather
			// than one it sends again.
			if markErr := mdb.MarkOutgoingWhatsAppDelivered(instanceID, msg.ID.Hex()); markErr != nil {
				if errors.Is(markErr, messagedb.ErrOutgoingWhatsAppNotFound) {
					// The claim expired and another tick has already archived this message.
					logger.Warning.Printf("[%s] WhatsApp message '%s' left the queue while it was being sent, leaving the archive to the tick that owns it", instanceID, msg.MessageType)
					continue
				}
				// The mark is insurance against a failing archive write. Archive anyway: a
				// delivered message left in the queue is worse than an unmarked one.
				logger.Error.Printf("[%s] Error marking whatsapp ('%s') as delivered: %v", instanceID, msg.MessageType, markErr)
			}
			if err := archiveWhatsApp(mdb, instanceID, msg, deliveredWhatsApp()); err != nil {
				continue
			}
			counters.IncreaseCounter(true)
		}
	}
	counters.Stop()
	logger.Info.Printf("[%s] Finished processing %d whatsapp messages in %d s.", instanceID, counters.Success, counters.Duration)
}

func generateThreadID(threadName string) string {
	newID := uuid.New().String()
	threadID := threadName + "-" + newID[:8]
	return threadID
}
