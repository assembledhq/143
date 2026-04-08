package handlers

import (
	"encoding/json"
	"testing"
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
		{"valid", `{"recipients":["a@b.com"]}`, false},
		{"empty recipients", `{"recipients":[]}`, true},
		{"missing recipients", `{}`, true},
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
		{"valid", `{"url":"https://example.com/hook"}`, false},
		{"with secret", `{"url":"https://example.com/hook","secret":"s3cr3t"}`, false},
		{"missing url", `{"secret":"s3cr3t"}`, true},
		{"empty config", `{}`, true},
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

func TestConfigError_Error(t *testing.T) {
	t.Parallel()
	err := errMissingField("channel_id")
	if err.Error() != "missing required config field: channel_id" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}
