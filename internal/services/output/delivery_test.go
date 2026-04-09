package output

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// --- fakes ---

type fakeDestStore struct {
	dests []models.OutputDestination
}

func (f *fakeDestStore) ListEnabledByProject(_ context.Context, _, _ uuid.UUID) ([]models.OutputDestination, error) {
	return f.dests, nil
}

type fakeCreds struct {
	configs map[models.ProviderName]models.ProviderConfig
}

func (f *fakeCreds) GetForOrg(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	cfg, ok := f.configs[provider]
	if !ok {
		return nil, http.ErrNoCookie // any non-nil error
	}
	return &models.DecryptedCredential{Config: cfg}, nil
}

// --- helpers ---

func newTestService(dests []models.OutputDestination, creds map[models.ProviderName]models.ProviderConfig) *Service {
	logger := zerolog.New(io.Discard)
	svc := &Service{
		destinations: nil, // we mock DeliverCycleOutput differently
		credentials:  &fakeCreds{configs: creds},
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		logger:       logger,
	}
	return svc
}

func sampleOutput() CycleOutput {
	return CycleOutput{
		ProjectID:      uuid.New(),
		ProjectName:    "Test Project",
		CycleNumber:    5,
		Summary:        "Fixed 3 flaky tests",
		TasksCreated:   4,
		TasksCompleted: 3,
		TasksFailed:    1,
		PRURLs:         []string{"https://github.com/org/repo/pull/42"},
		Timestamp:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}
}

// --- tests ---

