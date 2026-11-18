package types

import "go.mongodb.org/mongo-driver/bson/primitive"

type OutgoingWhatsApp struct {
	ID              primitive.ObjectID `bson:"_id,omitempty"`
	MessageType     string             `bson:"messageType"`
	ToPhoneNumber   string             `bson:"toPhoneNumber"`
	TemplateName    string             `bson:"templateName"`
	Lang            string             `bson:"lang"`
	ContentParams   map[string]string  `bson:"contentParams,omitempty"`
	UserID          string             `bson:"userId,omitempty"`
	AddedAt         int64              `bson:"addedAt"`
	LastSendAttempt int64              `bson:"lastSendAttempt"`
	SendAttempt     int                `bson:"sendAttempt"`
	// Delivered records that Meta accepted the message, before it is archived. A row that
	// carries it is waiting for its archive write to succeed and must never be sent again.
	Delivered bool `bson:"delivered,omitempty"`
	// WAMID is the id Meta gave the message when it accepted it, recorded with Delivered and
	// kept in the archive, where the delivery statuses Meta reports later are matched on it.
	// It is absent on a message Meta never accepted, and on one whose acceptance named no id.
	WAMID string `bson:"wamid,omitempty"`
}
