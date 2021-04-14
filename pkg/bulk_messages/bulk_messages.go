package bulk_messages

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/templates"
	"github.com/influenzanet/messaging-service/pkg/types"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	umAPI "github.com/influenzanet/user-management-service/pkg/api"
)

const loginTokenLifeTime = 7 * 24 * 60 * 60 // 7 days

func GenerateAutoMessages(
	apiClients *types.APIClients,
	messageDBService *messagedb.MessageDBService,
	instanceID string,
	autoMessage types.AutoMessage,
) {
	switch autoMessage.Type {
	case "all-users":
		GenerateForAllUsers(
			apiClients,
			messageDBService,
			instanceID,
			autoMessage.Template,
		)
	case "study-participants":
		autoMessage.Template.StudyKey = autoMessage.StudyKey
		GenerateForStudyParticipants(
			apiClients,
			messageDBService,
			instanceID,
			autoMessage.Template,
			autoMessage.Condition.ToAPI(),
		)
	default:
		log.Printf("GenerateAutoMessages: message type unknown: %s", autoMessage.Type)
	}
}

func GenerateForAllUsers(
	apiClients *types.APIClients,
	messageDBService *messagedb.MessageDBService,
	instanceID string,
	messageTemplate types.EmailTemplate,
) {
	counters := types.InitMessageCounter()

	currentWeekday := time.Now().Weekday()
	stream, err := getFilteredUserStream(apiClients, instanceID, messageTemplate.MessageType, int32(currentWeekday))
	if err != nil {
		log.Printf("GenerateForAllUsers: %v", err)
		return
	}

	for {
		user, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("%v.GenerateForAllUsers(_) = _, %v", apiClients.UserManagementService, err)
			break
		}

		if !isSubscribed(user, messageTemplate.MessageType) {
			continue
		}

		outgoing, err := prepareOutgoingEmail(
			user,
			apiClients,
			messageDBService,
			instanceID,
			messageTemplate,
		)
		if err != nil {
			counters.IncreaseCounter(false)
			log.Printf("unexpected error: %v", err)
			continue
		}

		_, err = messageDBService.AddToOutgoingEmails(instanceID, *outgoing)
		if err != nil {
			counters.IncreaseCounter(false)
			log.Printf("unexpected error: %v", err)
			continue
		}
		counters.IncreaseCounter(true)
	}
	counters.Stop()
	log.Printf("Generated %d (%d failed) '%s' messages in %d s.", counters.Total, counters.Failed, messageTemplate.MessageType, counters.Duration)
}

func GenerateForStudyParticipants(
	apiClients *types.APIClients,
	messageDBService *messagedb.MessageDBService,
	instanceID string,
	messageTemplate types.EmailTemplate,
	condition *api.ExpressionArg,
) {
	counters := types.InitMessageCounter()

	currentWeekday := time.Now().Weekday()
	stream, err := getFilteredUserStream(apiClients, instanceID, messageTemplate.MessageType, int32(currentWeekday))
	if err != nil {
		log.Printf("GenerateForStudyParticipants: %v", err)
		return
	}

	for {
		user, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("%v.GenerateForStudyParticipants(_) = _, %v", apiClients.UserManagementService, err)
			break
		}

		if !isSubscribed(user, messageTemplate.MessageType) {
			continue
		}

		if err = checkStudyStateForUser(
			user,
			apiClients,
			instanceID,
			messageTemplate.StudyKey,
			condition,
		); err != nil {
			continue
		}

		outgoing, err := prepareOutgoingEmail(
			user,
			apiClients,
			messageDBService,
			instanceID,
			messageTemplate,
		)
		if err != nil {
			counters.IncreaseCounter(false)
			log.Printf("unexpected error: %v", err)
			continue
		}

		_, err = messageDBService.AddToOutgoingEmails(instanceID, *outgoing)
		if err != nil {
			counters.IncreaseCounter(false)
			log.Printf("unexpected error: %v", err)
			continue
		}
		counters.IncreaseCounter(true)
	}
	counters.Stop()
	log.Printf("Generated %d (%d failed) '%s' messages in %d s.", counters.Total, counters.Failed, messageTemplate.MessageType, counters.Duration)
}

