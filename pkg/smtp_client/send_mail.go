package smtp_client

import (
	"errors"
	"net/textproto"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
	"github.com/jordan-wright/email"
)

func (sc *SmtpClients) SendMail(
	to []string,
	subject string,
	htmlContent string,
	overrides *types.HeaderOverrides,
) error {
	sc.counter += 1
	if len(sc.connectionPool) < 1 {
		sc.connectionPool = initConnectionPool(sc.servers)
		if len(sc.connectionPool) < 1 {
			return errors.New("no servers defined")
		}
	}

	index := sc.counter % len(sc.connectionPool)
	selectedServer := sc.connectionPool[index]

	From := sc.servers.From
	Sender := sc.servers.Sender
	ReplyTo := sc.servers.ReplyTo

	if overrides != nil {
		if overrides.From != "" {
			From = overrides.From
		}
		if overrides.Sender != "" {
			Sender = overrides.Sender
		}

		if overrides.NoReplyTo {
			ReplyTo = []string{}
		} else if len(overrides.ReplyTo) > 0 {
			ReplyTo = overrides.ReplyTo
		}
	}

	e := &email.Email{
		To:      to,
		From:    From,
		Sender:  Sender,
		ReplyTo: ReplyTo,
		Subject: subject,
		HTML:    []byte(htmlContent),
		Headers: textproto.MIMEHeader{},
	}
	err := selectedServer.Send(e, time.Second*time.Duration(sc.servers.Servers[index].SendTimeout))

	if err != nil {
		// close and try to reconnect
		logger.Error.Printf("error when trying to send email: %v", err)
		pool, errReconnect := connectToPool(sc.servers.Servers[index])
		if errReconnect != nil {
			logger.Error.Printf("cannot reconnect pool for %s", sc.servers.Servers[index].Host)
		} else {
			logger.Info.Printf("successfully reconnected to %s", sc.servers.Servers[index].Host)
			sc.connectionPool[index] = *pool
		}
	}
	return err
}
