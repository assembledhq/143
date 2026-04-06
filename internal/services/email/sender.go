package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/rs/zerolog"
)

// Sender sends transactional emails.
type Sender interface {
	SendInvitation(ctx context.Context, to, inviterName, orgName, acceptURL string) error
}

// SMTPConfig holds SMTP connection details.
type SMTPConfig struct {
	Host     string // SMTP server host
	Port     string // SMTP server port (e.g. "587")
	Username string // SMTP auth username
	Password string // SMTP auth password
	From     string // From address (e.g. "noreply@example.com")
}

// SMTPSender sends emails via an SMTP server.
type SMTPSender struct {
	cfg SMTPConfig
}

// NewSMTPSender creates an SMTP-backed email sender.
func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg}
}

// SendInvitation sends an HTML invitation email.
func (s *SMTPSender) SendInvitation(ctx context.Context, to, inviterName, orgName, acceptURL string) error {
	subject := fmt.Sprintf("You've been invited to join %s", orgName)

	html := invitationHTML(inviterName, orgName, acceptURL)

	msg := strings.Join([]string{
		"From: " + s.cfg.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		html,
	}, "\r\n")

	addr := s.cfg.Host + ":" + s.cfg.Port
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send invitation email to %s: %w", to, err)
	}
	return nil
}

// NoopSender logs invitation details without sending an email.
// Used when SMTP is not configured.
type NoopSender struct{}

// NewNoopSender creates a no-op email sender that only logs.
func NewNoopSender() *NoopSender {
	return &NoopSender{}
}

// SendInvitation logs the invitation instead of sending an email.
func (n *NoopSender) SendInvitation(ctx context.Context, to, inviterName, orgName, acceptURL string) error {
	zerolog.Ctx(ctx).Info().
		Str("to", to).
		Str("inviter", inviterName).
		Str("org", orgName).
		Str("accept_url", acceptURL).
		Msg("email sending skipped (SMTP not configured)")
	return nil
}

// invitationHTML returns the HTML body for an invitation email.
func invitationHTML(inviterName, orgName, acceptURL string) string {
	inviterText := "Someone"
	if inviterName != "" {
		inviterText = inviterName
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="margin:0;padding:0;background-color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;">
  <table width="100%%" cellpadding="0" cellspacing="0" style="padding:40px 20px;">
    <tr><td align="center">
      <table width="480" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:8px;overflow:hidden;">
        <tr><td style="padding:32px 32px 24px;text-align:center;">
          <h1 style="margin:0 0 8px;font-size:20px;font-weight:600;color:#18181b;">
            Join %s
          </h1>
          <p style="margin:0 0 24px;font-size:14px;color:#71717a;line-height:1.5;">
            %s has invited you to join <strong>%s</strong>.
          </p>
          <a href="%s" style="display:inline-block;padding:10px 24px;background-color:#18181b;color:#ffffff;text-decoration:none;border-radius:6px;font-size:14px;font-weight:500;">
            Accept Invitation
          </a>
          <p style="margin:24px 0 0;font-size:12px;color:#a1a1aa;line-height:1.5;">
            This invitation expires in 7 days. If you didn't expect this email, you can safely ignore it.
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`, orgName, inviterText, orgName, acceptURL)
}
