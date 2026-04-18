package evtx

import (
	"errors"
	"strings"
	"testing"
)

func TestHostPort(t *testing.T) {
	if got := hostPort("graylog.local", 12201); got != "graylog.local:12201" {
		t.Errorf("hostPort host:port: got %q", got)
	}
	if got := hostPort("::1", 12201); got != "[::1]:12201" {
		t.Errorf("hostPort IPv6 must bracket: got %q", got)
	}
}

func TestShortMessage(t *testing.T) {
	e := WindowsEvent{CEPAEventType: "CEPP_FILE_WRITE", ObjectName: "/mnt/a.txt"}
	if got := e.ShortMessage(); got != "CEPP_FILE_WRITE on /mnt/a.txt" {
		t.Errorf("ShortMessage: got %q", got)
	}
}

func TestSendWithRetry_FirstSendSucceeds(t *testing.T) {
	calls := 0
	err := sendWithRetry(
		func() error { calls++; return nil },
		func() error { t.Fatal("reconnect must not run"); return nil },
	)
	if err != nil || calls != 1 {
		t.Errorf("expected 1 send, nil err; got calls=%d err=%v", calls, err)
	}
}

func TestSendWithRetry_RetrySucceeds(t *testing.T) {
	sends := 0
	reconnects := 0
	err := sendWithRetry(
		func() error {
			sends++
			if sends == 1 {
				return errors.New("first-fail")
			}
			return nil
		},
		func() error { reconnects++; return nil },
	)
	if err != nil || sends != 2 || reconnects != 1 {
		t.Errorf("expected sends=2 reconnects=1 err=nil; got %d %d %v", sends, reconnects, err)
	}
}

func TestSendWithRetry_ReconnectFails(t *testing.T) {
	err := sendWithRetry(
		func() error { return errors.New("send-fail") },
		func() error { return errors.New("reconnect-fail") },
	)
	if err == nil || !strings.Contains(err.Error(), "send-fail") ||
		!strings.Contains(err.Error(), "reconnect-fail") {
		t.Errorf("expected joined error, got %v", err)
	}
}

func TestSendWithRetry_RetryFails(t *testing.T) {
	err := sendWithRetry(
		func() error { return errors.New("send-fail") },
		func() error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "send after reconnect") {
		t.Errorf("expected 'send after reconnect' wrapper, got %v", err)
	}
}
