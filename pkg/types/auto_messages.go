package types

import (
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AutoMessage are predefined message to be sent on condition or at fixed time
// They are also used to send custom messages (all-users, study-particpants)
type AutoMessage struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	Template  EmailTemplate      `bson:"template"`
	Type      string             `bson:"type"` // bulk message type, e.g. "all-users", "scheduled-participant-messages", "researcher-notifications", "study-participants"
	StudyKey  string             `bson:"studyKey,omitempty"`
	Condition *ExpressionArg     `bson:"condition,omitempty"`
	NextTime  int64              `bson:"nextTime"`
	Period    int64              `bson:"period"`
	Label     string             `bson:"label"`
}

func AutoMessageFromAPI(obj *api.AutoMessage) *AutoMessage {
	if obj == nil {
		return nil
	}
	_id, _ := primitive.ObjectIDFromHex(obj.Id)
	return &AutoMessage{
		ID:        _id,
		Template:  EmailTemplateFromAPI(obj.Template),
		Type:      obj.Type,
		StudyKey:  obj.StudyKey,
		Condition: ExpressionArgFromAPI(obj.Condition),
		NextTime:  obj.NextTime,
		Period:    obj.Period,
		Label:     obj.Label,
	}
}

func (obj *AutoMessage) ToAPI() *api.AutoMessage {
	if obj == nil {
		return nil
	}
	return &api.AutoMessage{
		Id:        obj.ID.Hex(),
		Template:  obj.Template.ToAPI(),
		Type:      obj.Type,
		StudyKey:  obj.StudyKey,
		Condition: obj.Condition.ToAPI(),
		NextTime:  obj.NextTime,
		Period:    obj.Period,
		Label:     obj.Label,
	}
}
