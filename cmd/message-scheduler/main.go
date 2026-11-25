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
	// whatsAppSendTimeout is how long a send can keep running past the moment its claim was
	// checked. It is the client's own HTTP timeout, so the claim window cannot be computed
	// from a figure the client no longer uses.
	whatsAppSendTimeout = waClient.WhatsAppHTTPTimeout
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
	// WhatsAppTemplateRetryDelay is how long, in seconds, the messages of a template Meta
	// refuses wait before they are tried again.
	WhatsAppTemplateRetryDelay int64
	// WhatsAppMaxAge is how long, in seconds, a message may wait in the queue: an older one is
	// archived as expired and never sent.
	WhatsAppMaxAge  int64
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

	conf.WhatsAppTemplateRetryDelay, err = parseWhatsAppTemplateRetryDelay(os.Getenv("MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_WHATSAPP_TEMPLATE_RETRY_DELAY: %v", err)
	}
	conf.WhatsAppMaxAge, err = parseWhatsAppMaxAge(os.Getenv("MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE"))
	if err != nil {
		logger.Error.Fatalf("cannot parse MESSAGE_SCHEDULER_WHATSAPP_MAX_AGE: %v", err)
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
	ensureWhatsAppIndexes(messageDBService, globalDBService)

	// Single place that decides "is WhatsApp on?" for this process; the per-instance half of
	// that decision (Enabled) asks user-management through clients.UserManagementService.
	whatsAppSettings := config.WhatsAppSettingsFromEnv(clients.UserManagementService)
	config.LogWhatsAppStartup("message-scheduler", whatsAppSettings)

	// WhatsApp client for direct HTTP delivery (optional)
	whatsAppClient := waClient.NewWhatsAppClient(
		whatsAppSettings.Token(),
		whatsAppSettings.PhoneNumberID(),
		whatsAppSettings.APIVersion(),
	)
	if whatsAppClient != nil {
		logger.Info.Println("WhatsApp direct delivery enabled for message-scheduler")
	} else {
		logger.Warning.Println("WhatsApp direct delivery disabled: missing WHATSAPP_TOKEN or WHATSAPP_PHONE_NUMBER_ID")
	}

	// At start-up no instance is known yet, so there is nothing to ask user-management about:
	// CanSend is the local-only precondition (kill switch plus this process's own Meta
	// credentials), not the per-instance decision. The delivery loop below asks the
	// per-instance question, Enabled(instance.InstanceID), once instances are known.
	whatsAppEnabled := whatsAppSettings.CanSend()

	// Generating WhatsApp messages this process cannot deliver only fills the queue: refuse
	// generation instead, and name what is missing. Never fatal, the e-mail channel goes on.
	generateWhatsApp, whatsAppProblems := checkWhatsAppConfig(whatsAppEnabled, whatsAppClient != nil, conf.Frequencies.WhatsApp)
	for _, problem := range whatsAppProblems {
		logger.Error.Println(problem)
	}
	if generateWhatsApp {
		bulk_messages.Configure(whatsAppSettings)
	} else {
		// Same effect as before: a disabled settings value stops the generators from
		// queueing WhatsApp messages this process could not deliver anyway.
		bulk_messages.Configure(config.WhatsAppSettings{})
		if whatsAppEnabled {
			logger.Error.Println("WhatsApp message generation is disabled in the message-scheduler until the WhatsApp delivery configuration is complete")
		}
	}

	go runnerForLowPrioOutgoingEmails(messageDBService, globalDBService, clients, conf.Frequencies.LowPrio)
	go runnerForAutoMessages(messageDBService, globalDBService, clients, conf.Frequencies.AutoMessage)
	go runnerForParticipantMessages(messageDBService, globalDBService, clients, conf.Frequencies.ParticipantMessages)
	go runnerForResearcherNotifications(messageDBService, globalDBService, clients, conf.Frequencies.ResearcherNotifications)
	go runnerForOutgoingWhatsApp(messageDBService, globalDBService, whatsAppClient, conf.Frequencies.WhatsApp, whatsAppEnabled, conf.WhatsAppTemplateRetryDelay, conf.WhatsAppMaxAge, whatsAppSettings)
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
				// Only this schedule has ended: the other schedules and instances still run.
				continue
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

func runnerForOutgoingWhatsApp(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, wac *waClient.WhatsAppClient, freq int, enabled bool, templateRetryDelay int64, maxAge int64, settings config.WhatsAppSettings) {
	if run, reason := shouldRunOutgoingWhatsApp(enabled, freq, wac != nil); !run {
		if settings.TurnedOffByKillSwitch() {
			logger.Warning.Printf("outgoing whatsapp runner not started: %s. Messages already in outgoing-whatsapp stay queued and are not delivered.", reason)
			return
		}
		// WhatsApp is not turned off here, so user-management may enable it for an instance and
		// messages be queued that nothing will ever send.
		logWhatsAppAlert(alertRunnerNotStarted, nil, fmt.Sprintf("outgoing whatsapp runner not started: %s. Messages already in outgoing-whatsapp stay queued and are not delivered; set WHATSAPP_ENABLED=false where WhatsApp is not used.", reason))
		return
	}
	period := time.Duration(freq) * time.Second
	logInitialLoopStartedMsg("outgoing whatsapp", period)

	if problem := whatsAppLockProblem(freq, whatsAppSendTimeout); problem != "" {
		logger.Warning.Printf("outgoing whatsapp: %s", problem)
	}
	if problem := whatsAppMaxAgeProblem(maxAge, templateRetryDelay); problem != "" {
		logger.Warning.Printf("outgoing whatsapp: %s", problem)
	}

	olderThan := getThreadLockInterval(freq)
	for {
		go handleOutgoingWhatsApp(mdb, gdb, wac, olderThan, templateRetryDelay, maxAge, settings)
		time.Sleep(period)
	}
}

func handleOutgoingWhatsApp(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService, wac *waClient.WhatsAppClient, lastAttemptOlderThan int64, templateRetryDelay int64, maxAge int64, settings config.WhatsAppSettings) {
	threadID := generateThreadID("WA")
	logger.Info.Printf("--> Process <%s> started: fetching and sending outgoing whatsapp messages...", threadID)

	var wg sync.WaitGroup
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("%v", err)
	}
	for _, instance := range instances {
		// Per-instance decision shared with the generators: on top of the local
		// precondition that already gated this runner, user-management may turn WhatsApp
		// off for a specific instance, or lack credentials this process needs may turn it
		// off here even where user-management allows it.
		if !settings.Enabled(instance.InstanceID) {
			logWhatsAppOffInstance(mdb, instance.InstanceID)
			continue
		}
		// Asked again before every send: a tick can drain the queue for far longer than the
		// cache window, and turning WhatsApp off must stop it too, not only the ticks after it.
		instanceID := instance.InstanceID
		whatsAppOn := func() bool { return settings.Enabled(instanceID) }
		wg.Add(1)
		go handleOutgoingWhatsAppForInstance(mdb, instanceID, wac, whatsAppOn, lastAttemptOlderThan, templateRetryDelay, maxAge, &wg)
	}
	wg.Wait()
	logger.Info.Printf("<-- Process <%s> finished: fetching and sending outgoing whatsapp messages", threadID)
}

