package worker

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// emitThreadAttribution writes per-tab file events and accumulates the tab's
// cost meter at turn-complete. Called from the continue_session OnTurnComplete
// hook so the orchestrator stays unaware of attribution storage. Best-effort:
// any error here is logged and swallowed because the user-visible turn has
// already succeeded.
func emitThreadAttribution(
	ctx context.Context,
	stores *Stores,
	orgID, sessionID, threadID uuid.UUID,
	turn int,
	diff string,
	costUSD float64,
	logger zerolog.Logger,
) {
	if stores == nil {
		return
	}
	if stores.SessionThreads != nil && costUSD > 0 {
		// LLM cost APIs report dollars; the schema stores cents.
		if err := stores.SessionThreads.AddCost(ctx, orgID, threadID, costUSD*100); err != nil {
			logger.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to record thread cost")
		}
	}
	if stores.ThreadFileEvents == nil || strings.TrimSpace(diff) == "" {
		return
	}
	events := parseDiffFileEvents(diff)
	if len(events) == 0 {
		return
	}
	threadIDCopy := threadID
	for i := range events {
		events[i].OrgID = orgID
		events[i].SessionID = sessionID
		events[i].ThreadID = &threadIDCopy
		events[i].Turn = turn
	}
	if err := stores.ThreadFileEvents.AppendBatch(ctx, orgID, events); err != nil {
		logger.Warn().Err(err).Str("thread_id", threadID.String()).Int("event_count", len(events)).Msg("failed to record thread file events")
	}
}

// parseDiffFileEvents extracts a SessionThreadFileEvent per touched path from
// a unified diff. We intentionally rely on the agent-supplied diff (already
// captured in AgentResult.Diff) rather than re-shelling git status inside the
// sandbox; the diff is the canonical record of the turn's outputs and avoids
// races against concurrent siblings touching unrelated files.
//
// The parser is deliberately small: each `diff --git a/<path> b/<path>`
// header is one event, classified by the presence of `new file mode` /
// `deleted file mode` lines, and falling back to "modified" otherwise.
// Renames are recorded as a delete + create pair to keep per-path histories
// independent.
func parseDiffFileEvents(diff string) []models.SessionThreadFileEvent {
	if diff == "" {
		return nil
	}
	var events []models.SessionThreadFileEvent
	type pending struct {
		oldPath  string
		newPath  string
		isNew    bool
		isDelete bool
		isRename bool
	}
	flush := func(p *pending) {
		if p == nil || (p.oldPath == "" && p.newPath == "") {
			return
		}
		switch {
		case p.isRename:
			events = append(events, models.SessionThreadFileEvent{
				Path:      p.oldPath,
				EventType: models.FileEventTypeDeleted,
			})
			events = append(events, models.SessionThreadFileEvent{
				Path:      p.newPath,
				EventType: models.FileEventTypeCreated,
			})
		case p.isNew:
			events = append(events, models.SessionThreadFileEvent{
				Path:      p.newPath,
				EventType: models.FileEventTypeCreated,
			})
		case p.isDelete:
			events = append(events, models.SessionThreadFileEvent{
				Path:      p.oldPath,
				EventType: models.FileEventTypeDeleted,
			})
		default:
			path := p.newPath
			if path == "" {
				path = p.oldPath
			}
			events = append(events, models.SessionThreadFileEvent{
				Path:      path,
				EventType: models.FileEventTypeModified,
			})
		}
	}

	var current *pending
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush(current)
			current = &pending{}
			rest := strings.TrimPrefix(line, "diff --git ")
			oldPath, newPath := splitDiffHeaderPaths(rest)
			current.oldPath = oldPath
			current.newPath = newPath
			if oldPath != newPath && oldPath != "" && newPath != "" {
				current.isRename = true
			}
		case strings.HasPrefix(line, "new file mode"):
			if current != nil {
				current.isNew = true
				current.isRename = false
			}
		case strings.HasPrefix(line, "deleted file mode"):
			if current != nil {
				current.isDelete = true
				current.isRename = false
			}
		case strings.HasPrefix(line, "rename from "):
			if current != nil {
				current.oldPath = strings.TrimPrefix(line, "rename from ")
				current.isRename = true
			}
		case strings.HasPrefix(line, "rename to "):
			if current != nil {
				current.newPath = strings.TrimPrefix(line, "rename to ")
				current.isRename = true
			}
		}
	}
	flush(current)
	return events
}

// splitDiffHeaderPaths parses the `a/<path> b/<path>` portion of a
// "diff --git" header. Handles paths with spaces by splitting on " b/" once
// the leading "a/" is recognized — `git diff --no-prefix` is not used here
// since the agent emits standard git format.
func splitDiffHeaderPaths(rest string) (oldPath, newPath string) {
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "a/") {
		// Fallback: best-effort split on the last whitespace, trim a/ b/.
		parts := strings.Fields(rest)
		if len(parts) >= 2 {
			oldPath = strings.TrimPrefix(parts[len(parts)-2], "a/")
			newPath = strings.TrimPrefix(parts[len(parts)-1], "b/")
		}
		return
	}
	rest = strings.TrimPrefix(rest, "a/")
	idx := strings.Index(rest, " b/")
	if idx < 0 {
		return rest, rest
	}
	oldPath = rest[:idx]
	newPath = rest[idx+len(" b/"):]
	return
}
