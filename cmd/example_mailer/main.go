package main

import (
	"crypto/tls"
	"errors"
	"io/ioutil"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/coneno/logger"
	"github.com/jordan-wright/email"
	"gopkg.in/yaml.v2"
)

type SmtpServerList struct {
	Servers     []SmtpServer `yaml:"servers"`
	FromName    string       `yaml:"fromName"`
	FromAddr    string       `yaml:"from"`
	AddressList []string     `yaml:"sendTo"`
}

type SmtpServer struct {
	Host               string `yaml:"host"`
	Port               string `yaml:"port"`
	Connections        int    `yaml:"connections"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
	AuthData           struct {
		Username string `yaml:"user"`
		Password string `yaml:"password"`
	} `yaml:"auth"`
}

// Address URI to smtp server
func (s *SmtpServer) Address() string {
	return s.Host + ":" + s.Port
}

func (sl *SmtpServerList) ReadFromFile(fname string) (err error) {
	yamlFile, err := ioutil.ReadFile(fname)
	if err != nil {
		logger.Error.Printf("ReadServerConfig: err #%v ", err)
		return err
	}
	err = yaml.UnmarshalStrict(yamlFile, &sl)
	return
}

type SmtpClients struct {
	servers        SmtpServerList
	connectionPool []email.Pool
	counter        int
	addresses      []string
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
		addresses:      serverList.AddressList,
	}
	return sc, nil
}

func initConnectionPool(serverList SmtpServerList) []email.Pool {
	connectionPools := []email.Pool{}
	for _, server := range serverList.Servers {
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
		logger.Debug.Println(tlsOpts)
		var pool, err = email.NewPool(server.Address(), server.Connections, auth, tlsOpts)
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

func formatFrom(defaultAddr string, defaultName string, overrideAddr string, overrideName string) string {
	fromAddr := defaultAddr
	if len(overrideAddr) > 0 {
		fromAddr = overrideAddr
	}

	fromName := defaultName
	if len(overrideName) > 0 {
		fromName = overrideName
	}
	from := fromAddr
	if len(fromName) > 0 {
		from = fromName + " <" + fromAddr + ">"
	}
	return from
}

func (sc *SmtpClients) SendMail(
	to []string,
	fromAddressOverride string,
	fromNameOverride string,
	subject string,
	htmlContent string,
) error {
	sc.counter += 1
	if len(sc.connectionPool) < 1 {
		return errors.New("no servers defined")
	}

	index := sc.counter % len(sc.connectionPool)
	selectedServer := sc.connectionPool[index]

	e := &email.Email{
		To:      to,
		From:    formatFrom(sc.servers.FromAddr, sc.servers.FromName, fromAddressOverride, fromNameOverride),
		Subject: subject,
		HTML:    []byte(htmlContent),
		Headers: textproto.MIMEHeader{},
	}
	return selectedServer.Send(e, time.Second*15)
}

func main() {

	smtpClients, err := NewSmtpClients("server_configs.yaml")
	if err != nil {
		logger.Error.Fatal(err)
	}

	tStart := time.Now()
	for i := 0; i < 10; i++ {
		err := smtpClients.SendMail(
			smtpClients.addresses,
			"",
			"",
			"TEST email",
			"<h1>Test email </h1><p>Counter "+strconv.Itoa(i)+"</p>",
		)
		if err != nil {
			logger.Error.Fatal(err)
		}
		logger.Debug.Printf("Testmail %d sent", i)
	}
	runTime := time.Now().UnixNano() - tStart.UnixNano()
	logger.Info.Printf("Runtime for sending: %d ms", runTime/1000000)
}
