// Package email ports DysonNetwork.Ring.Email: the SMTP sender
// (EmailService) and the sending-plan state machine
// (EmailSendingPlanService).
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"src.solsynth.dev/sosys/metoer/internal/config"
	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/observability"
)

// Service is the EmailService port.
type Service struct {
	cfg  *config.Config
	obs  *observability.Service
	log  *slog.Logger
}

// New builds the email service (throws when the Email section is missing,
// mirroring "Email service was not configured.").
func New(cfg *config.Config, obs *observability.Service, log *slog.Logger) (*Service, error) {
	if cfg.Email.Server == "" {
		return nil, fmt.Errorf("email service was not configured")
	}
	return &Service{cfg: cfg, obs: obs, log: log}, nil
}

// SendEmail mirrors EmailService.SendEmailAsync: subject prefixed with
// "[{SubjectPrefix}] ", HTML body only, SMTP with optional implicit TLS
// (MailKit UseSsl=false does not auto-STARTTLS). Outcomes are recorded via
// observability; the send error is rethrown after recording.
func (s *Service) SendEmail(ctx context.Context, recipientName, recipientEmail, subject, htmlBody, source string) error {
	subject = fmt.Sprintf("[%s] %s", s.cfg.Email.SubjectPrefix, subject)
	s.log.Info("sending email", "to", recipientEmail, "subject", subject)

	startedAt := time.Now()
	outcome := model.DeliveryOutcomeFailure
	var sendErr error
	defer func() {
		s.obs.RecordEmail(ctx, source, outcome, time.Since(startedAt).Milliseconds(), sendErr)
	}()

	message := buildMessage(s.cfg.Email.FromName, s.cfg.Email.FromAddress, recipientName, recipientEmail, subject, htmlBody)

	if err := s.send(ctx, message, recipientEmail); err != nil {
		sendErr = err
		return err
	}

	outcome = model.DeliveryOutcomeSuccess
	s.log.Info("email sent", "subject", subject, "to", recipientEmail)
	return nil
}

func (s *Service) send(ctx context.Context, message []byte, recipientEmail string) error {
	addr := net.JoinHostPort(s.cfg.Email.Server, fmt.Sprint(s.cfg.Email.Port))
	var client *smtp.Client

	if s.cfg.Email.UseSsl {
		conn, tlsErr := tls.Dial("tcp", addr, &tls.Config{ServerName: s.cfg.Email.Server, MinVersion: tls.VersionTLS12})
		if tlsErr != nil {
			return fmt.Errorf("tls dial %s: %w", addr, tlsErr)
		}
		client = smtp.NewClient(conn)
	} else {
		conn, dialErr := net.Dial("tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("connect smtp %s: %w", addr, dialErr)
		}
		client = smtp.NewClient(conn)
	}
	defer func() { _ = client.Close() }()

	if s.cfg.Email.Username != "" {
		auth := sasl.NewPlainClient("", s.cfg.Email.Username, s.cfg.Email.Password)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(s.cfg.Email.FromAddress, nil); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(recipientEmail, nil); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(message); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// buildMessage constructs the MIME message (BodyBuilder.HtmlBody equivalent:
// a single text/html part; From/To display names, RFC 2047 subjects for
// non-ASCII).
func buildMessage(fromName, fromAddr, toName, toAddr, subject, html string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", formatAddress(fromName, fromAddr))
	fmt.Fprintf(&b, "To: %s\r\n", formatAddress(toName, toAddr))
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(html)
	return b.Bytes()
}

// formatAddress renders a display-name <addr> pair, quoting names that
// contain specials.
func formatAddress(name, addr string) string {
	if strings.TrimSpace(name) == "" {
		return addr
	}
	addrSpec, err := mail.ParseAddress(addr)
	if err == nil {
		addrSpec.Name = name
		return addrSpec.String()
	}
	return fmt.Sprintf("%s <%s>", encodeHeader(name), addr)
}

// encodeHeader applies RFC 2047 encoded-words only when needed (MailKit's
// behavior for non-ASCII header values).
func encodeHeader(value string) string {
	for _, r := range value {
		if r > 127 {
			return mime.QEncoding.Encode("UTF-8", value)
		}
	}
	return value
}
