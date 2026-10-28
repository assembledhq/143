package thread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRetainedCreationPreflightReusesEligibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		capacity                   int
		archived                   bool
		status                     models.SessionStatus
		codeOwned, automationOwned bool
		want                       error
	}{
		{name: "available", capacity: models.MaxThreadsPerSession - 1, status: models.SessionStatusIdle},
		{name: "visible capacity", capacity: models.MaxThreadsPerSession, status: models.SessionStatusIdle, want: db.ErrThreadLimitReached},
		{name: "archived capacity excluded", capacity: models.MaxThreadsPerSession, archived: true, status: models.SessionStatusIdle},
		{name: "terminal", status: models.SessionStatusSkipped, want: ErrSessionTerminal},
		{name: "code review owned", codeOwned: true, status: models.SessionStatusIdle},
		{name: "automation owned", automationOwned: true, status: models.SessionStatusIdle, want: models.ErrSessionAutomationOwned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, deps := newTestService(t)
			org, session := uuid.New(), uuid.New()
			deps.sessionStore.getByIDFn = func(_ context.Context, gotOrg, gotSession uuid.UUID) (models.Session, error) {
				require.Equal(t, []uuid.UUID{org, session}, []uuid.UUID{gotOrg, gotSession}, "preflight reads only its tenant and session")
				return models.Session{ID: session, OrgID: org, AgentType: models.AgentTypeClaudeCode, Status: tt.status}, nil
			}
			if tt.codeOwned {
				id := uuid.New()
				deps.sessionStore.codeReviewOwner = &id
			}
			if tt.automationOwned {
				deps.sessionStore.automationOwner = &models.SessionAutomationOwner{GenerationID: uuid.New()}
			}
			deps.threadStore.listBySessionFn = func(_ context.Context, gotOrg, gotSession uuid.UUID) ([]models.SessionThread, error) {
				require.Equal(t, []uuid.UUID{org, session}, []uuid.UUID{gotOrg, gotSession}, "capacity uses the same scoped visible-thread surface as ordinary creation")
				rows := make([]models.SessionThread, tt.capacity)
				if tt.archived {
					now := time.Now()
					rows[0].ArchivedAt = &now
				}
				return rows, nil
			}
			deps.threadStore.createFn = func(context.Context, *models.SessionThread, int) error {
				t.Fatal("preflight must never create a row")
				return errors.New("unexpected creation")
			}
			err := svc.PreflightCreateThread(context.Background(), CreateThreadInput{OrgID: org, SessionID: session, AgentType: string(models.AgentTypeClaudeCode)})
			if tt.codeOwned {
				var owned *models.SessionCodeReviewOwnedError
				require.ErrorAs(t, err, &owned, "preflight preserves the existing typed code-review ownership refusal")
			} else if tt.want != nil {
				require.ErrorIs(t, err, tt.want, "preflight preserves ordinary deterministic creation refusal")
			} else {
				require.NoError(t, err, "an ordinarily addable session can prepare without reserving capacity")
			}
		})
	}
}