func prepareOutgoingEmail(
	user *umAPI.User,
	apiClients *types.APIClients,
	messageDBService *messagedb.MessageDBService,
	instanceID string,
	messageTemplate types.EmailTemplate,

) (*types.OutgoingEmail, error) {
	outgoingEmail := types.OutgoingEmail{
		MessageType:     messageTemplate.MessageType,
		HeaderOverrides: messageTemplate.HeaderOverrides,
		AddedAt:         time.Now().Unix(),
	}
	contentInfos := map[string]string{}

	if user.Account.Type == "email" {
		outgoingEmail.To = []string{user.Account.AccountId}
	} else {
		return nil, fmt.Errorf("account type not supported yet: %s", user.Account.Type)
	}

	if messageTemplate.MessageType == constants.EMAIL_TYPE_NEWSLETTER {
		outgoingEmail.To = getEmailsByIds(user.ContactInfos, user.ContactPreferences.SendNewsletterTo)
		token, err := getUnsubscribeToken(apiClients.UserManagementService, instanceID, user)
		if err != nil {
			return nil, err
		}
		contentInfos["unsubscribeToken"] = token
	} else if messageTemplate.MessageType == constants.EMAIL_TYPE_WEEKLY ||
		messageTemplate.MessageType == constants.EMAIL_TYPE_STUDY_REMINDER {
		token, err := getTemploginToken(apiClients.UserManagementService, instanceID, user, messageTemplate.StudyKey, loginTokenLifeTime)
		if err != nil {
			return nil, err
		}
		contentInfos["loginToken"] = token
		contentInfos["studyKey"] = messageTemplate.StudyKey
	}

	contentInfos["language"] = user.Account.PreferredLanguage
	subject, content, err := generateEmailContent(messageTemplate, user.Account.PreferredLanguage, contentInfos)
	if err != nil {
		return nil, err
	}

	outgoingEmail.Subject = subject
	outgoingEmail.Content = content
	return &outgoingEmail, nil
}

func checkStudyStateForUser(
	user *umAPI.User,
	apiClients *types.APIClients,
	instanceID string,
	studyKey string,
	condition *api.ExpressionArg,
) error {
	profileIDs := make([]string, len(user.Profiles))
	for i, p := range user.Profiles {
		profileIDs[i] = p.Id
	}

	// check if user is in the study with at least one profile
	_, err := apiClients.StudyService.HasParticipantStateWithCondition(context.Background(), &studyAPI.ProfilesWithConditionReq{
		InstanceId: instanceID,
		ProfileIds: profileIDs,
		StudyKey:   studyKey,
		Condition:  expressionArgFromMessageToStudyAPI(condition),
	})
	return err
}

func isSubscribed(user *umAPI.User, messageType string) bool {
	switch messageType {
	case constants.EMAIL_TYPE_WEEKLY:
		return user.ContactPreferences.SubscribedToWeekly
	case constants.EMAIL_TYPE_NEWSLETTER:
		return user.ContactPreferences.SubscribedToNewsletter
	}
	return true
}

func expressionArgFromMessageToStudyAPI(arg *api.ExpressionArg) *studyAPI.ExpressionArg {
	if arg == nil {
		return nil
	}
	newArg := &studyAPI.ExpressionArg{
		Dtype: arg.Dtype,
	}
	switch x := arg.Data.(type) {
	case *api.ExpressionArg_Exp:
		newArg.Data = &studyAPI.ExpressionArg_Exp{Exp: expressionFromMessageToStudyAPI(arg.GetExp())}
	case *api.ExpressionArg_Str:
		newArg.Data = &studyAPI.ExpressionArg_Str{Str: arg.GetStr()}
	case *api.ExpressionArg_Num:
		newArg.Data = &studyAPI.ExpressionArg_Num{Num: arg.GetNum()}
	case nil:
		// The field is not set.
	default:
		log.Printf("api.ExpressionArg has unexpected type %T", x)
	}

	return newArg
}

