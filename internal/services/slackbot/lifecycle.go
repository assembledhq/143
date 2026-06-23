package slackbot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
)

const maxSlackFinalResponseChars = 2400

type SessionLifecycleState string

const (
	SessionLifecycleStarting SessionLifecycleState = "starting"
	SessionLifecycleRunning  SessionLifecycleState = "running"
	SessionLifecycleWaiting  SessionLifecycleState = "waiting"
	SessionLifecycleComplete SessionLifecycleState = "complete"
	SessionLifecycleFailed   SessionLifecycleState = "failed"
)

type SlackRoutingMode string

const (
	SlackRoutingModeAuto       SlackRoutingMode = "auto"
	SlackRoutingModeAnswerOnly SlackRoutingMode = "answer_only"
	SlackRoutingModeStartWork  SlackRoutingMode = "start_work"
)

type SlackSessionRenderInput struct {
	OrgID                uuid.UUID
	Session              models.Session
	Link                 models.SlackSessionLink
	State                SessionLifecycleState
	Title                string
	Summary              string
	SessionURL           string
	Context              SlackSessionContextSummary
	RoutingMode          SlackRoutingMode
	Outcome              SlackSessionOutcome
	TeamSessionClaimable bool
	Actions              []SlackAction
}

type SlackSessionContextSummary struct {
	RepositoryName string
	Branch         string
	PullRequestURL string
	PreviewURL     string
	Confidence     string
	Missing        []MissingSlackContext
}

type MissingSlackContext struct {
	Kind   string
	Reason string
}

type SlackSessionOutcome struct {
	BranchURL          string
	PullRequest        *models.PullRequest
	PreviewStatus      models.PreviewStatus
	PreviewURL         string
	DiffStats          json.RawMessage
	RequiredNextAction string
}

type SlackAction struct {
	Text     string
	URL      string
	ActionID string
	Value    string
	Confirm  *SlackActionConfirm
}

type SlackActionConfirm struct {
	Title       string
	Text        string
	ConfirmText string
	DenyText    string
}

type SlackRenderedMessage struct {
	Text   string
	Blocks []ingestion.SlackBlock
}

func RenderSessionStatus(input SlackSessionRenderInput) SlackRenderedMessage {
	text := renderStatusText(input)
	return SlackRenderedMessage{
		Text:   text,
		Blocks: renderBlocks(text, defaultActions(input)),
	}
}

func RenderFinalResponse(content string, input SlackSessionRenderInput) SlackRenderedMessage {
	text := strings.TrimSpace(content)
	if len(text) > maxSlackFinalResponseChars {
		text = strings.TrimSpace(text[:maxSlackFinalResponseChars]) + "\n\n[Truncated in Slack]"
	}
	if text == "" {
		text = "143 session completed."
	}
	if input.SessionURL != "" {
		text += "\n\nSession: " + input.SessionURL
	}
	text = appendOutcomeDetails(text, input.Outcome)
	if input.Link.TeamSession {
		text = strings.TrimSpace(text) + "\n\n" + TeamSessionLine()
	}
	return SlackRenderedMessage{
		Text:   text,
		Blocks: renderBlocks(text, defaultActions(input)),
	}
}

