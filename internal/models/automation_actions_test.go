package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAutomationActionConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		valid     bool
	}{
		{"Slack only", `{"actions":["slack_notification"],"slack_channel_id":"C0123456789"}`, true},
		{"GitHub only", `{"actions":["github_issue_comment"],"repository":"owner/repo"}`, true},
		{"GitHub comment updates only", `{"actions":["github_issue_comment_update"],"repository":"owner/repo"}`, true},
		{"GitHub comment updates require a repository", `{"actions":["github_issue_comment_update"]}`, false},
		{"GitHub inline review only", `{"actions":["github_pull_request_review"],"repository":"owner/repo"}`, true},
		{"GitHub inline reviews require a repository", `{"actions":["github_pull_request_review"]}`, false},
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

func TestAutomationActionCommentUpdateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*AutomationActionRequest)
		grant   AutomationActionKind
		wantErr string
	}{
		{"explicit update grant", func(*AutomationActionRequest) {}, AutomationActionCommentUpdate, ""},
		{"create grant does not allow edits", func(*AutomationActionRequest) {}, AutomationActionComment, "action kind is not configured"},
		{"missing source receipt", func(r *AutomationActionRequest) { r.SourceActionID = "" }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"malformed source receipt", func(r *AutomationActionRequest) { r.SourceActionID = "123" }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"zero source receipt", func(r *AutomationActionRequest) { r.SourceActionID = uuid.Nil.String() }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"uppercase source receipt", func(r *AutomationActionRequest) { r.SourceActionID = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA" }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"URN source receipt", func(r *AutomationActionRequest) { r.SourceActionID = "urn:uuid:" + r.SourceActionID }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"braced source receipt", func(r *AutomationActionRequest) { r.SourceActionID = "{" + r.SourceActionID + "}" }, AutomationActionCommentUpdate, "comment updates require a canonical nonzero source_action_id UUID"},
		{"create rejects update source field", func(r *AutomationActionRequest) { r.Kind = AutomationActionComment }, AutomationActionComment, "source_action_id is only valid for comment updates"},
		{"missing PR", func(r *AutomationActionRequest) { r.PRNumber, r.HeadSHA = 0, "" }, AutomationActionCommentUpdate, "GitHub actions require a PR precondition"},
		{"abbreviated head", func(r *AutomationActionRequest) { r.HeadSHA = "abc123" }, AutomationActionCommentUpdate, "a PR precondition requires a positive pr_number and full lowercase head_sha"},
		{"blank text", func(r *AutomationActionRequest) { r.Text = " " }, AutomationActionCommentUpdate, "provide bounded text, without properties"},
		{"oversized text", func(r *AutomationActionRequest) { r.Text = strings.Repeat("a", (32<<10)+1) }, AutomationActionCommentUpdate, "provide bounded text, without properties"},
		{"source marker spoof", func(r *AutomationActionRequest) { r.Text += "\n<!-- 143-automation-action:spoof -->" }, AutomationActionCommentUpdate, "comment text contains a reserved automation marker"},
		{"update marker spoof", func(r *AutomationActionRequest) { r.Text += "\n<!-- 143-automation-comment-update:spoof -->" }, AutomationActionCommentUpdate, "comment text contains a reserved automation marker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := AutomationActionRequest{
				AutomationActionKey: AutomationActionKey{"review:42", "edit"},
				Kind:                AutomationActionCommentUpdate,
				PRNumber:            42,
				HeadSHA:             strings.Repeat("a", 40),
				Text:                "Updated review",
				SourceActionID:      uuid.NewString(),
			}
			tt.change(&request)
			config := AutomationActionConfig{Actions: []AutomationActionKind{tt.grant}, Repository: "owner/repo"}
			err := request.ValidateFor(config)
			if tt.wantErr == "" {
				require.NoError(t, err, "an explicitly granted and scoped update should validate")
			} else {
				require.EqualError(t, err, tt.wantErr, "reject invalid or ungranted updates")
			}
		})
	}
}

