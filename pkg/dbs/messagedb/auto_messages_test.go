package messagedb

import (
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
)

func TestAutoMessageDB(t *testing.T) {
	t1 := types.AutoMessage{
		Type:     "testtype",
		NextTime: time.Now().Unix() - 10,
		Template: types.EmailTemplate{
			DefaultLanguage: "test1",
		},
	}
	t.Run("save not existing message", func(t *testing.T) {
		var err error
		t1, err = testDBService.SaveAutoMessage(testInstanceID, t1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("save existing message", func(t *testing.T) {
		t1.Type = "testtype2"
		res, err := testDBService.SaveAutoMessage(testInstanceID, t1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if res.Type != "testtype2" {
			t.Errorf("unexpected result: %v", res)
		}
	})

	t.Run("delete existing message", func(t *testing.T) {
		err := testDBService.DeleteAutoMessage(testInstanceID, t1.ID.Hex())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})

	t.Run("delete not existing message", func(t *testing.T) {
		err := testDBService.DeleteAutoMessage(testInstanceID, t1.ID.Hex())
		if err == nil {
			t.Error("should fail when message already deleted")
			return
		}
	})

	testMessages := []types.AutoMessage{
		{
			Type:     "t1",
			NextTime: time.Now().Unix() - 10,
		},
		{
			Type:     "t3",
			NextTime: time.Now().Unix() + 10,
		},
	}
	for _, temp := range testMessages {
		_, err := testDBService.SaveAutoMessage(testInstanceID, temp)
		if err != nil {
			t.Errorf("unexpected error when creating test messages: %v", err)
			return
		}
	}

	t.Run("find active message", func(t *testing.T) {
		res, err := testDBService.FindAutoMessages(testInstanceID, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		logger.Debug.Println(res)
		if len(res) != 1 {
			t.Errorf("unexpected number of messages found: %d", len(res))
		}
	})
	t.Run("find all message", func(t *testing.T) {
		res, err := testDBService.FindAutoMessages(testInstanceID, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		logger.Debug.Println(res)
		if len(res) != 2 {
			t.Errorf("unexpected number of messages found: %d", len(res))
		}
	})

}
