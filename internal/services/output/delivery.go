// Package output delivers scheduled project results to external systems
// (Slack, email, Notion, webhooks) using the org's existing integrations.
// No local MCP servers required — all delivery is server-side.
package output

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// blockedWebhookHeaders are headers that custom webhook configs must not override.
var blockedWebhookHeaders = map[string]bool{
	"host":             true,
	"content-type":     true,
	"content-length":   true,
	"x-signature-256":  true,
	"authorization":    true,
	"transfer-encoding": true,
}

// maxResponseBodySize limits how much of an external API response we'll read.
const maxResponseBodySize = 1 << 20 // 1 MB

// allowedWebhookMethods restricts which HTTP methods webhooks may use.
var allowedWebhookMethods = map[string]bool{
	http.MethodPost:  true,
	http.MethodPut:   true,
	http.MethodPatch: true,
}

// DeliveryResult captures the outcome of a single delivery attempt.
type DeliveryResult struct {
	DestinationID uuid.UUID
	Type          models.OutputDestinationType
	Success       bool
	Error         string
}

// CycleOutput is the payload delivered to each destination after a project cycle.
type CycleOutput struct {
	ProjectID      uuid.UUID `json:"project_id"`
	ProjectName    string    `json:"project_name"`
	CycleNumber    int       `json:"cycle_number"`
	Analysis       string    `json:"analysis"`
	Summary        string    `json:"summary"`
	TasksCreated   int       `json:"tasks_created"`
	TasksCompleted int       `json:"tasks_completed"`
	TasksFailed    int       `json:"tasks_failed"`
	PRURLs         []string  `json:"pr_urls,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// credentialResolver looks up org integration credentials.
type credentialResolver interface {
	GetForOrg(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// Service delivers project outputs to configured destinations.
type Service struct {
	destinations *db.OutputDestinationStore
	credentials  credentialResolver
	smtpCfg      *SMTPConfig
	httpClient   *http.Client
	logger       zerolog.Logger
}

// SMTPConfig holds SMTP connection details for email delivery.
type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

func NewService(
	destinations *db.OutputDestinationStore,
	credentials credentialResolver,
	logger zerolog.Logger,
) *Service {
	return &Service{
		destinations: destinations,
		credentials:  credentials,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		logger:       logger,
	}
}

// SetSMTPConfig enables email delivery.
func (s *Service) SetSMTPConfig(cfg *SMTPConfig) {
	s.smtpCfg = cfg
}

// DeliverCycleOutput sends the cycle output to all enabled destinations for a project.
func (s *Service) DeliverCycleOutput(ctx context.Context, orgID uuid.UUID, output CycleOutput) []DeliveryResult {
	dests, err := s.destinations.ListEnabledByProject(ctx, orgID, output.ProjectID)
	if err != nil {
		s.logger.Error().Err(err).Str("project_id", output.ProjectID.String()).Msg("failed to list output destinations")
		return nil
	}
	if len(dests) == 0 {
		return nil
	}

	results := make([]DeliveryResult, 0, len(dests))
	for _, dest := range dests {
		result := DeliveryResult{
			DestinationID: dest.ID,
			Type:          dest.DestinationType,
		}

		var deliverErr error
		switch dest.DestinationType {
		case models.OutputDestSlack:
			deliverErr = s.deliverSlack(ctx, orgID, dest, output)
		case models.OutputDestEmail:
			deliverErr = s.deliverEmail(ctx, dest, output)
		case models.OutputDestNotion:
			deliverErr = s.deliverNotion(ctx, orgID, dest, output)
		case models.OutputDestWebhook:
			deliverErr = s.deliverWebhook(ctx, dest, output)
		default:
			deliverErr = fmt.Errorf("unsupported destination type: %s", dest.DestinationType)
		}

		if deliverErr != nil {
			result.Error = deliverErr.Error()
			s.logger.Warn().Err(deliverErr).
				Str("destination_id", dest.ID.String()).
				Str("type", string(dest.DestinationType)).
				Msg("output delivery failed")
		} else {
			result.Success = true
		}
		results = append(results, result)
	}
	return results
}

// slackResponse is the JSON envelope returned by Slack API methods.
// Slack returns HTTP 200 even on application errors; the `ok` field indicates success.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// deliverSlack posts a formatted message to a Slack channel using the org's Slack token.
func (s *Service) deliverSlack(ctx context.Context, orgID uuid.UUID, dest models.OutputDestination, output CycleOutput) error {
	var cfg models.SlackOutputConfig
	if err := json.Unmarshal(dest.Config, &cfg); err != nil {
		return fmt.Errorf("parse slack config: %w", err)
	}

	// Resolve Slack access token from org credentials.
	cred, err := s.credentials.GetForOrg(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return fmt.Errorf("slack credential not found: %w", err)
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok {
		return fmt.Errorf("invalid slack credential type")
	}

	// Build Slack message blocks.
	text := formatSlackMessage(output)

	body := map[string]interface{}{
		"channel": cfg.ChannelID,
		"text":    text,
	}
	if cfg.ThreadTS != "" {
		body["thread_ts"] = cfg.ThreadTS
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+slackCfg.AccessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if readErr != nil {
		return fmt.Errorf("read slack response body: %w", readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack API HTTP error: %s %s", resp.Status, string(respBody))
	}

	// Slack returns HTTP 200 even on application errors — check the "ok" field.
	var slackResp slackResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return fmt.Errorf("parse slack response: %w", err)
	}
	if !slackResp.OK {
		return fmt.Errorf("slack API error: %s", slackResp.Error)
	}

	return nil
}

// deliverEmail sends the cycle summary via SMTP.
func (s *Service) deliverEmail(ctx context.Context, dest models.OutputDestination, output CycleOutput) error {
	if s.smtpCfg == nil {
		return fmt.Errorf("SMTP not configured, cannot deliver email output")
	}

	var cfg models.EmailOutputConfig
	if err := json.Unmarshal(dest.Config, &cfg); err != nil {
		return fmt.Errorf("parse email config: %w", err)
	}
	if len(cfg.Recipients) == 0 {
		return fmt.Errorf("no email recipients configured")
	}

	subject := cfg.Subject
	if subject == "" {
		subject = fmt.Sprintf("[143] %s — Cycle #%d complete", output.ProjectName, output.CycleNumber)
	}
	// RFC 2047 Q-encode the subject to handle non-ASCII characters safely.
	encodedSubject := mime.QEncoding.Encode("utf-8", subject)

	body := formatEmailHTML(output)

	// Build the To header using canonical addresses from mail.ParseAddress
	// to ensure properly formatted SMTP headers.
	canonicalRecipients := make([]string, 0, len(cfg.Recipients))
	for _, addr := range cfg.Recipients {
		parsed, err := mail.ParseAddress(addr)
		if err != nil {
			canonicalRecipients = append(canonicalRecipients, addr)
		} else {
			canonicalRecipients = append(canonicalRecipients, parsed.Address)
		}
	}

	msg := strings.Join([]string{
		"From: " + s.smtpCfg.From,
		"To: " + strings.Join(canonicalRecipients, ","),
		"Subject: " + encodedSubject,
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := s.smtpCfg.Host + ":" + s.smtpCfg.Port
	auth := smtp.PlainAuth("", s.smtpCfg.Username, s.smtpCfg.Password, s.smtpCfg.Host)

	// Use a context-aware dialer for SMTP to avoid hanging indefinitely.
	smtpTimeout := 30 * time.Second
	deadline, ok := ctx.Deadline()
	if ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("context deadline already exceeded")
		}
		if remaining < smtpTimeout {
			smtpTimeout = remaining
		}
	}
	dialer := &net.Dialer{Timeout: smtpTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}

	client, err := smtp.NewClient(conn, s.smtpCfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	// Upgrade to TLS if the server supports STARTTLS.
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.smtpCfg.Host}); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth: %w", err)
	}
	if err := client.Mail(s.smtpCfg.From); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, rcpt := range canonicalRecipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("SMTP RCPT TO %s: %w", rcpt, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		wc.Close()
		return fmt.Errorf("write email body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close SMTP data: %w", err)
	}
	return client.Quit()
}

// deliverNotion appends a block to a Notion page using the org's Notion token.
func (s *Service) deliverNotion(ctx context.Context, orgID uuid.UUID, dest models.OutputDestination, output CycleOutput) error {
	var cfg models.NotionOutputConfig
	if err := json.Unmarshal(dest.Config, &cfg); err != nil {
		return fmt.Errorf("parse notion config: %w", err)
	}

	if cfg.DatabaseID != "" {
		return fmt.Errorf("notion database delivery is not yet supported, use page_id instead")
	}
	if cfg.PageID == "" {
		return fmt.Errorf("notion page_id is required")
	}
	if !models.NotionIDPattern.MatchString(cfg.PageID) {
		return fmt.Errorf("notion page_id must be a valid UUID")
	}

	cred, err := s.credentials.GetForOrg(ctx, orgID, models.ProviderNotion)
	if err != nil {
		return fmt.Errorf("notion credential not found: %w", err)
	}
	notionCfg, ok := cred.Config.(models.NotionConfig)
	if !ok {
		return fmt.Errorf("invalid notion credential type")
	}

	// Append a paragraph block to the Notion page with the cycle summary.
	block := map[string]interface{}{
		"children": []map[string]interface{}{
			{
				"object": "block",
				"type":   "heading_2",
				"heading_2": map[string]interface{}{
					"rich_text": []map[string]interface{}{
						{"type": "text", "text": map[string]string{"content": fmt.Sprintf("Cycle #%d — %s", output.CycleNumber, output.Timestamp.Format("Jan 2, 2006"))}},
					},
				},
			},
			{
				"object": "block",
				"type":   "paragraph",
				"paragraph": map[string]interface{}{
					"rich_text": []map[string]interface{}{
						{"type": "text", "text": map[string]string{"content": output.Summary}},
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("marshal notion block: %w", err)
	}
	apiURL := fmt.Sprintf("https://api.notion.com/v1/blocks/%s/children", cfg.PageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+notionCfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notion API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
		if readErr != nil {
			return fmt.Errorf("notion API error: %s (could not read body: %v)", resp.Status, readErr)
		}
		return fmt.Errorf("notion API error: %s %s", resp.Status, string(respBody))
	}
	return nil
}

// ValidateWebhookURL checks that the URL is safe to call (HTTPS, no private IPs).
// Exported so that API handlers can validate at config creation time.
func ValidateWebhookURL(rawURL string) error {
	_, err := validateAndResolveWebhookURL(rawURL)
	return err
}

// validateAndResolveWebhookURL validates the URL and returns the first safe
// resolved IP address. The caller can pin the HTTP connection to this IP
// to prevent DNS rebinding between validation and the actual request.
func validateAndResolveWebhookURL(rawURL string) (net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL must use HTTPS (got %q)", u.Scheme)
	}
	host := u.Hostname()

	// Block localhost and cloud metadata endpoints by name.
	blockedHosts := []string{"localhost", "metadata.google.internal"}
	for _, blocked := range blockedHosts {
		if strings.EqualFold(host, blocked) {
			return nil, fmt.Errorf("webhook URL must not point to %s", blocked)
		}
	}

	// If the host is already an IP, validate it directly.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("webhook URL must not point to a private or loopback address")
		}
		return ip, nil
	}

	// Resolve DNS and verify none of the resolved IPs are private.
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve webhook hostname %q: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("webhook hostname %q resolves to private address %s", host, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("webhook hostname %q resolved to no addresses", host)
	}
	return ips[0], nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// deliverWebhook POSTs the cycle output as JSON to a configured URL.
func (s *Service) deliverWebhook(ctx context.Context, dest models.OutputDestination, output CycleOutput) error {
	var cfg models.WebhookOutputConfig
	if err := json.Unmarshal(dest.Config, &cfg); err != nil {
		return fmt.Errorf("parse webhook config: %w", err)
	}

	// Re-validate and resolve the webhook URL at delivery time to prevent
	// TOCTOU attacks where DNS changes between config creation and delivery.
	resolvedIP, err := validateAndResolveWebhookURL(cfg.URL)
	if err != nil {
		return fmt.Errorf("webhook URL validation failed: %w", err)
	}

	jsonBody, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	method := cfg.Method
	if method == "" {
		method = http.MethodPost
	}
	if !allowedWebhookMethods[method] {
		return fmt.Errorf("webhook method %q is not allowed; use POST, PUT, or PATCH", method)
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Apply custom headers, skipping any that could override security-critical headers.
	for k, v := range cfg.Headers {
		if blockedWebhookHeaders[strings.ToLower(k)] {
			continue
		}
		req.Header.Set(k, v)
	}

	// HMAC signature if a secret is configured.
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write(jsonBody)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Signature-256", "sha256="+sig)
	}

	// Pin the request to the resolved IP to prevent DNS rebinding between
	// validation and actual connection. Pass the hostname for TLS SNI.
	u, _ := url.Parse(cfg.URL) // already validated above
	transport := s.pinnedTransport(resolvedIP, u.Hostname())
	client := &http.Client{Timeout: s.httpClient.Timeout, Transport: transport}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
		if readErr != nil {
			return fmt.Errorf("webhook error: %s (could not read body: %v)", resp.Status, readErr)
		}
		return fmt.Errorf("webhook error: %s %s", resp.Status, string(respBody))
	}
	return nil
}

// pinnedTransport returns an http.Transport that resolves all hostnames to the
// given IP, preventing DNS rebinding attacks between validation and request.
// The hostname is used for TLS SNI so certificate validation works correctly.
func (s *Service) pinnedTransport(pinnedIP net.IP, hostname string) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: hostname},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			pinnedAddr := net.JoinHostPort(pinnedIP.String(), port)
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, pinnedAddr)
		},
	}
}

// formatSlackMessage builds a plain-text Slack message for a cycle output.
func formatSlackMessage(o CycleOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s* — Cycle #%d complete\n\n", o.ProjectName, o.CycleNumber)
	fmt.Fprintf(&b, "%s\n\n", o.Summary)
	fmt.Fprintf(&b, "Tasks: %d created, %d completed, %d failed\n", o.TasksCreated, o.TasksCompleted, o.TasksFailed)
	if len(o.PRURLs) > 0 {
		b.WriteString("\nPull Requests:\n")
		for _, u := range o.PRURLs {
			fmt.Fprintf(&b, "• %s\n", u)
		}
	}
	return b.String()
}

// formatEmailHTML builds an HTML email body for a cycle output.
func formatEmailHTML(o CycleOutput) string {
	prSection := ""
	if len(o.PRURLs) > 0 {
		var prLinks strings.Builder
		prLinks.WriteString("<h3 style='margin:16px 0 8px;font-size:14px;color:#18181b;'>Pull Requests</h3><ul style='margin:0;padding-left:20px;'>")
		for _, u := range o.PRURLs {
			escaped := html.EscapeString(u)
			fmt.Fprintf(&prLinks, "<li><a href='%s'>%s</a></li>", escaped, escaped)
		}
		prLinks.WriteString("</ul>")
		prSection = prLinks.String()
	}

	safeName := html.EscapeString(o.ProjectName)
	safeSummary := html.EscapeString(o.Summary)

	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"></head>
<body style="margin:0;padding:0;background-color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="padding:40px 20px;"><tr><td align="center">
<table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:8px;overflow:hidden;">
<tr><td style="padding:32px;">
  <h1 style="margin:0 0 4px;font-size:18px;font-weight:600;color:#18181b;">%s</h1>
  <p style="margin:0 0 20px;font-size:13px;color:#71717a;">Cycle #%d — %s</p>
  <div style="font-size:14px;color:#3f3f46;line-height:1.6;">%s</div>
  <table style="margin:20px 0;width:100%%;border-collapse:collapse;">
    <tr>
      <td style="padding:8px 12px;background:#f4f4f5;border-radius:4px;text-align:center;">
        <div style="font-size:20px;font-weight:600;color:#18181b;">%d</div>
        <div style="font-size:11px;color:#71717a;">Created</div>
      </td>
      <td style="width:8px;"></td>
      <td style="padding:8px 12px;background:#f0fdf4;border-radius:4px;text-align:center;">
        <div style="font-size:20px;font-weight:600;color:#16a34a;">%d</div>
        <div style="font-size:11px;color:#71717a;">Completed</div>
      </td>
      <td style="width:8px;"></td>
      <td style="padding:8px 12px;background:#fef2f2;border-radius:4px;text-align:center;">
        <div style="font-size:20px;font-weight:600;color:#dc2626;">%d</div>
        <div style="font-size:11px;color:#71717a;">Failed</div>
      </td>
    </tr>
  </table>
  %s
</td></tr>
</table>
</td></tr></table>
</body></html>`,
		safeName, o.CycleNumber, o.Timestamp.Format("Jan 2, 2006 3:04 PM"),
		safeSummary, o.TasksCreated, o.TasksCompleted, o.TasksFailed, prSection)
}
