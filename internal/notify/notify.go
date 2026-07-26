// Package notify sends roost incident notifications over SMTP. It uses only
// the standard library (net/smtp) to honor roost's two-dependency rule, and
// injects the send function so tests never touch the network.
package notify

import (
	"fmt"
	"net/smtp"
	"strings"
)

// Mailer sends plain-text notification emails. Send defaults to smtp.SendMail;
// tests inject a fake so no real connection is made.
type Mailer struct {
	Host string
	Port int
	User string
	Pass string
	From string   // sender address; defaults to User when empty
	To   []string // recipients
	Send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// Enabled reports whether the mailer is configured enough to send.
func (m Mailer) Enabled() bool { return m.Host != "" && len(m.To) > 0 }

// Notify sends one notification. A disabled mailer is a silent no-op so the
// caller need not check first.
func (m Mailer) Notify(subject, body string) error {
	if !m.Enabled() {
		return nil
	}
	send := m.Send
	if send == nil {
		send = smtp.SendMail
	}
	from := m.From
	if from == "" {
		from = m.User
	}
	var auth smtp.Auth
	if m.User != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)
	return send(addr, auth, from, m.To, buildMessage(from, m.To, subject, body))
}

// buildMessage assembles a minimal RFC 822 plain-text message.
func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
