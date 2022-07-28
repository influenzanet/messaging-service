package smtp_client

import (
	"crypto/tls"
	"net/smtp"

	"github.com/coneno/logger"
	"github.com/jordan-wright/email"
)

type SmtpClients struct {
	servers        SmtpServerList
	connectionPool []email.Pool
	counter        int
}

func NewSmtpClients(configFile string) (*SmtpClients, error) {
	serverList := SmtpServerList{}
	if err := serverList.ReadFromFile(configFile); err != nil {
		return nil, err
	}

	sc := &SmtpClients{
		servers:        serverList,
		counter:        0,
		connectionPool: initConnectionPool(serverList),
	}
	return sc, nil
}

func initConnectionPool(serverList SmtpServerList) []email.Pool {
	connectionPools := []email.Pool{}
	for _, server := range serverList.Servers {
		pool, err := connectToPool(server)
		if err != nil {
			logger.Error.Print("Error setting up connection pool for: " + server.Address())
			continue
		} else {
			connectionPools = append(connectionPools, *pool)
		}
	}
	if len(connectionPools) < 1 {
		logger.Error.Fatal("no smtp server connection in the pool")
	}
	return connectionPools
}

func connectToPool(server SmtpServer) (*email.Pool, error) {
	//Set number of concurrent connections here
	auth := smtp.PlainAuth(
		"",
		server.AuthData.Username,
		server.AuthData.Password,
		server.Host,
	)
	if server.AuthData.Username == "" && server.AuthData.Password == "" {
		auth = nil
	}

	tlsOpts := &tls.Config{
		InsecureSkipVerify: server.InsecureSkipVerify,
		ServerName:         server.Host,
	}
	pool, err := email.NewPool(server.Address(), server.Connections, auth, tlsOpts)
	return pool, err
}
