package email

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoopSender_SendInvitation(t *testing.T) {
	t.Parallel()

	sender := NewNoopSender()
	err := sender.SendInvitation(context.Background(), "user@example.com", "Alice", "Acme Corp", "https://example.com/accept?token=abc")
	require.NoError(t, err, "NoopSender should never return an error")
}

func TestInvitationHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inviter     string
		org         string
		url         string
		mustContain []string
	}{
		{
			name:    "with inviter name",
			inviter: "Alice",
			org:     "Acme Corp",
			url:     "https://example.com/accept?token=abc",
			mustContain: []string{
				"Acme Corp",
				"Alice",
				"https://example.com/accept?token=abc",
				"Accept Invitation",
			},
		},
		{
			name:    "empty inviter falls back to Someone",
			inviter: "",
			org:     "TestOrg",
			url:     "https://example.com/invite",
			mustContain: []string{
				"Someone",
				"TestOrg",
			},
		},
		{
			name:    "HTML special characters are escaped",
			inviter: "<script>alert('xss')</script>",
			org:     "Org & Co <Ltd>",
			url:     "https://example.com/invite?a=1&b=2",
			mustContain: []string{
				"&lt;script&gt;",
				"Org &amp; Co &lt;Ltd&gt;",
				"a=1&amp;b=2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			html := invitationHTML(tt.inviter, tt.org, tt.url)
			for _, s := range tt.mustContain {
				require.Contains(t, html, s, "invitation HTML should contain %q", s)
			}
		})
	}
}

func TestNewSMTPSender(t *testing.T) {
	t.Parallel()

	cfg := SMTPConfig{
		Host:     "smtp.example.com",
		Port:     "587",
		Username: "user",
		Password: "pass",
		From:     "noreply@example.com",
	}
	sender := NewSMTPSender(cfg)
	require.NotNil(t, sender, "NewSMTPSender should return a non-nil sender")
	require.Equal(t, "smtp.example.com", sender.cfg.Host, "should store config")
}
