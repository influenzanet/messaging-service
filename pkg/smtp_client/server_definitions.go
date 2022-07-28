package smtp_client

import (
	"io/ioutil"

	"github.com/coneno/logger"
	"gopkg.in/yaml.v2"
)

type SmtpServerList struct {
	Servers []SmtpServer `yaml:"servers"`
	From    string       `yaml:"from"`
	Sender  string       `yaml:"sender"`
	ReplyTo []string     `yaml:"replyTo"`
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
	SendTimeout int `yaml:"sendTimeout"`
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
