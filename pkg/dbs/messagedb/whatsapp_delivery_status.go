package messagedb

import (
	"errors"

	"github.com/influenzanet/messaging-service/pkg/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// allDeliveryStatuses lists the statuses WhatsAppDeliveryStatusRank knows, lowest first.
var allDeliveryStatuses = []string{types.MetaStatusSent, types.MetaStatusDelivered, types.MetaStatusRead, types.MetaStatusFailed}

// RecordWhatsAppDeliveryStatus records the delivery status Meta reported for the archived
// message with the given wamid (matched on its hash, the only form the archive keeps; see
// types.WAMIDHash), unless the message already has a status that ranks the same or
// higher (see types.WhatsAppDeliveryStatusRank): the check and the write are one conditional
// update, so of two notifications processed at the same time the higher status is the one kept,
// and a repeated notification changes nothing. errorCode is stored only on a failed status.
//
// result says which of these happened: the status was recorded, the row already had it or a
// later one, no archived message has this wamid (see types.DeliveryStatusUnknownMessage), or
// the status has no rank and is not recorded. userID is the account the message was sent to,
// whether or not this call changed the row; it is "" for a message with no account. On an
// error, result is 0.
func (dbService *MessageDBService) RecordWhatsAppDeliveryStatus(instanceID, wamid, status string, at int64, errorCode int) (userID string, result types.DeliveryStatusResult, err error) {
	rank := types.WhatsAppDeliveryStatusRank(status)
	if rank == 0 {
		return "", types.DeliveryStatusNotTracked, nil
	}
	// An empty id has no hash, and must not reach the rows that have none.
	wamidHash := types.WAMIDHash(wamid)
	if wamidHash == "" {
		return "", types.DeliveryStatusUnknownMessage, nil
	}
	notLower := []string{}
	for _, s := range allDeliveryStatuses {
		if types.WhatsAppDeliveryStatusRank(s) >= rank {
			notLower = append(notLower, s)
		}
	}
	set := bson.M{"metaStatus": status, "metaStatusAt": at}
	// Failed ranks highest, so no later status replaces it and its code is never left behind on
	// another status.
	if status == types.MetaStatusFailed && errorCode != 0 {
		set["metaErrorCode"] = errorCode
	}
	update := bson.M{"$set": set}

	ctx, cancel := dbService.getContext()
	defer cancel()
	sent := dbService.collectionRefSentWhatsApp(instanceID)
	projection := bson.M{"userId": 1}

	var row types.SentWhatsApp
	err = sent.FindOneAndUpdate(ctx,
		bson.M{"wamidHash": wamidHash, "metaStatus": bson.M{"$nin": notLower}},
		update,
		options.FindOneAndUpdate().SetProjection(projection),
	).Decode(&row)
	if err == nil {
		return row.UserID, types.DeliveryStatusRecorded, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return "", 0, err
	}
	// Nothing changed: the message is unknown, or it already has this status or a later one.
	err = sent.FindOne(ctx, bson.M{"wamidHash": wamidHash}, options.FindOne().SetProjection(projection)).Decode(&row)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", types.DeliveryStatusUnknownMessage, nil
	}
	if err != nil {
		return "", 0, err
	}
	return row.UserID, types.DeliveryStatusUnchanged, nil
}