func expressionFromMessageToStudyAPI(arg *api.Expression) *studyAPI.Expression {
	if arg == nil {
		return nil
	}
	newArg := &studyAPI.Expression{
		Name:       arg.Name,
		ReturnType: arg.ReturnType,
	}
	data := make([]*studyAPI.ExpressionArg, len(arg.Data))
	for i, d := range arg.Data {
		data[i] = expressionArgFromMessageToStudyAPI(d)
	}
	newArg.Data = data
	return newArg
}

func getFilteredUserStream(
	apiClients *types.APIClients,
	instanceID string,
	messageType string,
	weekday int32,
) (umAPI.UserManagementApi_StreamUsersClient, error) {
	var filters *umAPI.StreamUsersMsg_Filters
	if messageType == constants.EMAIL_TYPE_NEWSLETTER ||
		messageType == constants.EMAIL_TYPE_WEEKLY {
		filters = &umAPI.StreamUsersMsg_Filters{
			OnlyConfirmedAccounts:    true,
			UseReminderWeekdayFilter: true,
			ReminderWeekday:          weekday,
		}
	} else if messageType == constants.EMAIL_TYPE_STUDY_REMINDER {
		filters = &umAPI.StreamUsersMsg_Filters{
			OnlyConfirmedAccounts:    true,
			UseReminderWeekdayFilter: false,
		}
	}

	return apiClients.UserManagementService.StreamUsers(context.Background(),
		&umAPI.StreamUsersMsg{
			InstanceId: instanceID,
			Filters:    filters,
		},
	)
}

func getEmailsByIds(contacts []*umAPI.ContactInfo, ids []string) []string {
	emails := []string{}
	for _, c := range contacts {
		if c.Type == "email" {
			for _, id := range ids {
				if c.Id == id {
					emails = append(emails, c.GetEmail())
				}
			}
		}
	}
	return emails
}

func generateEmailContent(temp types.EmailTemplate, prefLang string, contentInfos map[string]string) (subject string, content string, err error) {
	translation := templates.GetTemplateTranslation(temp, prefLang)
	subject = translation.Subject
	decodedTemplate, err := base64.StdEncoding.DecodeString(translation.TemplateDef)
	if err != nil {
		return "", "", err
	}

	// execute template
	content, err = templates.ResolveTemplate(
		temp.MessageType+prefLang,
		string(decodedTemplate),
		contentInfos,
	)
	return
}

func getTemploginToken(
	userClient umAPI.UserManagementApiClient,
	instanceID string,
	user *umAPI.User,
	studyKey string,
	expiresIn int64,
) (token string, err error) {
	resp, err := userClient.GenerateTempToken(context.Background(), &api_types.TempTokenInfo{
		UserId:     user.Id,
		Purpose:    constants.TOKEN_PURPOSE_SURVEY_LOGIN,
		InstanceId: instanceID,
		Expiration: time.Now().Unix() + expiresIn,
		Info: map[string]string{
			"studyKey": studyKey,
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Token, nil
}

func getUnsubscribeToken(
	userClient umAPI.UserManagementApiClient,
	instanceID string,
	user *umAPI.User,
) (token string, err error) {
	resp, err := userClient.GetOrCreateTemptoken(context.Background(), &api_types.TempTokenInfo{
		UserId:     user.Id,
		Purpose:    constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER,
		InstanceId: instanceID,
		Expiration: time.Now().Unix() + 157680000,
	})
	if err != nil {
		return "", err
	}
	return resp.Token, nil
}