// logWhatsAppOffInstance reports an instance the tick skips because WhatsApp is off for it. An
// instance that never uses WhatsApp is skipped on every tick, so with nothing queued the line is
// Debug only; a warning there would come once a tick for every such instance and bury the
// alerts. Messages waiting in its queue are never sent while WhatsApp stays off, which an
// operator has to see: that, and a queue that cannot be read, are warnings.
func logWhatsAppOffInstance(mdb *messagedb.MessageDBService, instanceID string) {
	queued, err := mdb.HasOutgoingWhatsApp(instanceID)
	switch {
	case err != nil:
		logger.Warning.Printf("[%s] WhatsApp is not enabled for this instance, and its outgoing-whatsapp queue cannot be read: %v", instanceID, err)
	case queued:
		logger.Warning.Printf("[%s] WhatsApp is not enabled for this instance, its outgoing messages stay queued", instanceID)
	default:
		logger.Debug.Printf("[%s] WhatsApp is not enabled for this instance, nothing queued", instanceID)
	}
}

// whatsAppSender sends a template message and returns the id Meta gave it, which may be empty
// on an accepted message.
type whatsAppSender interface {
	SendTemplateMessage(context.Context, string, string, string, map[string]string) (string, error)
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
//
// A failed archive write is an alert: the webhook matches Meta's delivery statuses on the
// archive only, so the statuses of a message that waits in the queue for its archive are
// acknowledged and not recorded, a 131026 included (accepted, see "Delivery statuses and
// replies" in docs/whatsapp-scheduler.md). The cause is logged on a line of its own, since a
// database error can quote the values of the document it refused.
func archiveWhatsApp(mdb *messagedb.MessageDBService, instanceID string, msg types.OutgoingWhatsApp, outcome types.WhatsAppSendOutcome) error {
	if _, err := mdb.AddToSentWhatsApp(instanceID, msg, outcome); err != nil {
		logWhatsAppAlert(alertArchiveWriteFailed, []alertField{
			{"instance", instanceID}, {"type", msg.MessageType}, {"status", outcome.Status},
		}, "WhatsApp message could not be archived: it stays queued for a later tick, and the delivery statuses Meta reports for it until then are not recorded")
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
		outcome := failedWhatsApp(cause)
		if archiveWhatsApp(mdb, instanceID, msg, outcome) == nil {
			logWhatsAppAlert(alertArchivedFailed, []alertField{
				{"instance", instanceID}, {"type", msg.MessageType}, {"template", msg.TemplateName}, {"lang", msg.Lang},
				{"code", outcome.ErrorCode}, {"class", outcome.ErrorClass}, {"attempts", maxWhatsAppSendAttempts},
			}, "WhatsApp message archived as failed: it used up its attempts")
		}
		return
	}
	// Increment counter; lastSendAttempt stays at lock value, providing natural backoff.
	if incErr := mdb.IncrementSendAttemptForOutgoingWhatsApp(instanceID, msg.ID.Hex()); incErr != nil {
		logger.Error.Printf("Error incrementing sendAttempt for whatsapp ('%s') in instance %s: %v", msg.MessageType, instanceID, incErr)
	}
}

// markHandledWhatsApp records the messages of a fetch and reports the first one this tick has
// already taken out of an earlier fetch. A message handed out twice means the pass over the
// queue outlived the claims written at its start: the rows come back unchanged, so nothing the
// tick can do this time round will move them, whatever kept them in the queue. It holds one
// entry per message the tick has processed, and lives no longer than the tick.
func markHandledWhatsApp(messages []types.OutgoingWhatsApp, handled map[string]bool) (types.OutgoingWhatsApp, bool) {
	for _, msg := range messages {
		id := msg.ID.Hex()
		if handled[id] {
			return msg, true
		}
		handled[id] = true
	}
	return types.OutgoingWhatsApp{}, false
}

// warnAboutHeldWhatsApp reports that a stop leaves a held transient failure undecided. Such a
// message is not charged, which is the intent, but it has to be visible: it was sent to Meta
// and failed, and nothing else in the log says what became of it.
func warnAboutHeldWhatsApp(instanceID string, undecided *types.OutgoingWhatsApp) {
	if undecided == nil {
		return
	}
	logger.Warning.Printf("[%s] WhatsApp message '%s' was held as a transient failure and is left undecided by the stop", instanceID, undecided.MessageType)
}

// pauseWhatsAppAfterThrottle stores in the database that Meta limited the sender, so that every
// scheduler of the instance, this process or another, stays away until the wait is over. The
// wait grows with the throttles in a row, honours the Retry-After of the answer, and is never
// shorter than the claim lock of the messages left unsent by the stop.
func pauseWhatsAppAfterThrottle(mdb *messagedb.MessageDBService, instanceID string, sendErr *waClient.WhatsAppSendError, lastAttemptOlderThan int64, knownLevel int) {
	level := knownLevel
	if state, err := mdb.GetWhatsAppThrottle(instanceID); err != nil {
		logger.Error.Printf("[%s] cannot read the WhatsApp throttle state, backing off from level %d: %v", instanceID, level, err)
	} else {
		level = state.Level
	}
	delay := whatsAppThrottleDelay(level, sendErr.RetryAfter, time.Duration(lastAttemptOlderThan)*time.Second, randomJitter())
	now := time.Now()
	notBefore := whatsAppThrottleNotBefore(now, delay)
	state, err := mdb.RecordWhatsAppThrottle(instanceID, notBefore, now.Unix())
	if err != nil {
		logWhatsAppAlert(alertThrottlePause, []alertField{
			{"instance", instanceID}, {"code", sendErr.Code}, {"stored", false},
		}, fmt.Sprintf("Meta is limiting WhatsApp sending (%s) but the pause cannot be stored, the next tick will try again at its normal pace: %v", sendErr.Class(), err))
		return
	}
	logWhatsAppAlert(alertThrottlePause, []alertField{
		{"instance", instanceID}, {"code", sendErr.Code}, {"pause_s", int64(delay / time.Second)},
		{"until", time.Unix(state.NotBefore, 0).UTC().Format(time.RFC3339)}, {"in_a_row", state.Level},
	}, fmt.Sprintf("Meta is limiting WhatsApp sending (%s, retry-after %d s): sending paused", sendErr.Class(), int64(sendErr.RetryAfter/time.Second)))
}

// handleOutgoingWhatsAppForInstance drains the instance's queue. whatsAppOn reports whether
// WhatsApp is still on for the instance: it is asked before every call to Meta, and once it
// answers no the tick stops, so turning WhatsApp off reaches a tick already under way within
// the cache window of the answer, plus at most the one send already in flight.
func handleOutgoingWhatsAppForInstance(mdb *messagedb.MessageDBService, instanceID string, wac whatsAppSender, whatsAppOn func() bool, lastAttemptOlderThan int64, templateRetryDelay int64, maxAge int64, wg *sync.WaitGroup) {
	defer wg.Done()
	// However the tick ends, paused included, it states what it leaves in the queue.
	defer func() { reportWhatsAppQueue(mdb, instanceID, time.Now().Unix()) }()

	// While Meta limits the sender nothing is claimed or sent: the wait is stored in the
	// database, so it binds every scheduler of the instance and survives a restart.
	throttle, throttleErr := mdb.GetWhatsAppThrottle(instanceID)
	if throttleErr != nil {
		logger.Error.Printf("[%s] cannot read the WhatsApp throttle state, sending as usual: %v", instanceID, throttleErr)
	} else if time.Now().Unix() < throttle.NotBefore {
		logger.Warning.Printf("[%s] WhatsApp sending paused until %s after Meta throttling", instanceID, time.Unix(throttle.NotBefore, 0).UTC().Format(time.RFC3339))
		return
	}
	// Set while the stored level is above zero: the first accepted message resets it.
	throttleToReset := throttle.Level > 0

	counters := types.InitMessageCounter()
	// Messages this tick archived as expired: they are not sent, so they are not counted with
	// the messages it tried to send.
	expired := 0

	// How long after this batch was claimed a send may still start.
	sendWindow := whatsAppSendWindow(lastAttemptOlderThan, whatsAppSendTimeout)

	// Every message this tick has already had out of a fetch.
	handled := map[string]bool{}

	// The templates (name and language) Meta refused in this tick: their messages have been
	// deferred and are not sent again before the retry delay.
	refusedTemplates := map[string]bool{}

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
	for batch := 0; ; batch++ {
		// A tick that is already past the first check stops as soon as any replica has stored a
		// pause; the first batch uses the state read at the start. A read error fails open.
		if batch > 0 {
			if current, readErr := mdb.GetWhatsAppThrottle(instanceID); readErr != nil {
				logger.Error.Printf("[%s] cannot read the WhatsApp throttle state, sending as usual: %v", instanceID, readErr)
			} else if time.Now().Unix() < current.NotBefore {
				logger.Warning.Printf("[%s] WhatsApp sending paused until %s after Meta throttling, stopping the tick", instanceID, time.Unix(current.NotBefore, 0).UTC().Format(time.RFC3339))
				warnAboutHeldWhatsApp(instanceID, undecided)
				break processQueue
			}
		}
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
		if repeated, again := markHandledWhatsApp(messages, handled); again {
			// The queue is handing back what this tick has already worked on: messages that
			// keep their claim and their attempt count, such as a recipient Meta is limiting,
			// an archive write that keeps failing or a held transient failure, would be sent
			// again and again inside this one tick. They are left for the next one.
			logger.Error.Printf("[%s] stopping the WhatsApp tick: WhatsApp message '%s' was fetched again after this tick had already processed it, so the pass over the queue outlived its claims and nothing is moving", instanceID, repeated.MessageType)
			warnAboutHeldWhatsApp(instanceID, undecided)
			break processQueue
		}
		for position, msg := range messages {
			// Measured from before the claim loop: that is the earliest moment a claim of
			// this batch can have been written, so no message is sent on a claim that is
			// older than the guard believes.
			claimAge := time.Now().Unix() - claimStart
			if claimAge > sendWindow {
				// Lock expires naturally after olderThan — no reset needed.
				counters.IncreaseCounter(false)
				if position == 0 {
					// Claiming the batch alone spent the window: every message of this batch
					// would be skipped, nothing would change, and the next fetch would claim
					// the same rows again, so the tick would never end. Stop and leave the
					// queue to the next tick, which starts from a fresh claim.
					logger.Error.Printf("[%s] stopping the WhatsApp tick: claiming the batch took %d s, longer than the %d s send window left by a lock of %d s and a send timeout of %s", instanceID, claimAge, sendWindow, lastAttemptOlderThan, whatsAppSendTimeout)
					warnAboutHeldWhatsApp(instanceID, undecided)
					break processQueue
				}
				logger.Warning.Printf("Skip sending whatsapp ('%s') in instance %s: its claim is %d s old and the send window is %d s", msg.MessageType, instanceID, claimAge, sendWindow)
				// Nothing was done to this message, so the fetch that hands it back once its
				// claim expires is a legitimate one and must not stop the tick: only messages
				// this tick acted on count as handled.
				delete(handled, msg.ID.Hex())
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

			if whatsAppMessageExpired(msg.AddedAt, time.Now().Unix(), maxAge) {
				// Waited too long to be of use: archived unsent, without asking Meta. Nothing
				// was sent, so a held failure is neither decided nor affected by it.
				if expireWhatsApp(mdb, instanceID, msg) {
					expired++
				}
				continue
			}

			if refusedTemplates[whatsAppTemplateKey(msg)] {
				// Already deferred with the rest of its template when Meta refused it.
				counters.IncreaseCounter(false)
				continue
			}

			// Checked here, after the branches that never call Meta, so that it only ever holds
			// back a send. Nothing is sent or charged from here on: the claims of this batch are
			// left to expire, and a held failure is left undecided, as after any other stop.
			if !whatsAppOn() {
				logger.Warning.Printf("[%s] stopping the WhatsApp tick: WhatsApp was turned off for this instance while it ran; the messages left stay queued", instanceID)
				warnAboutHeldWhatsApp(instanceID, undecided)
				break processQueue
			}

			// Direct HTTP call to Meta API, bounded by the context timeout
			sendCtx, cancel := context.WithTimeout(context.Background(), whatsAppSendTimeout)
			wamid, err := wac.SendTemplateMessage(sendCtx, msg.ToPhoneNumber, msg.TemplateName, msg.Lang, msg.ContentParams)
			cancel()
			if err != nil {
				logger.Error.Printf("Could not send whatsapp ('%s') in instance %s (attempt %d): %v", msg.MessageType, instanceID, msg.SendAttempt+1, err)
				counters.IncreaseCounter(false)

				var sendErr *waClient.WhatsAppSendError
				if errors.As(err, &sendErr) {
					switch class := sendErr.Class(); class {
					case waClient.WhatsAppErrorAuth, waClient.WhatsAppErrorThrottled:
						// Nothing a single message can cause: a sender/API outage must not
						// exhaust the whole queue's retries. Keep all claimed locks until
						// expiry; stop this instance's tick.
						if class == waClient.WhatsAppErrorThrottled {
							// The pause is at least as long as the locks, so they have expired
							// when sending resumes.
							pauseWhatsAppAfterThrottle(mdb, instanceID, sendErr, lastAttemptOlderThan, throttle.Level)
						}
						if class == waClient.WhatsAppErrorAuth {
							logWhatsAppAlert(alertAuthStop, []alertField{
								{"instance", instanceID}, {"status", sendErr.StatusCode}, {"code", sendErr.Code}, {"subcode", sendErr.Subcode},
							}, fmt.Sprintf("stopping the WhatsApp tick: Meta refused the sender (%s) at '%s'; nothing charged, locks left to expire", class, msg.MessageType))
						} else {
							logger.Error.Printf("[%s] stopping the WhatsApp tick on a sender-side failure (%s) at '%s'; nothing charged, locks left to expire", instanceID, class, msg.MessageType)
						}
						warnAboutHeldWhatsApp(instanceID, undecided)
						break processQueue
					case waClient.WhatsAppErrorTemplate:
						// Meta answered, so a held failure is decided like after any answer,
						// unless it belongs to the same template: the deferral charges it then.
						if undecided != nil && whatsAppTemplateKey(*undecided) == whatsAppTemplateKey(msg) {
							undecided = nil
							undecidedErr = nil
						} else {
							chargeUndecided()
						}
						// Only this template is refused: defer its messages and keep going.
						deferWhatsAppTemplate(mdb, instanceID, msg, err, handled, lastAttemptOlderThan, templateRetryDelay)
						refusedTemplates[whatsAppTemplateKey(msg)] = true
						continue
					case waClient.WhatsAppErrorPermanent:
						// Meta answered and will refuse the same request again: a held failure is
						// decided like after any answer, and this message is archived at once.
						chargeUndecided()
						logWhatsAppAlert(alertPermanentFailure, []alertField{
							{"instance", instanceID}, {"type", msg.MessageType}, {"template", msg.TemplateName}, {"lang", msg.Lang}, {"code", sendErr.Code},
						}, "WhatsApp message archived at the first attempt: Meta refused it permanently; retrying cannot fix it")
						if archiveWhatsApp(mdb, instanceID, msg, failedWhatsApp(err)) != nil {
							// The message stays queued: charging it keeps the attempt cap in force,
							// so a database that keeps refusing the archive cannot have it sent to
							// Meta on every tick.
							recordFailedWhatsAppAttempt(mdb, instanceID, msg, err)
						}
						continue
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
						// Meta answered, about this recipient alone: a held failure is decided
						// like after any answer. Keep this message's lock, but let other
						// recipients proceed.
						chargeUndecided()
						continue
					}
				}

				chargeUndecided()
				recordFailedWhatsAppAttempt(mdb, instanceID, msg, err)
				continue
			}

			chargeUndecided()
			if throttleToReset {
				// Meta accepted a message, so the next throttle starts from the shortest wait.
				if resetErr := mdb.ResetWhatsAppThrottleLevel(instanceID, time.Now().Unix()); resetErr != nil {
					logger.Error.Printf("[%s] cannot reset the WhatsApp throttle level: %v", instanceID, resetErr)
				} else {
					throttleToReset = false
				}
			}
			// Meta has accepted the message: record that on the queued row before archiving
			// it, so that a failed archive write leaves a row the next tick finishes rather
			// than one it sends again. The archive keeps Meta's id even when the mark fails.
			msg.WAMID = wamid
			if markErr := mdb.MarkOutgoingWhatsAppDelivered(instanceID, msg.ID.Hex(), wamid); markErr != nil {
				if errors.Is(markErr, messagedb.ErrOutgoingWhatsAppNotFound) {
					// The claim expired and another tick has already archived this message,
					// or the participant withdrew WhatsApp while it was being sent and
					// DeleteOutgoingWhatsAppForUser archived it as cancelled. Accepted: the
					// send cannot be called back once it has started.
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
	if expired > 0 {
		logWhatsAppAlert(alertExpired, []alertField{
			{"instance", instanceID}, {"count", expired}, {"max_age_s", maxAge},
		}, fmt.Sprintf("%d WhatsApp message(s) archived as expired without being sent: they waited in the queue longer than %d s", expired, maxAge))
	}
	logger.Info.Printf("[%s] Finished processing %d whatsapp messages in %d s.", instanceID, counters.Success, counters.Duration)
}

func generateThreadID(threadName string) string {
	newID := uuid.New().String()
	threadID := threadName + "-" + newID[:8]
	return threadID
}
