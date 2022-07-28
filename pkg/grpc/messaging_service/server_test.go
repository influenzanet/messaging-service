package messaging_service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/dbs/messagedb"
	"github.com/influenzanet/messaging-service/pkg/types"

	"google.golang.org/grpc/status"
)

var testMessageDBService *messagedb.MessageDBService

const (
	testDBNamePrefix = "TEST_SERVICE_"
)

var (
	testInstanceID = strconv.FormatInt(time.Now().Unix(), 10)
)

// Pre-Test Setup
func TestMain(m *testing.M) {
	setupTestMessageDBService()
	result := m.Run()
	dropTestDB()
	os.Exit(result)
}

func setupTestMessageDBService() {
	connStr := os.Getenv("MESSAGE_DB_CONNECTION_STR")
	username := os.Getenv("MESSAGE_DB_USERNAME")
	password := os.Getenv("MESSAGE_DB_PASSWORD")
	prefix := os.Getenv("MESSAGE_DB_CONNECTION_PREFIX") // Used in test mode
	if connStr == "" || username == "" || password == "" {
		logger.Error.Fatal("Couldn't read DB credentials.")
	}
	URI := fmt.Sprintf(`mongodb%s://%s:%s@%s`, prefix, username, password, connStr)

	var err error
	Timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_TIMEOUT: " + err.Error())
	}
	IdleConnTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_IDLE_CONN_TIMEOUT" + err.Error())
	}
	mps, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	MaxPoolSize := uint64(mps)
	if err != nil {
		logger.Error.Fatal("DB_MAX_POOL_SIZE: " + err.Error())
	}
	testMessageDBService = messagedb.NewMessageDBService(
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
	logger.Debug.Println("Drop test database: service package")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testMessageDBService.DBClient.Database(testDBNamePrefix + testInstanceID + "_messageDB").Drop(ctx)
	if err != nil {
		logger.Error.Fatal(err)
	}
}

func shouldHaveGrpcErrorStatus(err error, expectedError string) (bool, string) {
	if err == nil {
		return false, "should return an error"
	}
	st, ok := status.FromError(err)
	if !ok || st == nil {
		return false, fmt.Sprintf("unexpected error: %s", err.Error())
	}

	if len(expectedError) > 0 && st.Message() != expectedError {
		return false, fmt.Sprintf("wrong error: %s", st.Message())
	}
	return true, ""
}
