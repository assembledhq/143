package slackbot

import (
	"strings"
	"time"
)

type SlackProgressKind string

const (
	SlackProgressReadingContext   SlackProgressKind = "reading_context"
	SlackProgressResolvingContext SlackProgressKind = "resolving_context"
	SlackProgressRunningAgent     SlackProgressKind = "running_agent"
	SlackProgressRunningCommand   SlackProgressKind = "running_command"
	SlackProgressRunningTests     SlackProgressKind = "running_tests"
	SlackProgressStartingPreview  SlackProgressKind = "starting_preview"
	SlackProgressWaitingForInput  SlackProgressKind = "waiting_for_input"
	SlackProgressCompleted        SlackProgressKind = "completed"
	SlackProgressFailed           SlackProgressKind = "failed"
	SlackProgressGeneric          SlackProgressKind = "generic"
)

type ProgressInput struct {
	UpdateKind string
	Title      string
	Summary    string
	Terminal   bool
	Failed     bool
	OccurredAt time.Time
}

type SlackProgressUpdate struct {
	Kind       SlackProgressKind
	Title      string
	Summary    string
	Terminal   bool
	OccurredAt time.Time
}

type SlackProgressPrevious struct {
	Kind      SlackProgressKind
	UpdatedAt time.Time
}

type SlackProgressPolicy struct {
	MinUpdateInterval     time.Duration
	AlwaysSendTerminal    bool
	SuppressDuplicateKind bool
}

func DefaultSlackProgressPolicy() SlackProgressPolicy {
	return SlackProgressPolicy{
		MinUpdateInterval:     30 * time.Second,
		AlwaysSendTerminal:    true,
		SuppressDuplicateKind: true,
	}
}

func NormalizeProgressUpdate(input ProgressInput) SlackProgressUpdate {
	kind := classifyProgressKind(input.UpdateKind, input.Title, input.Summary)
	if input.Terminal {
		if input.Failed {
			kind = SlackProgressFailed
		} else {
			kind = SlackProgressCompleted
		}
	}
	return SlackProgressUpdate{
		Kind:       kind,
		Title:      strings.TrimSpace(input.Title),
		Summary:    strings.TrimSpace(input.Summary),
		Terminal:   input.Terminal,
		OccurredAt: input.OccurredAt,
	}
}

func ShouldSendProgressUpdate(update SlackProgressUpdate, previous SlackProgressPrevious, policy SlackProgressPolicy) bool {
	if update.Terminal && policy.AlwaysSendTerminal {
		return true
	}
	if policy.SuppressDuplicateKind && previous.Kind != "" && previous.Kind == update.Kind {
		return false
	}
	if !previous.UpdatedAt.IsZero() && policy.MinUpdateInterval > 0 {
		occurredAt := update.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = time.Now()
		}
		if occurredAt.Sub(previous.UpdatedAt) < policy.MinUpdateInterval {
			return false
		}
	}
	return true
}

func classifyProgressKind(updateKind, title, summary string) SlackProgressKind {
	combined := strings.ToLower(strings.TrimSpace(updateKind + " " + title + " " + summary))
	switch {
	case strings.Contains(combined, "context") && strings.Contains(combined, "read"):
		return SlackProgressReadingContext
	case strings.Contains(combined, "resolv"):
		return SlackProgressResolvingContext
	case strings.Contains(combined, "test"):
		return SlackProgressRunningTests
	case strings.Contains(combined, "preview"):
		return SlackProgressStartingPreview
	case strings.Contains(combined, "input") || strings.Contains(combined, "approval") || strings.Contains(combined, "waiting"):
		return SlackProgressWaitingForInput
	case strings.Contains(combined, "command") || strings.Contains(combined, "shell"):
		return SlackProgressRunningCommand
	case strings.Contains(combined, "agent") || strings.Contains(combined, "tool"):
		return SlackProgressRunningAgent
	default:
		return SlackProgressGeneric
	}
}