func renderStatusText(input SlackSessionRenderInput) string {
	lines := []string{statusTitle(input)}
	if summary := strings.TrimSpace(input.Summary); summary != "" {
		lines = append(lines, summary)
	}
	if contextLines := renderContextLines(input); len(contextLines) > 0 {
		lines = append(lines, "", strings.Join(contextLines, "\n"))
	}
	if input.SessionURL != "" {
		lines = append(lines, "", "Session: "+input.SessionURL)
	}
	if input.Link.TeamSession {
		lines = append(lines, "", TeamSessionLine())
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func statusTitle(input SlackSessionRenderInput) string {
	if title := strings.TrimSpace(input.Title); title != "" {
		return title
	}
	switch input.State {
	case SessionLifecycleStarting:
		return "Starting a 143 session"
	case SessionLifecycleRunning:
		return "Running 143 session"
	case SessionLifecycleWaiting:
		return "Waiting for your input"
	case SessionLifecycleComplete:
		return "Completed"
	case SessionLifecycleFailed:
		return "Failed"
	default:
		return "143 session update"
	}
}

func renderContextLines(input SlackSessionRenderInput) []string {
	lines := []string{}
	if input.Context.RepositoryName != "" {
		lines = append(lines, "Repo: "+slackCodeSpan(input.Context.RepositoryName))
	}
	if input.Context.Branch != "" {
		lines = append(lines, "Branch: "+slackCodeSpan(input.Context.Branch))
	}
	if input.Context.PullRequestURL != "" {
		lines = append(lines, "PR: "+input.Context.PullRequestURL)
	}
	if input.Context.PreviewURL != "" {
		lines = append(lines, "Preview: "+input.Context.PreviewURL)
	}
	if label := routingModeLabel(input.RoutingMode); label != "" {
		lines = append(lines, "Mode: "+label)
	}
	return lines
}

func slackCodeSpan(value string) string {
	return "`" + strings.ReplaceAll(strings.TrimSpace(value), "`", "'") + "`"
}

func routingModeLabel(mode SlackRoutingMode) string {
	switch mode {
	case SlackRoutingModeAuto:
		return "Auto"
	case SlackRoutingModeAnswerOnly:
		return "Answer only"
	case SlackRoutingModeStartWork:
		return "Start work"
	default:
		return ""
	}
}

func appendOutcomeDetails(text string, outcome SlackSessionOutcome) string {
	lines := []string{}
	if outcome.BranchURL != "" {
		lines = append(lines, "Branch: "+outcome.BranchURL)
	}
	if outcome.PullRequest != nil && strings.TrimSpace(outcome.PullRequest.GitHubPRURL) != "" {
		lines = append(lines, pullRequestOutcomeLine(*outcome.PullRequest))
	}
	if outcome.PreviewURL != "" {
		if outcome.PreviewStatus != "" {
			lines = append(lines, fmt.Sprintf("Preview: %s - %s", outcome.PreviewStatus, outcome.PreviewURL))
		} else {
			lines = append(lines, "Preview: "+outcome.PreviewURL)
		}
	}
	if line := diffStatsOutcomeLine(outcome.DiffStats); line != "" {
		lines = append(lines, line)
	}
	if outcome.RequiredNextAction != "" {
		lines = append(lines, "Next: "+outcome.RequiredNextAction)
	}
	if len(lines) == 0 {
		return text
	}
	return strings.TrimSpace(text) + "\n\n" + strings.Join(lines, "\n")
}

func pullRequestOutcomeLine(pr models.PullRequest) string {
	line := "PR: " + strings.TrimSpace(pr.GitHubPRURL)
	metadata := []string{}
	if pr.Status != "" {
		metadata = append(metadata, string(pr.Status))
	}
	if pr.CIStatus != "" {
		metadata = append(metadata, "CI "+string(pr.CIStatus))
	}
	if len(metadata) > 0 {
		line += " (" + strings.Join(metadata, ", ") + ")"
	}
	return line
}

func diffStatsOutcomeLine(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var stats struct {
		FilesChanged int `json:"files_changed"`
		Added        int `json:"added"`
		Removed      int `json:"removed"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return "Changes: diff available in 143"
	}
	if stats.FilesChanged == 0 && stats.Added == 0 && stats.Removed == 0 {
		return ""
	}
	fileLabel := "files"
	if stats.FilesChanged == 1 {
		fileLabel = "file"
	}
	return fmt.Sprintf("Changes: %d %s, +%d/-%d", stats.FilesChanged, fileLabel, stats.Added, stats.Removed)
}

func defaultActions(input SlackSessionRenderInput) []SlackAction {
	actions := []SlackAction{}
	if input.SessionURL != "" {
		actions = append(actions, SlackAction{Text: "Join session", URL: input.SessionURL})
	}
	actions = append(actions, input.Actions...)
	return actions
}

func renderBlocks(text string, actions []SlackAction) []ingestion.SlackBlock {
	blocks := []ingestion.SlackBlock{{
		Type: "section",
		Text: &ingestion.SlackTextObject{Type: "mrkdwn", Text: text},
	}}
	elements := []map[string]any{}
	for _, action := range actions {
		label := strings.TrimSpace(action.Text)
		if label == "" {
			continue
		}
		element := map[string]any{
			"type": "button",
			"text": map[string]string{"type": "plain_text", "text": label},
		}
		if action.URL != "" {
			element["url"] = action.URL
		}
		if action.ActionID != "" {
			element["action_id"] = action.ActionID
		}
		if action.Value != "" {
			element["value"] = action.Value
		}
		if action.Confirm != nil {
			confirmText := strings.TrimSpace(action.Confirm.ConfirmText)
			if confirmText == "" {
				confirmText = "Confirm"
			}
			denyText := strings.TrimSpace(action.Confirm.DenyText)
			if denyText == "" {
				denyText = "Cancel"
			}
			element["confirm"] = map[string]any{
				"title":   map[string]string{"type": "plain_text", "text": action.Confirm.Title},
				"text":    map[string]string{"type": "mrkdwn", "text": action.Confirm.Text},
				"confirm": map[string]string{"type": "plain_text", "text": confirmText},
				"deny":    map[string]string{"type": "plain_text", "text": denyText},
			}
		}
		elements = append(elements, element)
	}
	if len(elements) > 0 {
		blocks = append(blocks, ingestion.SlackBlock{Type: "actions", Elements: elements})
	}
	return blocks
}

func TeamSessionLine() string {
	return "_This is a team session started from Slack without a linked 143 user._"
}