func TestAutomationActionCreateSerialization(t *testing.T) {
	t.Parallel()
	const storedRequest = `{"operation_key":"review:42","action_key":"create","kind":"github_issue_comment","pr_number":42,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","text":"Original review"}`
	var request AutomationActionRequest
	require.NoError(t, json.Unmarshal([]byte(storedRequest), &request), "decode a pre-update create request")
	require.NoError(t, request.Validate(), "pre-update create requests remain valid")
	raw, err := json.Marshal(request)
	require.NoError(t, err, "encode a pre-update create request")
	require.Equal(t, storedRequest, string(raw), "create request bytes must remain stable for the persisted digest")
}

func TestAutomationActionGitHubReviewValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*AutomationActionRequest)
		grant   AutomationActionKind
		wantErr string
	}{
		{"explicit review grant with optional summary omitted", func(*AutomationActionRequest) {}, AutomationActionGitHubReview, ""},
		{"comment grant does not allow inline reviews", func(*AutomationActionRequest) {}, AutomationActionComment, "action kind is not configured"},
		{"missing PR", func(r *AutomationActionRequest) { r.PRNumber, r.HeadSHA = 0, "" }, AutomationActionGitHubReview, "GitHub actions require a PR precondition"},
		{"missing head", func(r *AutomationActionRequest) { r.HeadSHA = "" }, AutomationActionGitHubReview, "a PR precondition requires a positive pr_number and full lowercase head_sha"},
		{"missing comments", func(r *AutomationActionRequest) { r.Comments = nil }, AutomationActionGitHubReview, "GitHub PR reviews require one to five inline comments"},
		{"too many comments", func(r *AutomationActionRequest) {
			r.Comments = make([]AutomationActionReviewComment, 6)
		}, AutomationActionGitHubReview, "GitHub PR reviews require one to five inline comments"},
		{"accepts a distinct comment on the same file", func(r *AutomationActionRequest) {
			r.Comments = append(r.Comments, AutomationActionReviewComment{Path: r.Comments[0].Path, Line: 2, Body: "Second finding"})
		}, AutomationActionGitHubReview, ""},
		{"duplicate file and line", func(r *AutomationActionRequest) {
			r.Comments = append(r.Comments, AutomationActionReviewComment{Path: r.Comments[0].Path, Line: 1, Body: "Another finding"})
		}, AutomationActionGitHubReview, "inline comments must have distinct file and line anchors"},
		{"zero line", func(r *AutomationActionRequest) { r.Comments[0].Line = 0 }, AutomationActionGitHubReview, "inline comment 1 requires a relative file path and positive line"},
		{"blank finding", func(r *AutomationActionRequest) { r.Comments[0].Body = " \n" }, AutomationActionGitHubReview, "inline comment 1 requires bounded, nonempty text"},
		{"oversized finding", func(r *AutomationActionRequest) { r.Comments[0].Body = strings.Repeat("é", 2049) }, AutomationActionGitHubReview, "inline comment 1 requires bounded, nonempty text"},
		{"oversized summary", func(r *AutomationActionRequest) { r.Text = strings.Repeat("é", 2049) }, AutomationActionGitHubReview, "GitHub PR reviews accept optional bounded summary text, without properties"},
		{"properties", func(r *AutomationActionRequest) { r.Properties = map[string]string{} }, AutomationActionGitHubReview, "GitHub PR reviews accept optional bounded summary text, without properties"},
		{"source action", func(r *AutomationActionRequest) { r.SourceActionID = uuid.NewString() }, AutomationActionGitHubReview, "source_action_id is only valid for comment updates"},
		{"spoofed summary marker", func(r *AutomationActionRequest) { r.Text = AutomationActionReviewMarkerPrefix + "spoof -->" }, AutomationActionGitHubReview, "review text contains a reserved automation marker"},
		{"summary team mention", func(r *AutomationActionRequest) { r.Text = "Ask @owner/team" }, AutomationActionGitHubReview, "review text must leave mentions to the server"},
		{"finding user mention", func(r *AutomationActionRequest) { r.Comments[0].Body = "Ask @someone" }, AutomationActionGitHubReview, "inline comment 1 must leave mentions to the server"},
		{"encoded finding mention", func(r *AutomationActionRequest) { r.Comments[0].Body = "Ask &#64;someone" }, AutomationActionGitHubReview, "inline comment 1 must leave mentions to the server"},
		{"spoofed finding marker", func(r *AutomationActionRequest) {
			r.Comments[0].Body += AutomationActionCommentMarkerPrefix + "spoof -->"
		}, AutomationActionGitHubReview, "inline comment 1 contains a reserved automation marker"},
		{"timeline comment rejects inline comments", func(r *AutomationActionRequest) {
			r.Kind = AutomationActionComment
			r.Text = "Timeline comment"
		}, AutomationActionComment, "comments are only valid for GitHub PR reviews"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := AutomationActionRequest{
				AutomationActionKey: AutomationActionKey{"review:42", "inline"},
				Kind:                AutomationActionGitHubReview,
				PRNumber:            42,
				HeadSHA:             strings.Repeat("a", 40),
				Comments:            []AutomationActionReviewComment{{Path: "assets/Écran de test.tsx", Line: 1, Body: "Use the documented component."}},
			}
			tt.change(&request)
			err := request.ValidateFor(AutomationActionConfig{Actions: []AutomationActionKind{tt.grant}, Repository: "owner/repo"})
			if tt.wantErr == "" {
				require.NoError(t, err, "an explicitly granted review with valid inline anchors should validate")
			} else {
				require.EqualError(t, err, tt.wantErr, "reject invalid or ungranted inline reviews")
			}
		})
	}
}

func TestAutomationActionReviewPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		path  string
		valid bool
	}{
		{"root file", "README.md", true},
		{"unicode and spaces", "assets/Écran de test.tsx", true},
		{"leading dot in a component name", ".github/file.yml", true},
		{"empty path", "", false},
		{"absolute path", "/assets/item.tsx", false},
		{"current directory component", "assets/./item.tsx", false},
		{"parent directory component", "assets/../item.tsx", false},
		{"empty component", "assets//item.tsx", false},
		{"trailing slash", "assets/", false},
		{"backslash", `assets\item.tsx`, false},
		{"ASCII control", "assets/item\n.tsx", false},
		{"unicode control", "assets/item\u0085.tsx", false},
		{"invalid UTF-8", "assets/item\xff.tsx", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, validAutomationReviewPath(tt.path), "only clean relative Git file paths may anchor findings")
		})
	}
}

func TestAutomationActionConfigJSONRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		raw      string
		expected AutomationActionConfig
	}{
		{
			name: "omitted optional properties",
			raw:  `{"actions":["slack_notification"],"slack_channel_id":"C0123456789"}`,
			expected: AutomationActionConfig{
				Actions:        []AutomationActionKind{AutomationActionSlack},
				SlackChannelID: "C0123456789",
			},
		},
		{
			name: "empty optional properties from the configuration form",
			raw:  `{"actions":["slack_notification","github_label"],"repository":"owner/repo","label":"needs-review","slack_channel_id":"C0123456789","notion_properties":{},"notion_data_source_id":""}`,
			expected: AutomationActionConfig{
				Actions:        []AutomationActionKind{AutomationActionLabel, AutomationActionSlack},
				Repository:     "owner/repo",
				Label:          "needs-review",
				SlackChannelID: "C0123456789",
			},
		},
		{
			name: "configured Notion properties are preserved",
			raw:  `{"actions":["notion_tracking_row"],"notion_data_source_id":"379d5706-2bc0-8021-a0e6-000b04d8902d","notion_properties":{"Report":"title","Summary":"rich_text"}}`,
			expected: AutomationActionConfig{
				Actions:            []AutomationActionKind{AutomationActionNotion},
				NotionDataSourceID: "379d5706-2bc0-8021-a0e6-000b04d8902d",
				NotionProperties: map[string]AutomationActionPropertyType{
					"Report":  AutomationActionPropertyTitle,
					"Summary": AutomationActionPropertyText,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config, err := ParseAutomationActionConfig(json.RawMessage(tt.raw))
			require.NoError(t, err, "the configured destinations should be valid")
			stored, err := json.Marshal(config)
			require.NoError(t, err, "the action destination should serialize for storage")
			var restored AutomationActionConfig
			require.NoError(t, json.Unmarshal(stored, &restored), "the stored action destination should decode")
			require.Equal(t, config, restored.Canonical(), "persisting an action must not change its configuration identity")
			require.Equal(t, tt.expected, config, "canonicalization should preserve configured destinations and normalize empty properties")
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
