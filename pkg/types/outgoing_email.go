package types

import "go.mongodb.org/mongo-driver/bson/primitive"

type OutgoingEmail struct {
	ID              primitive.ObjectID `bson:"_id,omitempty"`
	MessageType     string             `bson:"messageType"`
	To              []string           `bson:"to"`
	Subject         string             `bson:"subject"`
	HeaderOverrides *HeaderOverrides   `bson:"headers"`
	Content         string             `bson:"content"`
	AddedAt         int64              `bson:"addedAt"`
}