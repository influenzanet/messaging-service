package types

import (
	"crypto/sha256"
	"encoding/hex"
)

// A wamid, the id Meta gives a WhatsApp message, is not an opaque id: it is the recipient's
// full phone number and a random part, base64-encoded. It is therefore neither logged nor
// archived as it is, only through the two forms below. Neither can be turned back into the
// number, as the random part is hashed with it.

// WAMIDHash is the form in which a wamid is archived in sent-whatsapp and matched against the
// delivery statuses Meta reports: the hex SHA-256 of the id, or "" for an empty id, which has
// nothing to match.
func WAMIDHash(wamid string) string {
	if wamid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(wamid))
	return hex.EncodeToString(sum[:])
}

// WAMIDLogRef is the form in which a wamid goes into the logs: the first 12 hex digits of
// WAMIDHash, or "none" for an empty id. It is the same wherever the same id is logged (the
// send of a message and the delivery statuses Meta reports for it), and an archived row is
// found from it with a prefix match on wamidHash. user-management computes it the same way for
// the verification codes it sends.
func WAMIDLogRef(wamid string) string {
	if wamid == "" {
		return "none"
	}
	return WAMIDHash(wamid)[:12]
}
