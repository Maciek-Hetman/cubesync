package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
)

type Mailer interface {
	SendVerification(context.Context, string, string) error
	SendPasswordReset(context.Context, string, string) error
}

type SMTPMailer struct {
	config config.Config
	logger *slog.Logger
}

func NewMailer(cfg config.Config, logger *slog.Logger) Mailer {
	return &SMTPMailer{config: cfg, logger: logger}
}

func (m *SMTPMailer) SendVerification(ctx context.Context, email, token string) error {
	link := m.config.ClientURL + "/verify-email?token=" + url.QueryEscape(token)
	return m.send(ctx, email, "Verify your CubeTimer account", "Open this link to verify your email:\n\n"+link, link)
}

func (m *SMTPMailer) SendPasswordReset(ctx context.Context, email, token string) error {
	link := m.config.ClientURL + "/reset-password?token=" + url.QueryEscape(token)
	return m.send(ctx, email, "Reset your CubeTimer password", "Open this link to reset your password:\n\n"+link, link)
}

func (m *SMTPMailer) send(ctx context.Context, recipient, subject, body, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.config.SMTPHost == "" {
		if m.config.LogOneTimeLinks {
			m.logger.Warn("development_one_time_link", "recipient", recipient, "subject", subject, "url", link)
			return nil
		}
		return fmt.Errorf("SMTP_HOST is required when one-time link logging is disabled")
	}

	from, err := mail.ParseAddress(m.config.SMTPFrom)
	if err != nil {
		return fmt.Errorf("parse SMTP_FROM: %w", err)
	}
	var auth smtp.Auth
	if m.config.SMTPUsername != "" {
		auth = smtp.PlainAuth("", m.config.SMTPUsername, m.config.SMTPPassword, m.config.SMTPHost)
	}
	message := []byte(
		"From: " + m.config.SMTPFrom + "\r\n" +
			"To: " + recipient + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"\r\n" + body + "\r\n",
	)
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", m.config.SMTPAddress())
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, m.config.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()
	if m.config.SMTPStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: m.config.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
