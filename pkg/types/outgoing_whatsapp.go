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
	HighPrio        bool               `bson:"highPrio"`
	LastSendAttempt int64              `bson:"lastSendAttempt"`
	SendAttempt     int                `bson:"sendAttempt"`
	// Delivered records that Meta accepted the message, before it is archived. A row that
	// carries it is waiting for its archive write to succeed and must never be sent again.
	Delivered bool `bson:"delivered,omitempty"`
}
