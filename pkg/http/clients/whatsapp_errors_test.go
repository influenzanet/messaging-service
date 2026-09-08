package clients

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWhatsAppSendErrorClass(t *testing.T) {
	for class, codes := range map[WhatsAppErrorClass][]int{
		WhatsAppErrorAuth:               {3, 10, 190, 200, 250, 299, 368, 131005, 131031, 131042, 133010},
		WhatsAppErrorThrottled:          {4, 17, 341, 613, 80007, 130429, 131048},
		WhatsAppErrorTransient:          {1, 2, 131000, 131016, 131057},
		WhatsAppErrorRecipientThrottled: {131056},
		WhatsAppErrorUnknown:            {0, 199, 300, 999999, 100, 131009, 131026, 131030, 132000, 132001, 132012, 132015, 132016},
	} {
		for _, code := range codes {
			t.Run(fmt.Sprint(code), func(t *testing.T) {
				err := &WhatsAppSendError{StatusCode: 400, Code: code}
				if got := err.Class(); got != class {
					t.Fatalf("Class() = %v, want %v", got, class)
				}
			})
		}
	}
	for _, tt := range []struct {
		name string
		err  WhatsAppSendError
		want WhatsAppErrorClass
	}{
		{"unauthorized", WhatsAppSendError{StatusCode: 401}, WhatsAppErrorAuth},
		{"forbidden beats message error", WhatsAppSendError{StatusCode: 403, Code: 132012}, WhatsAppErrorAuth},
		{"throttled beats message error", WhatsAppSendError{StatusCode: 429, Code: 100}, WhatsAppErrorThrottled},
		{"pair limit on HTTP429", WhatsAppSendError{StatusCode: 429, Code: 131056}, WhatsAppErrorRecipientThrottled},
		{"pair limit with transient flag", WhatsAppSendError{StatusCode: 400, Code: 131056, IsTransient: true}, WhatsAppErrorRecipientThrottled},
		{"timeout", WhatsAppSendError{StatusCode: 408}, WhatsAppErrorTransient},
		{"server error", WhatsAppSendError{StatusCode: 500}, WhatsAppErrorTransient},
		{"proxy beats message error", WhatsAppSendError{StatusCode: 502, Code: 100}, WhatsAppErrorTransient},
		{"unavailable", WhatsAppSendError{StatusCode: 503}, WhatsAppErrorTransient},
		{"transient flag beats message error", WhatsAppSendError{StatusCode: 400, Code: 100, IsTransient: true}, WhatsAppErrorTransient},
		{"transport", WhatsAppSendError{cause: context.DeadlineExceeded}, WhatsAppErrorTransient},
		{"unknown bad request", WhatsAppSendError{StatusCode: 400}, WhatsAppErrorUnknown},
		{"not found", WhatsAppSendError{StatusCode: 404}, WhatsAppErrorUnknown},
		{"redirect", WhatsAppSendError{StatusCode: 302}, WhatsAppErrorUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Class(); got != tt.want {
				t.Fatalf("Class() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendTemplateMessageErrorResponse(t *testing.T) {
	const phone = "+391234567890"
	for _, tt := range []struct {
		name   string
		status int
		body   string
		code   int
		class  WhatsAppErrorClass
	}{
		{"token", 401, `{"error":{"code":190,"error_subcode":463}}`, 190, WhatsAppErrorAuth},
		{"token with HTTP400", 400, `{"error":{"code":190}}`, 190, WhatsAppErrorAuth},
		{"forbidden HTML", 403, `<html>unavailable</html>`, 0, WhatsAppErrorAuth},
		{"template parameters", 400, `{"error":{"code":132012,"message":"bad phone +391234567890","error_data":{"details":"+391234567890"}}}`, 132012, WhatsAppErrorUnknown},
		{"rate limit", 429, `{"error":{"code":130429}}`, 130429, WhatsAppErrorThrottled},
		{"empty rate limit", 429, ``, 0, WhatsAppErrorThrottled},
		{"overloaded", 500, `{"error":{"code":131000}}`, 131000, WhatsAppErrorTransient},
		{"proxy HTML", 502, `<html>unavailable</html>`, 0, WhatsAppErrorTransient},
		{"transient flag", 400, `{"error":{"code":999,"is_transient":true}}`, 999, WhatsAppErrorTransient},
		{"unknown", 400, `{"error":{"code":999}}`, 999, WhatsAppErrorUnknown},
		{"empty bad request", 400, ``, 0, WhatsAppErrorUnknown},
		{"invalid JSON bad request", 400, `<html>bad request</html>`, 0, WhatsAppErrorUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client := NewWhatsAppClient("test-token", "test-phone-id")
			client.apiBaseURL = server.URL
			err := client.SendTemplateMessage(context.Background(), phone, "template", "it", nil)
			var sendErr *WhatsAppSendError
			if !errors.As(err, &sendErr) {
				t.Fatalf("error = %v, want *WhatsAppSendError", err)
			}
			if sendErr.StatusCode != tt.status || sendErr.Code != tt.code || sendErr.Class() != tt.class {
				t.Fatalf("error = %+v, class = %v", sendErr, sendErr.Class())
			}
			if tt.name == "token" && sendErr.Subcode != 463 {
				t.Fatalf("subcode = %d, want 463", sendErr.Subcode)
			}
			if strings.Contains(err.Error(), phone) || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("error contains sensitive data: %v", err)
			}
		})
	}
}

func TestSendTemplateMessageTransportErrors(t *testing.T) {
	t.Run("error body disconnected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":`)
		}))
		defer server.Close()
		client := NewWhatsAppClient("test-token", "test-phone-id")
		client.apiBaseURL = server.URL
		err := client.SendTemplateMessage(context.Background(), "+391234567890", "template", "it", nil)
		var sendErr *WhatsAppSendError
		if !errors.As(err, &sendErr) || sendErr.Class() != WhatsAppErrorTransient || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected a wrapped transient response-body disconnect, got %v", err)
		}
	})
	t.Run("error body timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.(http.Flusher).Flush()
			<-release
		}))
		defer server.Close()
		defer close(release)
		client := NewWhatsAppClient("test-token", "test-phone-id")
		client.apiBaseURL = server.URL
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := client.SendTemplateMessage(ctx, "+391234567890", "template", "it", nil)
		var sendErr *WhatsAppSendError
		if !errors.As(err, &sendErr) || sendErr.Class() != WhatsAppErrorTransient || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a wrapped transient response-body timeout, got %v", err)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()
		client := NewWhatsAppClient("test-token", "test-phone-id")
		client.apiBaseURL = server.URL
		err := client.SendTemplateMessage(context.Background(), "+391234567890", "template", "it", nil)
		var sendErr *WhatsAppSendError
		if !errors.As(err, &sendErr) || sendErr.Class() != WhatsAppErrorTransient || errors.Unwrap(err) == nil {
			t.Fatalf("expected a wrapped transient transport error, got %v", err)
		}
	})
	t.Run("response timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer server.Close()
		defer close(release)
		client := NewWhatsAppClient("test-token", "test-phone-id")
		client.apiBaseURL = server.URL
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := client.SendTemplateMessage(ctx, "+391234567890", "template", "it", nil)
		var sendErr *WhatsAppSendError
		if !errors.As(err, &sendErr) || sendErr.Class() != WhatsAppErrorTransient || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a wrapped transient deadline error, got %v", err)
		}
	})
}
