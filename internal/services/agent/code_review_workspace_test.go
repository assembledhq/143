package agent

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewWorkspaceHolderSpy struct {
	SessionSandboxHolderStore
	events        *[]string
	released      bool
	releaseParams []db.AcquireCodeReviewSandboxHolderParams
	acquireParams []db.AcquireCodeReviewSandboxHolderParams
}

func (s *reviewWorkspaceHolderSpy) ReleaseAfterSuccessfulSynthesis(_ context.Context, _ uuid.UUID, params db.AcquireCodeReviewSandboxHolderParams) (bool, error) {
	*s.events = append(*s.events, "synthesis_release")
	s.releaseParams = append(s.releaseParams, params)
	if params.ContainerID != "review-container" || params.OwnerNodeID != "review-node" || params.ThreadID == uuid.Nil {
		return false, nil
	}
	return s.released, nil
}

func (s *reviewWorkspaceHolderSpy) AcquireCodeReview(_ context.Context, _ uuid.UUID, params db.AcquireCodeReviewSandboxHolderParams) (models.SessionSandboxHolder, bool, error) {
	*s.events = append(*s.events, "reviewer_acquire")
	s.acquireParams = append(s.acquireParams, params)
	if params.LeaseDuration != time.Minute || params.LeaseToken == uuid.Nil {
		return models.SessionSandboxHolder{}, false, nil
	}
	return models.SessionSandboxHolder{}, true, nil
}

type reviewTurnSessionSpy struct {
	SessionStore
	events *[]string
}

func (s *reviewTurnSessionSpy) ReleaseTurnHold(context.Context, uuid.UUID, uuid.UUID) (bool, string, error) {
	*s.events = append(*s.events, "turn_release")
	return false, "review-container", nil
}

func TestReleaseTurnHoldAfterReviewMaintainsWorkspaceBeforeTurnRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		origin            models.SessionOrigin
		source            models.SessionMessageSource
		successful        bool
		matchingThread    bool
		noRequestedThread bool
		synthesis         bool
		expectedEvents    []string
	}{
		{name: "reviewer handoff", origin: models.SessionOriginCodeReview, source: models.SessionMessageSourceCodeReview, successful: true, matchingThread: true, expectedEvents: []string{"synthesis_release", "reviewer_acquire", "turn_release"}},
		{name: "successful synthesis", origin: models.SessionOriginCodeReview, source: models.SessionMessageSourceCodeReview, successful: true, matchingThread: true, synthesis: true, expectedEvents: []string{"synthesis_release", "turn_release"}},
		{name: "recovered reviewer turn", origin: models.SessionOriginCodeReview, source: models.SessionMessageSourceCodeReview, successful: true, noRequestedThread: true, expectedEvents: []string{"synthesis_release", "reviewer_acquire", "turn_release"}},
		{name: "failed review turn", origin: models.SessionOriginCodeReview, source: models.SessionMessageSourceCodeReview, matchingThread: true, expectedEvents: []string{"turn_release"}},
		{name: "thread mismatch", origin: models.SessionOriginCodeReview, source: models.SessionMessageSourceCodeReview, successful: true, expectedEvents: []string{"turn_release"}},
		{name: "non-review message", origin: models.SessionOriginCodeReview, source: "", successful: true, matchingThread: true, expectedEvents: []string{"turn_release"}},
		{name: "ordinary session", origin: models.SessionOriginManual, source: models.SessionMessageSourceCodeReview, successful: true, matchingThread: true, expectedEvents: []string{"turn_release"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events := make([]string, 0, 3)
			threadID := uuid.New()
			requestedThreadID := threadID
			if !tt.matchingThread {
				requestedThreadID = uuid.New()
			}
			var requestedThread *uuid.UUID = &requestedThreadID
			if tt.noRequestedThread {
				requestedThread = nil
			}
			holderSpy := &reviewWorkspaceHolderSpy{events: &events, released: tt.synthesis}
			orchestrator := &Orchestrator{
				sessions:       &reviewTurnSessionSpy{events: &events},
				sandboxHolders: holderSpy,
				nodeID:         "review-node",
			}
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: tt.origin}
			pending := []models.SessionMessage{{Source: tt.source}}
			_, containerID, err := orchestrator.releaseTurnHoldAfterReview(context.Background(), context.Background(), session,
				&threadID, requestedThread, &Sandbox{ID: "review-container"}, pending, tt.successful, zerolog.Nop())
			require.NoError(t, err, "review turn hold should release without an error")
			require.Equal(t, "review-container", containerID, "turn release should report the review container")
			require.Equal(t, tt.expectedEvents, events, "review retention must be updated before releasing the active turn hold")
			for _, params := range append(holderSpy.releaseParams, holderSpy.acquireParams...) {
				require.Equal(t, session.ID, params.SessionID, "workspace holder operations should target the current review session")
				require.Equal(t, threadID, params.ThreadID, "workspace holder operations should target the selected thread")
				require.Equal(t, "review-container", params.ContainerID, "workspace holder operations should target the current container")
				require.Equal(t, "review-node", params.OwnerNodeID, "workspace holder operations should target the current owner")
			}
		})
	}
}
