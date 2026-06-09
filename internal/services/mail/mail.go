// Package mail provides SMTP email sending using settings stored in business_settings.
package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"mall/internal/services/settings"
)

// Message is a single outbound email.
type Message struct {
	To      string
	Subject string
	HTML    string
}

// Send sends one email using SMTP settings from the settings store.
func Send(st *settings.Store, m Message) error {
	host := st.Get("smtp_host", "")
	port := st.Get("smtp_port", "587")
	user := st.Get("smtp_username", "")
	pass := st.Get("smtp_password", "")
	enc := st.Get("smtp_encryption", "tls")
	fromName := st.Get("smtp_from_name", st.Get("website_name", "Mall"))
	fromEmail := st.Get("smtp_from_email", user)

	if host == "" {
		return fmt.Errorf("SMTP host not configured")
	}

	from := fmt.Sprintf("%s <%s>", fromName, fromEmail)
	body := buildMIME(from, m.To, m.Subject, m.HTML)
	addr := net.JoinHostPort(host, port)

	switch strings.ToLower(enc) {
	case "ssl":
		return sendSSL(addr, host, user, pass, fromEmail, m.To, body)
	default: // tls / starttls / none
		return sendSTARTTLS(addr, host, user, pass, fromEmail, m.To, body, enc != "none")
	}
}

// SendBatch sends the same subject+body to many recipients, one at a time.
// It returns the number successfully sent and the first error encountered (if any).
func SendBatch(st *settings.Store, subject, html string, recipients []string) (int, error) {
	sent := 0
	var firstErr error
	for _, to := range recipients {
		if err := Send(st, Message{To: to, Subject: subject, HTML: html}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to send to %s: %w", to, err)
			}
			continue
		}
		sent++
	}
	return sent, firstErr
}

// ── helpers ──────────────────────────────────────────────────────────────────

func buildMIME(from, to, subject, html string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(html)
	return []byte(b.String())
}

func sendSSL(addr, host, user, pass, from, to string, body []byte) error {
	tlsCfg := &tls.Config{ServerName: host}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if user != "" {
		if err := client.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	return send(client, from, to, body)
}

func sendSTARTTLS(addr, host, user, pass, from, to string, body []byte, doStartTLS bool) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if doStartTLS {
		tlsCfg := &tls.Config{ServerName: host}
		if err := client.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if user != "" {
		if err := client.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	return send(client, from, to, body)
}

func send(c *smtp.Client, from, to string, body []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(body); err != nil {
		return err
	}
	return w.Close()
}
