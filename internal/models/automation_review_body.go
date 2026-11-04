package models

import (
	"strings"

	"github.com/google/uuid"
)

func AutomationReviewMarkedBody(text string, actionID uuid.UUID) string {
	return text + "\n\n" + AutomationActionReviewMarkerPrefix + actionID.String() + " -->"
}

func AutomationReviewSummary(text, author string, actionID uuid.UUID) string {
	summary := strings.TrimSpace(text)
	if summary == "" {
		summary = "Automated review findings."
	}
	return AutomationReviewMarkedBody("@"+author+" "+summary, actionID)
}

type AutomationActionReviewFeedback struct {
	ReviewID int64
	PRAuthor string
	Body     string
	Inline   bool
	Path     string
	Line     *int
}

func (f AutomationActionReviewFeedback) ActionID() uuid.UUID {
	body := normalizeAutomationReviewBody(f.Body)
	_, marker, ok := strings.Cut(body, "\n\n"+AutomationActionReviewMarkerPrefix)
	if !ok || !strings.HasSuffix(marker, " -->") {
		return uuid.Nil
	}
	rawID := strings.TrimSuffix(marker, " -->")
	id, err := uuid.Parse(rawID)
	if err != nil || id.String() != rawID {
		return uuid.Nil
	}
	return id
}

func (f AutomationActionReviewFeedback) Matches(request AutomationActionRequest, actionID uuid.UUID) bool {
	body := normalizeAutomationReviewBody(f.Body)
	if !f.Inline {
		return f.PRAuthor != "" && body == normalizeAutomationReviewBody(AutomationReviewSummary(request.Text, f.PRAuthor, actionID))
	}
	for _, comment := range request.Comments {
		if f.Path == comment.Path && (f.Line == nil || *f.Line == comment.Line) &&
			body == normalizeAutomationReviewBody(AutomationReviewMarkedBody(comment.Body, actionID)) {
			return true
		}
	}
	return false
}

func normalizeAutomationReviewBody(body string) string {
	return strings.ReplaceAll(body, "\r\n", "\n")
}