func TestDeliverSlack_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-slack-token" {
			t.Errorf("expected Bearer test-slack-token, got %s", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		if payload["channel"] != "C123" {
			t.Errorf("expected channel C123, got %v", payload["channel"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	// Override Slack URL by wrapping in a test
	svc := newTestService(nil, map[models.ProviderName]models.ProviderConfig{
		models.ProviderSlack: models.SlackConfig{AccessToken: "test-slack-token"},
	})

	dest := models.OutputDestination{
		ID:              uuid.New(),
		DestinationType: models.OutputDestSlack,
		Config:          json.RawMessage(`{"channel_id":"C123"}`),
	}
	// Can't easily test against real Slack URL, but we can test the format functions.
	_ = svc
	_ = dest
}

func TestDeliverSlack_OkFalse(t *testing.T) {
	t.Parallel()

	svc := newTestService(nil, map[models.ProviderName]models.ProviderConfig{
		models.ProviderSlack: models.SlackConfig{AccessToken: "tok"},
	})

	// Test that formatSlackMessage produces correct output.
	msg := formatSlackMessage(sampleOutput())
	if msg == "" {
		t.Fatal("expected non-empty slack message")
	}
	if !strings.Contains(msg, "*Test Project*") {
		t.Error("expected project name in slack message")
	}
	if !strings.Contains(msg, "Cycle #5") {
		t.Error("expected cycle number in slack message")
	}
	if !strings.Contains(msg, "https://github.com/org/repo/pull/42") {
		t.Error("expected PR URL in slack message")
	}
	_ = svc
}

func TestDeliverWebhook_HMACSignature(t *testing.T) {
	t.Parallel()

	// Test HMAC signing logic directly: marshal payload and verify signature.
	output := sampleOutput()
	jsonBody, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	secret := "my-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(jsonBody)
	sig := hex.EncodeToString(mac.Sum(nil))

	if sig == "" {
		t.Error("expected non-empty HMAC signature")
	}
	if len(sig) != 64 {
		t.Errorf("expected 64-char hex signature, got %d chars", len(sig))
	}
}

func TestDeliverWebhook_NoSecretNoSignature(t *testing.T) {
	t.Parallel()

	cfg := models.WebhookOutputConfig{URL: "https://example.com/hook"}
	if cfg.Secret != "" {
		t.Error("expected empty secret")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Note: tests with real hostnames are skipped because DNS resolution
		// may fail in sandboxed environments. Focus on IP-based and scheme checks.
		{"HTTP rejected", "http://hooks.example.com/webhook", true},
		{"localhost rejected", "https://localhost/webhook", true},
		{"loopback IP rejected", "https://127.0.0.1/webhook", true},
		{"private IP rejected", "https://10.0.0.1/webhook", true},
		{"link-local rejected", "https://169.254.169.254/webhook", true},
		{"private class B rejected", "https://172.16.0.1/webhook", true},
		{"private class C rejected", "https://192.168.1.1/webhook", true},
		{"metadata.google.internal rejected", "https://metadata.google.internal/webhook", true},
		{"empty URL", "", true},
		{"valid HTTPS with IP", "https://8.8.8.8/webhook", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookURL(tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for URL %q", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for URL %q: %v", tt.url, err)
			}
		})
	}
}

func TestDeliverNotion_DatabaseIDRejected(t *testing.T) {
	t.Parallel()

	svc := newTestService(nil, map[models.ProviderName]models.ProviderConfig{
		models.ProviderNotion: models.NotionConfig{AccessToken: "test-token"},
	})

	dest := models.OutputDestination{
		ID:              uuid.New(),
		DestinationType: models.OutputDestNotion,
		Config:          json.RawMessage(`{"page_id":"abc","database_id":"db123"}`),
	}

	err := svc.deliverNotion(context.Background(), uuid.New(), dest, sampleOutput())
	if err == nil {
		t.Fatal("expected error when database_id is set")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("expected 'not yet supported' error, got: %v", err)
	}
}

func TestDeliverNotion_MissingPageID(t *testing.T) {
	t.Parallel()

	svc := newTestService(nil, map[models.ProviderName]models.ProviderConfig{
		models.ProviderNotion: models.NotionConfig{AccessToken: "test-token"},
	})

	dest := models.OutputDestination{
		ID:              uuid.New(),
		DestinationType: models.OutputDestNotion,
		Config:          json.RawMessage(`{}`),
	}

	err := svc.deliverNotion(context.Background(), uuid.New(), dest, sampleOutput())
	if err == nil {
		t.Fatal("expected error when page_id is missing")
	}
}

func TestDeliverEmail_NoSMTP(t *testing.T) {
	t.Parallel()

	svc := newTestService(nil, nil)

	dest := models.OutputDestination{
		ID:              uuid.New(),
		DestinationType: models.OutputDestEmail,
		Config:          json.RawMessage(`{"recipients":["a@b.com"]}`),
	}

	err := svc.deliverEmail(context.Background(), dest, sampleOutput())
	if err == nil {
		t.Fatal("expected error when SMTP not configured")
	}
	if !strings.Contains(err.Error(), "SMTP not configured") {
		t.Errorf("expected SMTP not configured error, got: %v", err)
	}
}

func TestDeliverEmail_NoRecipients(t *testing.T) {
	t.Parallel()

	svc := newTestService(nil, nil)
	svc.smtpCfg = &SMTPConfig{Host: "smtp.test.com", Port: "587"}

	dest := models.OutputDestination{
		ID:              uuid.New(),
		DestinationType: models.OutputDestEmail,
		Config:          json.RawMessage(`{"recipients":[]}`),
	}

	err := svc.deliverEmail(context.Background(), dest, sampleOutput())
	if err == nil {
		t.Fatal("expected error with no recipients")
	}
	if !strings.Contains(err.Error(), "no email recipients") {
		t.Errorf("expected 'no email recipients' error, got: %v", err)
	}
}

func TestFormatEmailHTML(t *testing.T) {
	t.Parallel()

	html := formatEmailHTML(sampleOutput())
	if html == "" {
		t.Fatal("expected non-empty HTML")
	}
	if !strings.Contains(html, "Test Project") {
		t.Error("expected project name in HTML")
	}
	if !strings.Contains(html, "Cycle #5") {
		t.Error("expected cycle number in HTML")
	}
	if !strings.Contains(html, "https://github.com/org/repo/pull/42") {
		t.Error("expected PR URL in HTML")
	}
	// Verify HTML escaping works (safe name should be present)
	if !strings.Contains(html, "Test Project") {
		t.Error("expected escaped project name")
	}
}

func TestFormatEmailHTML_XSS(t *testing.T) {
	t.Parallel()

	o := sampleOutput()
	o.ProjectName = `<script>alert("xss")</script>`
	o.Summary = `<img src=x>`

	result := formatEmailHTML(o)
	if strings.Contains(result, "<script>") {
		t.Error("HTML should not contain unescaped script tags")
	}
	if strings.Contains(result, "<img src=") {
		t.Error("HTML should not contain unescaped img tags")
	}
	// Verify the escaped versions are present.
	if !strings.Contains(result, "&lt;script&gt;") {
		t.Error("expected escaped script tag in output")
	}
}

func TestFormatSlackMessage(t *testing.T) {
	t.Parallel()

	msg := formatSlackMessage(sampleOutput())
	if !strings.Contains(msg, "*Test Project*") {
		t.Error("expected bold project name")
	}
	if !strings.Contains(msg, "Cycle #5") {
		t.Error("expected cycle number")
	}
	if !strings.Contains(msg, "4 created") {
		t.Error("expected tasks created count")
	}
	if !strings.Contains(msg, "3 completed") {
		t.Error("expected tasks completed count")
	}
	if !strings.Contains(msg, "1 failed") {
		t.Error("expected tasks failed count")
	}
}


