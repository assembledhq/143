package models

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestAutomationActionConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		valid     bool
	}{
		{"Slack only", `{"actions":["slack_notification"],"slack_channel_id":"C0123456789"}`, true},
		{"GitHub only", `{"actions":["github_issue_comment"],"repository":"owner/repo"}`, true},
		{"Notion arbitrary mapping", `{"actions":["notion_tracking_row"],"notion_data_source_id":"379d57062bc08021a0e6000b04d8902d","notion_properties":{"Report":"title","Summary":"rich_text","State":"select"}}`, true},
		{"disabled kinds need no destinations", `{"actions":["github_label"],"repository":"owner/repo","label":"needs-security"}`, true},
		{"no actions", `{}`, false}, {"unknown action", `{"actions":["http_request"]}`, false},
		{"duplicate", `{"actions":["slack_notification","slack_notification"],"slack_channel_id":"C0123456789"}`, false},
		{"missing selected destination", `{"actions":["slack_notification"]}`, false},
		{"unknown config", `{"actions":["slack_notification"],"slack_channel_id":"C0123456789","url":"https://evil.test"}`, false},
		{"trailing JSON", `{} {}`, false},
		{"traversal", `{"actions":["github_issue_comment"],"repository":"../repo"}`, false},
		{"missing title", `{"actions":["notion_tracking_row"],"notion_data_source_id":"379d57062bc08021a0e6000b04d8902d","notion_properties":{"Report":"url"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseAutomationActionConfig(json.RawMessage(tt.raw))
			require.Equal(t, tt.valid, err == nil, "only selected operations need valid destinations: %v", err)
		})
	}
}
func TestAutomationActionRequestValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*AutomationActionRequest)
		valid  bool
	}{
		{"single notification", func(*AutomationActionRequest) {}, true},
		{"another step same kind", func(r *AutomationActionRequest) { r.ActionKey = "notify-again" }, true},
		{"invalid key", func(r *AutomationActionRequest) { r.OperationKey = "a\n" }, false},
		{"too long key", func(r *AutomationActionRequest) { r.ActionKey = strings.Repeat("a", 161) }, false},
		{"no text", func(r *AutomationActionRequest) { r.Text = "" }, false},
		{"too much text", func(r *AutomationActionRequest) { r.Text = strings.Repeat("a", 4097) }, false},
		{"partial PR precondition", func(r *AutomationActionRequest) { r.PRNumber = 42 }, false},
		{"head without PR", func(r *AutomationActionRequest) { r.HeadSHA = strings.Repeat("a", 40) }, false},
		{"GitHub requires PR", func(r *AutomationActionRequest) { r.Kind = AutomationActionComment }, false},
		{"valid PR", func(r *AutomationActionRequest) {
			r.Kind = AutomationActionComment
			r.PRNumber = 42
			r.HeadSHA = strings.Repeat("a", 40)
		}, true},
		{"label rejects unused text", func(r *AutomationActionRequest) {
			r.Kind = AutomationActionLabel
			r.PRNumber = 42
			r.HeadSHA = strings.Repeat("a", 40)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := AutomationActionRequest{AutomationActionKey: AutomationActionKey{"daily:2026-09-22", "notify"}, Kind: AutomationActionSlack, Text: "Daily report"}
			tt.change(&r)
			require.Equal(t, tt.valid, r.Validate() == nil, "validate independent payload and key")
		})
	}
}
func TestAutomationActionNotionProperties(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		props map[string]string
		valid bool
	}{
		{"arbitrary report", map[string]string{"Report": "Daily", "Summary": "Ready", "State": "Done", "Link": "https://example.org", "Date": "2026-09-22"}, true},
		{"minimal title", map[string]string{"Report": "Daily"}, true},
		{"missing title", map[string]string{"Summary": "Daily"}, false},
		{"unknown property", map[string]string{"Report": "Daily", "Owner": "someone"}, false},
		{"bad URL", map[string]string{"Report": "Daily", "Link": "javascript:evil"}, false},
		{"bad date", map[string]string{"Report": "Daily", "Date": "tomorrow"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := AutomationActionConfig{Actions: []AutomationActionKind{AutomationActionNotion}, NotionProperties: map[string]AutomationActionPropertyType{"Report": AutomationActionPropertyTitle, "Summary": AutomationActionPropertyText, "State": AutomationActionPropertySelect, "Link": AutomationActionPropertyURL, "Date": AutomationActionPropertyDate}}
			r := AutomationActionRequest{AutomationActionKey: AutomationActionKey{"daily", "row"}, Kind: AutomationActionNotion, Properties: tt.props}
			require.Equal(t, tt.valid, r.ValidateFor(c) == nil, "honor configured property types and names")
		})
	}
}
func TestAutomationActionResult(t *testing.T) {
	t.Parallel()
	now := time.Now()
	expired := now.Add(-time.Second)
	future := now.Add(time.Minute)
	tests := []struct {
		name    string
		actions []AutomationAction
		want    AutomationActionDeliveryStatus
	}{
		{"none", nil, AutomationActionNotStarted},
		{"one completed is delivered", []AutomationAction{{Status: AutomationActionSucceeded}}, AutomationActionDelivered},
		{"two same kind", []AutomationAction{{Kind: AutomationActionSlack, Status: AutomationActionSucceeded}, {Kind: AutomationActionSlack, Status: AutomationActionSucceeded}}, AutomationActionDelivered},
		{"partial", []AutomationAction{{Status: AutomationActionSucceeded}, {Status: AutomationActionFailed}}, AutomationActionPartial},
		{"sending", []AutomationAction{{Status: AutomationActionSending, SendDeadlineAt: &future}}, AutomationActionInProgress},
		{"expired", []AutomationAction{{Status: AutomationActionSending, SendDeadlineAt: &expired}}, AutomationActionNeedsAttention},
		{"unknown", []AutomationAction{{Status: AutomationActionUnknown}}, AutomationActionNeedsAttention},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, AutomationActionResultFor(tt.actions, now).Status, "aggregate only recorded actions without requiring a bundle")
		})
	}
}
func TestAutomationActionEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{
		{"kind", AutomationActionSlack.Validate, true}, {"bad kind", AutomationActionKind("x").Validate, false},
		{"status", AutomationActionUnknown.Validate, true}, {"bad status", AutomationActionStatus("x").Validate, false},
		{"delivery", AutomationActionDelivered.Validate, true}, {"bad delivery", AutomationActionDeliveryStatus("x").Validate, false},
		{"property", AutomationActionPropertyTitle.Validate, true}, {"bad property", AutomationActionPropertyType("x").Validate, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, tt.validate() == nil, "validate public enum value")
		})
	}
}
