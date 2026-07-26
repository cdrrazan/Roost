package notify

import (
	"errors"
	"net/smtp"
	"strings"
	"testing"
)

func TestMailerNotifyBuildsAndSends(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	m := Mailer{
		Host: "smtp.example.com", Port: 587,
		User: "bot@example.com", Pass: "secret",
		From: "roost@example.com", To: []string{"me@example.com"},
		Send: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, msg
			return nil
		},
	}
	if err := m.Notify("Keeparu is down", "It stopped responding."); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotAddr != "smtp.example.com:587" {
		t.Errorf("addr = %q", gotAddr)
	}
	if gotFrom != "roost@example.com" || len(gotTo) != 1 || gotTo[0] != "me@example.com" {
		t.Errorf("from/to = %q/%v", gotFrom, gotTo)
	}
	s := string(gotMsg)
	for _, want := range []string{"Subject: Keeparu is down", "To: me@example.com", "It stopped responding.", "text/plain"} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q; got:\n%s", want, s)
		}
	}
}

func TestMailerDisabledIsNoop(t *testing.T) {
	called := false
	m := Mailer{Send: func(string, smtp.Auth, string, []string, []byte) error { called = true; return nil }}
	if !m.Enabled() {
		// good: no host / no recipients
	}
	if err := m.Notify("x", "y"); err != nil {
		t.Fatalf("disabled Notify should be a no-op, got %v", err)
	}
	if called {
		t.Error("disabled mailer must not send")
	}
}

func TestMailerFromDefaultsToUser(t *testing.T) {
	var gotFrom string
	m := Mailer{
		Host: "h", Port: 25, User: "u@example.com", To: []string{"a@b.com"},
		Send: func(_ string, _ smtp.Auth, from string, _ []string, _ []byte) error { gotFrom = from; return nil },
	}
	_ = m.Notify("s", "b")
	if gotFrom != "u@example.com" {
		t.Errorf("from = %q, want the user when From is empty", gotFrom)
	}
}

func TestMailerPropagatesSendError(t *testing.T) {
	m := Mailer{
		Host: "h", Port: 25, To: []string{"a@b.com"},
		Send: func(string, smtp.Auth, string, []string, []byte) error { return errors.New("boom") },
	}
	if err := m.Notify("s", "b"); err == nil {
		t.Error("expected send error to propagate")
	}
}
