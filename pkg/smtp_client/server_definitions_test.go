package smtp_client

import (
	"testing"

	"github.com/coneno/logger"
)

func TestReadServerConfigsFromFile(t *testing.T) {
	servers := SmtpServerList{}

	t.Run("with wrong filename", func(t *testing.T) {
		err := servers.ReadFromFile("../../test/configs/nothere.yaml")
		if err == nil {
			t.Error("error expected")
		}
	})

	t.Run("with wrong content", func(t *testing.T) {

		err := servers.ReadFromFile("../../test/configs/smtp-servers-wrong.yaml")
		if err == nil {
			t.Error("error expected")
		}
	})

	t.Run("with valid content", func(t *testing.T) {
		err := servers.ReadFromFile("../../test/configs/smtp-servers.yaml")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(servers.Servers) != 2 {
			t.Errorf("unexpected number of servers: %d", len(servers.Servers))
			return
		}
		if servers.Servers[0].Address() != "test.example.com:234" || servers.Servers[0].AuthData.Username != "testuser" {
			logger.Error.Println(servers)
			t.Error("first server wrong")
		}
		if servers.Servers[1].Address() != "test2.example.com:235" || servers.Servers[1].AuthData.Username != "testuser2" {
			t.Error("second server wrong")
		}
	})
}
