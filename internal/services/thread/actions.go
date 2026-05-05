package thread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// CancelThread asks the orchestrator to SIGINT the in-flight agent run and
// stamps the thread with cancel_requested_at so a worker that picks up the
// thread later (e.g. after a worker bounce) can also short-circuit.
//
// Cancel is best-effort: returning nil means the request was accepted, not
// that the agent has exited. The orchestrator transitions the thread to
// `cancelled` once the process unwinds.
func (s *Service) CancelThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if thread.SessionID != sessionID {
		return models.SessionThread{}, ErrThreadNotFound
	}
	switch thread.Status {
	case models.ThreadStatusPending, models.ThreadStatusRunning, models.ThreadStatusAwaitingInput:
		// fall through
	default:
		return models.SessionThread{}, ErrThreadNotCancellable
	}

	if err := s.threadStore.MarkCancelRequested(ctx, orgID, threadID); err != nil {
		return models.SessionThread{}, fmt.Errorf("mark cancel requested: %w", err)
	}

	if s.canceller != nil {
		_ = s.canceller.CancelThread(threadID)
	}

	// Re-fetch so callers see the cancel_requested_at timestamp.
	updated, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("reload thread: %w", err)
	}
	return updated, nil
}

// ListFileEvents returns the raw file-event timeline for a session. Used by
// the Changes view to power the "Touched by tab" / "Overlap" filters. We
// expose the timeline (not a pre-rolled view) so the frontend can switch
// between filter shapes without round-tripping for each.
//
// since, when non-nil, scopes the result to events observed at-or-after
// that time. Frontend polling passes the most recent observed_at it has
// seen so a long-lived session does not re-fetch the entire history every
// 5 seconds. Server-side filter is preferred over client-side trimming so
// the network/DB cost stays bounded.
func (s *Service) ListFileEvents(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error) {
	if s.fileEvents == nil {
		return []models.SessionThreadFileEvent{}, nil
	}
	if _, err := s.sessionStore.GetByID(ctx, orgID, sessionID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}
	return s.fileEvents.ListBySession(ctx, orgID, sessionID, since)
}

// ForkInput captures the parameters for forking a tab into its own session.
// Defaults are intentionally minimal: copy the source session's repo,
// branch, and prompt; let the caller override the new label/agent/model.
type ForkInput struct {
	SourceSessionID uuid.UUID
	SourceThreadID  uuid.UUID
	OrgID           uuid.UUID
	UserID          *uuid.UUID
	Label           string
}

// ForkResult is what ForkThread returns. We avoid creating the new session
// directly here because session creation goes through a richer pipeline
// (validation policy, snapshot copy, sandbox warm-up). Instead, we enqueue a
// `fork_session_thread` job that owns the heavy lifting and surface a
// pointer the UI can poll. This keeps the API call snappy.
type ForkResult struct {
	JobID uuid.UUID `json:"job_id"`
}

// ForkThread enqueues a job that copies a tab into a brand-new session with
// its own sandbox. The new session inherits the source session's repo,
// branch, and base snapshot so the forked tab boots from the same state the
// reviewer was looking at. Use this when a tab's work has diverged enough to
// deserve a separate PR.
func (s *Service) ForkThread(ctx context.Context, input ForkInput) (ForkResult, error) {
	thread, err := s.threadStore.GetByID(ctx, input.OrgID, input.SourceThreadID)
	if err != nil {
		return ForkResult{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if thread.SessionID != input.SourceSessionID {
		return ForkResult{}, ErrThreadNotFound
	}
	if _, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SourceSessionID); err != nil {
		return ForkResult{}, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	payload := map[string]any{
		"source_session_id": input.SourceSessionID.String(),
		"source_thread_id":  input.SourceThreadID.String(),
		"org_id":            input.OrgID.String(),
		"label":             strings.TrimSpace(input.Label),
	}
	if input.UserID != nil {
		payload["user_id"] = input.UserID.String()
	}
	jobID, err := s.jobStore.Enqueue(ctx, input.OrgID, "agent", "fork_session_thread", payload, 5, nil)
	if err != nil {
		return ForkResult{}, fmt.Errorf("%w: %w", ErrEnqueueFailed, err)
	}
	return ForkResult{JobID: jobID}, nil
}

// RevertThread enqueues a job that applies the thread's diff in reverse
// against the shared sandbox. It only succeeds when the patch applies
// cleanly; otherwise the orchestrator surfaces a "guided revert" message
// asking the user to ask another tab to revert by hand. We do not attempt
// this synchronously because the patch operation runs inside the sandbox
// and may need a fresh container exec.
func (s *Service) RevertThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, userID *uuid.UUID) (ForkResult, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return ForkResult{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if thread.SessionID != sessionID {
		return ForkResult{}, ErrThreadNotFound
	}
	if thread.Diff == nil || strings.TrimSpace(*thread.Diff) == "" {
		return ForkResult{}, errors.New("thread has no diff to revert")
	}
	payload := map[string]any{
		"session_id": sessionID.String(),
		"thread_id":  threadID.String(),
		"org_id":     orgID.String(),
	}
	if userID != nil {
		payload["user_id"] = userID.String()
	}
	jobID, err := s.jobStore.Enqueue(ctx, orgID, "agent", "revert_session_thread", payload, 5, nil)
	if err != nil {
		return ForkResult{}, fmt.Errorf("%w: %w", ErrEnqueueFailed, err)
	}
	return ForkResult{JobID: jobID}, nil
}
