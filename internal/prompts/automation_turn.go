package prompts

import "strings"

// AutomationTurnSummary is an earlier completed review embedded as data in
// a per-target automation turn's prompt.
type AutomationTurnSummary struct {
	TurnNumber int
	HeadSHA    string
	Summary    string
}

// AutomationTurnPromptData renders the visible user message of a per-target
// automation turn (design doc 125, "Prompt"). Every field that carries
// pull-request-derived text or earlier run output is rendered inside a
// delimited UNTRUSTED DATA block; the template repeats the boundary
// instruction on every turn.
type AutomationTurnPromptData struct {
	// Goal is the automation's own goal, trusted instructions.
	Goal string
	// EventContext is the trigger's GitHub event context (title, actor,
	// paths, comment text) captured at arrival: pull-request-derived text,
	// rendered as untrusted data.
	EventContext          string
	TurnNumber            int
	Mode                  string
	ContinuationReason    string
	BaselineSHA           string
	HeadSHA               string
	BaseSHA               string
	BaseBranch            string
	NativeContext         bool
	DependencyState       string
	InterruptedCheckpoint bool
	FullReview            bool
	DiffStat              string
	ChangedFiles          []string
	ChangedFilesTruncated bool
	Summaries             []AutomationTurnSummary
	EventText             string
}

const (
	// AutomationTurnDiffStatLimit bounds the embedded diff stat.
	AutomationTurnDiffStatLimit = 4 * 1024
	// AutomationTurnChangedFilesLimit bounds the embedded changed-file list.
	AutomationTurnChangedFilesLimit = 200
	// AutomationTurnSummaryLimit bounds each embedded review summary.
	AutomationTurnSummaryLimit = 4 * 1024
	// AutomationTurnEventTextLimit bounds the embedded event text.
	AutomationTurnEventTextLimit = 8 * 1024
)

// AutomationTurnPrompt renders the per-target turn prompt with every
// untrusted block size-bounded.
func AutomationTurnPrompt(data AutomationTurnPromptData) string {
	data.Goal = strings.TrimSpace(data.Goal)
	data.DiffStat = boundUntrusted(data.DiffStat, AutomationTurnDiffStatLimit)
	if len(data.ChangedFiles) > AutomationTurnChangedFilesLimit {
		data.ChangedFiles = data.ChangedFiles[:AutomationTurnChangedFilesLimit]
		data.ChangedFilesTruncated = true
	}
	for i := range data.ChangedFiles {
		data.ChangedFiles[i] = sanitizeUntrustedLine(data.ChangedFiles[i])
	}
	for i := range data.Summaries {
		data.Summaries[i].Summary = boundUntrusted(data.Summaries[i].Summary, AutomationTurnSummaryLimit)
		data.Summaries[i].HeadSHA = sanitizeUntrustedLine(data.Summaries[i].HeadSHA)
	}
	data.EventText = boundUntrusted(data.EventText, AutomationTurnEventTextLimit)
	data.EventContext = boundUntrusted(data.EventContext, AutomationTurnEventTextLimit)
	if data.DependencyState == "" {
		data.DependencyState = "unknown"
	}
	return strings.TrimSpace(render("automation_turn.template", data))
}

// boundUntrusted trims, bounds, and neutralizes block delimiters inside
// untrusted text so a crafted PR cannot close the data block early.
func boundUntrusted(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > limit {
		text = text[:limit] + "\n[truncated]"
	}
	text = strings.ReplaceAll(text, "<<<END UNTRUSTED DATA>>>", "<<<END UNTRUSTED DATA (escaped)>>>")
	text = strings.ReplaceAll(text, "<<<BEGIN UNTRUSTED DATA", "<<<BEGIN UNTRUSTED DATA (escaped)")
	return text
}

func sanitizeUntrustedLine(line string) string {
	line = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(line, "\n", " "), "\r", " "))
	return boundUntrusted(line, 512)
}
