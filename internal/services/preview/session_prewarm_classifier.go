package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

const defaultSessionPrewarmClassifierTimeout = 5 * time.Second
const maxSessionPrewarmTextChars = 500

var (
	sessionPrewarmURLPattern       = regexp.MustCompile(`https?://\S+`)
	sessionPrewarmCodeFencePattern = regexp.MustCompile("(?s)```.*?```")
	sessionPrewarmSpecials         = strings.NewReplacer("<", " ", ">", " ", "{", " ", "}", " ")
)

type SessionPrewarmLLM interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type SessionPrewarmClassifier struct {
	llm     SessionPrewarmLLM
	timeout time.Duration
}

type SessionPrewarmClassifierInput struct {
	RepositoryFullName          string
	RepositoryLanguage          string
	SessionSource               string
	UserPrompt                  string
	IssueLabels                 []string
	IssueType                   string
	PreviewHistory              string
	HistoricalPreviewOpenCount  int
	CapacitySummary             string
	Phase                       string
	ChangedFileKinds            []string
}

type SessionPrewarmClassifierResult struct {
	Decision    models.PreviewSpeculativeDecision
	Confidence  float64
	Reason      string
	Explanation string
	Status      string
}

func NewSessionPrewarmClassifier(llm SessionPrewarmLLM, timeout time.Duration) *SessionPrewarmClassifier {
	if timeout <= 0 {
		timeout = defaultSessionPrewarmClassifierTimeout
	}
	return &SessionPrewarmClassifier{llm: llm, timeout: timeout}
}

func (c *SessionPrewarmClassifier) Classify(ctx context.Context, input SessionPrewarmClassifierInput) SessionPrewarmClassifierResult {
	if c == nil || c.llm == nil {
		return sessionPrewarmClassifierFallback("classifier_error", "Classifier is not configured.", "decided")
	}
	previewHistory := strings.TrimSpace(input.PreviewHistory)
	if input.HistoricalPreviewOpenCount < 20 {
		previewHistory = fmt.Sprintf("Insufficient history (only %d sessions with opened previews; need 20 for a reliable signal).", input.HistoricalPreviewOpenCount)
	}
	systemPrompt := prompts.SessionPreviewPrewarmClassifierPrompt()
	userPrompt := prompts.SessionPreviewPrewarmClassifierUserPrompt(prompts.SessionPreviewPrewarmClassifierUserPromptData{
		RepositoryFullName: strings.TrimSpace(input.RepositoryFullName),
		RepositoryLanguage: strings.TrimSpace(input.RepositoryLanguage),
		SessionSource:      strings.TrimSpace(input.SessionSource),
		UserPrompt:         sanitizeSessionPrewarmClassifierText(input.UserPrompt),
		IssueLabels:        sanitizeSessionPrewarmList(input.IssueLabels),
		IssueType:          sanitizeSessionPrewarmClassifierText(input.IssueType),
		PreviewHistory:     previewHistory,
		CapacitySummary:    strings.TrimSpace(input.CapacitySummary),
		Phase:              strings.TrimSpace(input.Phase),
		ChangedFileKinds:   sanitizeSessionPrewarmList(input.ChangedFileKinds),
	})
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultSessionPrewarmClassifierTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := c.llm.Complete(callCtx, systemPrompt, userPrompt)
	if err != nil {
		if callCtx.Err() != nil {
			return sessionPrewarmClassifierFallback("classifier_timeout", "Classifier timed out.", "classifier_timeout")
		}
		return sessionPrewarmClassifierFallback("classifier_error", "Classifier failed.", "decided")
	}
	var parsed struct {
		Decision    models.PreviewSpeculativeDecision `json:"decision"`
		Confidence  float64                           `json:"confidence"`
		Reason      string                            `json:"reason"`
		Explanation string                            `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &parsed); err != nil {
		return sessionPrewarmClassifierFallback("classifier_error", "Classifier returned invalid JSON.", "decided")
	}
	if err := parsed.Decision.Validate(); err != nil {
		return sessionPrewarmClassifierFallback("classifier_error", "Classifier returned an invalid decision.", "decided")
	}
	if math.IsNaN(parsed.Confidence) || math.IsInf(parsed.Confidence, 0) {
		parsed.Confidence = 0
	}
	if parsed.Confidence < 0 {
		parsed.Confidence = 0
	} else if parsed.Confidence > 1 {
		parsed.Confidence = 1
	}
	reason := strings.TrimSpace(parsed.Reason)
	if reason == "" {
		reason = "classifier"
	}
	explanation := strings.TrimSpace(parsed.Explanation)
	if explanation == "" {
		explanation = "Classifier completed."
	}
	return SessionPrewarmClassifierResult{
		Decision:    parsed.Decision,
		Confidence:  parsed.Confidence,
		Reason:      truncateRunes(reason, 120),
		Explanation: truncateRunes(explanation, 300),
		Status:      "decided",
	}
}

func sessionPrewarmClassifierFallback(reason, explanation, status string) SessionPrewarmClassifierResult {
	return SessionPrewarmClassifierResult{
		Decision:    models.PreviewSpeculativeDecisionNone,
		Confidence:  0,
		Reason:      reason,
		Explanation: explanation,
		Status:      status,
	}
}

func sanitizeSessionPrewarmClassifierText(value string) string {
	value = sessionPrewarmCodeFencePattern.ReplaceAllString(value, " ")
	value = sessionPrewarmURLPattern.ReplaceAllString(value, " ")
	value = sessionPrewarmSpecials.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, maxSessionPrewarmTextChars)
}

func sanitizeSessionPrewarmList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := sanitizeSessionPrewarmClassifierText(value)
		if clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

func extractJSONObject(value string) string {
	value = strings.TrimSpace(value)
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return value
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
