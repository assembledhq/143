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
	// SendEmailVerification sends the proof-of-address link to a
	// password-signup user. Verifying unlocks email-domain auto-join.
	SendEmailVerification(ctx context.Context, to, verifyURL string) error
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

// SendInvitation sends a plain-text invitation email.
func (s *SMTPSender) SendInvitation(ctx context.Context, to, inviterName, orgName, acceptURL string) error {
	subject := fmt.Sprintf("Invitation: join %s on 143.dev", orgName)
	if err := s.sendPlainText(to, subject, invitationText(inviterName, orgName, acceptURL)); err != nil {
		return fmt.Errorf("send invitation email to %s: %w", to, err)
	}
	return nil
}

// SendEmailVerification sends a plain-text verify-your-email message.
func (s *SMTPSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	if err := s.sendPlainText(to, "Verify your email for 143.dev", emailVerificationText(verifyURL)); err != nil {
		return fmt.Errorf("send verification email to %s: %w", to, err)
	}
	return nil
}

// sendPlainText assembles and sends a UTF-8 plain-text message.
func (s *SMTPSender) sendPlainText(to, subject, body string) error {
	msg := strings.Join([]string{
		"From: " + s.cfg.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := s.cfg.Host + ":" + s.cfg.Port
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	return smtp.SendMail(addr, auth, s.cfg.From, []string{to}, []byte(msg))
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

// SendEmailVerification logs the verification link instead of sending it.
func (n *NoopSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	zerolog.Ctx(ctx).Info().
		Str("to", to).
		Str("verify_url", verifyURL).
		Msg("email sending skipped (SMTP not configured)")
	return nil
}

// invitationText returns the plain-text body for an invitation email.
func invitationText(inviterName, orgName, acceptURL string) string {
	inviterText := "Someone"
	if inviterName != "" {
		inviterText = inviterName
	}

	return fmt.Sprintf(`You’ve been invited to join %s on 143.dev

%s invited you to collaborate with their team.

What to do next:
1. Open the invite link below
2. Sign in or create your account
3. You’ll join %s automatically

Accept invitation:
%s

This link expires in 7 days.
If you weren’t expecting this, you can ignore this email.`, orgName, inviterText, orgName, acceptURL)
}

// emailVerificationText returns the plain-text body for a verification email.
func emailVerificationText(verifyURL string) string {
	return fmt.Sprintf(`Verify your email for 143.dev

Confirm this address belongs to you. If your company has verified its
domain, verifying also adds you to your team’s workspace automatically.

Verify your email:
%s

This link expires in 24 hours.
If you didn’t create a 143.dev account, you can ignore this email.`, verifyURL)
}
