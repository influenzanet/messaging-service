package messagedb

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/influenzanet/messaging-service/pkg/types"
)

var testDBService *MessageDBService

const (
	testDBNamePrefix = "TEST_GLOBALDB_"
)

var (
	testInstanceID = strconv.FormatInt(time.Now().Unix(), 10)
)

// Pre-Test Setup
func TestMain(m *testing.M) {
	setupTestDBService()
	result := m.Run()
	dropTestDB()
	os.Exit(result)
}

func setupTestDBService() {
	connStr := os.Getenv("MESSAGE_DB_CONNECTION_STR")
	username := os.Getenv("MESSAGE_DB_USERNAME")
	password := os.Getenv("MESSAGE_DB_PASSWORD")
	prefix := os.Getenv("MESSAGE_DB_CONNECTION_PREFIX") // Used in test mode
	if connStr == "" || username == "" || password == "" {
		log.Fatal("Couldn't read DB credentials.")
	}
	URI := fmt.Sprintf(`mongodb%s://%s:%s@%s`, prefix, username, password, connStr)

	var err error
	Timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		log.Fatal("DB_TIMEOUT: " + err.Error())
	}
	IdleConnTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		log.Fatal("DB_IDLE_CONN_TIMEOUT" + err.Error())
	}
	mps, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	MaxPoolSize := uint64(mps)
	if err != nil {
		log.Fatal("DB_MAX_POOL_SIZE: " + err.Error())
	}
	testDBService = NewMessageDBService(
		types.DBConfig{
			URI:             URI,
			Timeout:         Timeout,
			IdleConnTimeout: IdleConnTimeout,
			MaxPoolSize:     MaxPoolSize,
			DBNamePrefix:    testDBNamePrefix,
		},
	)
}

func dropTestDB() {
	log.Println("Drop test database: messagedb package")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testDBService.DBClient.Database(testDBNamePrefix + "messageDB").Drop(ctx)
	if err != nil {
		log.Fatal(err)
	}
}
