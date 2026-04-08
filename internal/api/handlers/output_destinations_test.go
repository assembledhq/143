package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
)

func TestValidateDestinationConfig_Slack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"valid", `{"channel_id":"C01ABC"}`, false},
		{"missing channel_id", `{"channel_name":"#general"}`, true},
		{"empty config", `{}`, true},
		{"null config", `null`, true},
		{"empty raw", ``, true},
		{"invalid json", `{bad`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationConfig("slack", json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDestinationConfig(slack, %s) error = %v, wantErr %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestinationConfig_Email(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"valid", `{"recipients":["alice@example.com"]}`, false},
		{"multiple valid", `{"recipients":["a@b.com","c@d.org"]}`, false},
		{"empty recipients", `{"recipients":[]}`, true},
		{"missing recipients", `{}`, true},
		{"invalid email format", `{"recipients":["not-an-email"]}`, true},
		{"empty string email", `{"recipients":[""]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationConfig("email", json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDestinationConfig(email, %s) error = %v, wantErr %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestinationConfig_Notion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"valid", `{"page_id":"abc123"}`, false},
		{"missing page_id", `{"page_title":"Report"}`, true},
		{"empty config", `{}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationConfig("notion", json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDestinationConfig(notion, %s) error = %v, wantErr %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestinationConfig_Webhook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"valid HTTPS with IP", `{"url":"https://8.8.8.8/hook"}`, false},
		{"with secret", `{"url":"https://8.8.8.8/hook","secret":"s3cr3t"}`, false},
		{"missing url", `{"secret":"s3cr3t"}`, true},
		{"empty config", `{}`, true},
		{"HTTP rejected", `{"url":"http://8.8.8.8/hook"}`, true},
		{"localhost rejected", `{"url":"https://localhost/hook"}`, true},
		{"private IP rejected", `{"url":"https://10.0.0.1/hook"}`, true},
		{"loopback rejected", `{"url":"https://127.0.0.1/hook"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationConfig("webhook", json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDestinationConfig(webhook, %s) error = %v, wantErr %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestinationConfig_MaxLength(t *testing.T) {
	t.Parallel()

	longVal := strings.Repeat("x", maxConfigFieldLen+1)

	tests := []struct {
		name    string
		dest    string
		config  string
		wantErr bool
	}{
		{"slack channel_id too long", "slack", `{"channel_id":"` + longVal + `"}`, true},
		{"notion page_id too long", "notion", `{"page_id":"` + longVal + `"}`, true},
		{"webhook url too long", "webhook", `{"url":"https://` + longVal + `.com"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationConfig(models.OutputDestinationType(tt.dest), json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestinationConfig_EmptyAndNull(t *testing.T) {
	t.Parallel()

	// Empty and null configs should fail for all types since required fields are missing.
	for _, destType := range []string{"slack", "email", "notion", "webhook"} {
		t.Run(destType+"_empty", func(t *testing.T) {
			err := validateDestinationConfig(models.OutputDestinationType(destType), json.RawMessage(``))
			if err == nil {
				t.Errorf("expected error for empty config on %s destination", destType)
			}
		})
		t.Run(destType+"_null", func(t *testing.T) {
			err := validateDestinationConfig(models.OutputDestinationType(destType), json.RawMessage(`null`))
			if err == nil {
				t.Errorf("expected error for null config on %s destination", destType)
			}
		})
		t.Run(destType+"_empty_object", func(t *testing.T) {
			err := validateDestinationConfig(models.OutputDestinationType(destType), json.RawMessage(`{}`))
			if err == nil {
				t.Errorf("expected error for empty object config on %s destination", destType)
			}
		})
	}
}

func TestConfigError_Error(t *testing.T) {
	t.Parallel()
	err := errMissingField("channel_id")
	if err.Error() != "missing required config field: channel_id" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestFieldTooLong_Error(t *testing.T) {
	t.Parallel()
	err := errFieldTooLong("url", 2048)
	expected := `config field "url" exceeds maximum length of 2048`
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}
