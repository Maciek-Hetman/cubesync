package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"net/smtp"
	"net/url"

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
	link := m.config.PublicURL + "/v1/auth/email/verify?token=" + url.QueryEscape(token)
	return m.send(ctx, email, "Verify your CubeTimer account", "Open this link to verify your email:\n\n"+link, link)
}

func (m *SMTPMailer) SendPasswordReset(ctx context.Context, email, token string) error {
	link := m.config.PublicURL + "/reset-password?token=" + url.QueryEscape(token)
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
	return smtp.SendMail(m.config.SMTPAddress(), auth, from.Address, []string{recipient}, message)
}
