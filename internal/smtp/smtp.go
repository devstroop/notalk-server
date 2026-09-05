package smtp

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"github.com/devstroop/notalk/internal/config"
)

// Client sends emails via SMTP.
type Client struct {
	cfg config.SMTPConfig
}

// New creates an SMTP client from config.
func New(cfg config.SMTPConfig) *Client {
	return &Client{cfg: cfg}
}

// Enabled returns true when SMTP is configured (host is set).
func (c *Client) Enabled() bool {
	return c.cfg.Host != ""
}

// Send sends a plain-text email.
func (c *Client) Send(to, subject, body string) error {
	if !c.Enabled() {
		return fmt.Errorf("SMTP not configured")
	}

	from := c.cfg.From
	if from == "" {
		from = c.cfg.Username
	}

	// Extract bare email from "Name <email>" format for envelope sender
	envelopeFrom := from
	if idx := strings.Index(from, "<"); idx != -1 {
		envelopeFrom = strings.Trim(from[idx:], "<> ")
	}

	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" +
		body

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)

	var auth smtp.Auth
	if c.cfg.Username != "" {
		auth = smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	}

	if c.cfg.TLS {
		return c.sendTLS(addr, auth, envelopeFrom, to, []byte(msg))
	}

	return smtp.SendMail(addr, auth, envelopeFrom, []string{to}, []byte(msg))
}

// sendTLS handles implicit TLS (port 465).
func (c *Client) sendTLS(addr string, auth smtp.Auth, from, to string, msg []byte) error {
	tlsCfg := &tls.Config{ServerName: c.cfg.Host}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	host, _, _ := net.SplitHostPort(addr)
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = cl.Close() }()

	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := cl.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return cl.Quit()
}
