package main

import (
	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/globaldb"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
)

// ensureWhatsAppIndexes creates the indexes of the WhatsApp collections of every instance known
// at startup, whether WhatsApp is on for it or not: the archive's retention index is what
// deletes archived messages after their retention, so it must exist wherever messages were
// ever archived. A failure is logged and the other instances go on; an instance added later
// gets its indexes at the next start of the scheduler.
func ensureWhatsAppIndexes(mdb *messagedb.MessageDBService, gdb *globaldb.GlobalDBService) {
	instances, err := gdb.GetAllInstances()
	if err != nil {
		logger.Error.Printf("cannot list the instances, the WhatsApp indexes were not checked and archived WhatsApp messages may not expire: %v", err)
		return
	}
	for _, instance := range instances {
		if err := mdb.CreateIndexesForWhatsApp(instance.InstanceID); err != nil {
			logger.Error.Printf("[%s] cannot create the WhatsApp indexes, archived WhatsApp messages may not expire: %v", instance.InstanceID, err)
		}
	}
}
