package thread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type retainedCreationTestStore struct {
	*mockThreadStore
	read   func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error)
	create func(context.Context, uuid.UUID, *models.SessionThread, int) error
}

func (s *retainedCreationTestStore) GetByIDForCreation(ctx context.Context, orgID, id uuid.UUID) (models.SessionThread, error) {
	return s.read(ctx, orgID, id)
}

func (s *retainedCreationTestStore) CreateWithID(ctx context.Context, orgID uuid.UUID, row *models.SessionThread, max int) error {
	return s.create(ctx, orgID, row, max)
}

func TestCreateRetainedThreadAdoptsBeforeFreshPreflights(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*models.SessionThread)
		conflict bool
	}{
		{"running original", func(*models.SessionThread) {}, false},
		{"terminal original", func(r *models.SessionThread) { r.Status = models.ThreadStatusCompleted }, false},
		{"different org", func(r *models.SessionThread) { r.OrgID = uuid.New() }, true},
		{"different session", func(r *models.SessionThread) { r.SessionID = uuid.New() }, true},
		{"different id", func(r *models.SessionThread) { r.ID = uuid.New() }, true},
		{"different agent", func(r *models.SessionThread) { r.AgentType = models.AgentTypeClaudeCode }, true},
		{"different label", func(r *models.SessionThread) { r.Label = "changed" }, true},
		{"explicit model vs absent", func(r *models.SessionThread) { v := ""; r.ModelOverride = &v }, true},
		{"archived original", func(r *models.SessionThread) { v := time.Now(); r.ArchivedAt = &v }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, deps := newTestService(t)
			input := CreateThreadInput{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), AgentType: string(models.AgentTypeCodex), Label: "retained review"}
			existing := models.SessionThread{ID: input.ID, OrgID: input.OrgID, SessionID: input.SessionID, AgentType: models.AgentTypeCodex,
				Label: input.Label, Status: models.ThreadStatusRunning, CurrentTurn: 7, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				PendingMessageCount: 2, CostCents: 5, CreatedBySource: models.ThreadCreatedBySourceSystem}
			tt.change(&existing)
			deps.sessionStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				t.Fatal("same-ID adoption must precede session/default/capacity preflights")
				return models.Session{}, errors.New("unexpected preflight")
			}
			svc.threadStore = &retainedCreationTestStore{mockThreadStore: deps.threadStore,
				read: func(_ context.Context, orgID, id uuid.UUID) (models.SessionThread, error) {
					require.Equal(t, []uuid.UUID{input.OrgID, input.ID}, []uuid.UUID{orgID, id}, "retained lookup must use only the original tenant and thread identity")
					return existing, nil
				},
				create: func(context.Context, uuid.UUID, *models.SessionThread, int) error {
					t.Fatal("existing identities must never reach an insert")
					return errors.New("unexpected insert")
				},
			}
			actual, err := svc.CreateThread(context.Background(), input)
			if tt.conflict {
				require.ErrorIs(t, err, db.ErrThreadCreationIdentityConflict, "mismatched or archived retained identity must refuse without replacement")
				require.Nil(t, actual, "identity conflict must return no thread candidate")
			} else {
				require.NoError(t, err, "an exact retained original must remain adoptable after later execution or terminal progress")
				require.Equal(t, &existing, actual, "adoption must preserve the entire original row including runtime progress and creation provenance")
			}
		})
	}
}

func TestCreateRetainedThreadFreshPath(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		status  models.SessionStatus
		wantErr error
		creates int
	}{
		{"fresh exact insert", models.SessionStatusRunning, nil, 1},
		{"fresh terminal refusal", models.SessionStatusSkipped, ErrSessionTerminal, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, deps := newTestService(t)
			input := CreateThreadInput{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), AgentType: string(models.AgentTypeCodex), Label: "original", CreatedBySource: models.ThreadCreatedBySourceSystem}
			createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			reads, creates := 0, 0
			deps.sessionStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				reads++
				return models.Session{ID: input.SessionID, OrgID: input.OrgID, Status: tt.status, AgentType: models.AgentTypeClaudeCode}, nil
			}
			expected := models.SessionThread{ID: input.ID, OrgID: input.OrgID, SessionID: input.SessionID, AgentType: models.AgentTypeCodex,
				Label: input.Label, Status: models.ThreadStatusIdle, CreatedBySource: models.ThreadCreatedBySourceSystem,
				ExecutionMode: models.ThreadExecutionModeWork, FilesystemMode: models.ThreadFilesystemModeReadWrite}
			svc.threadStore = &retainedCreationTestStore{mockThreadStore: deps.threadStore,
				read: func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, pgx.ErrNoRows
				},
				create: func(_ context.Context, orgID uuid.UUID, row *models.SessionThread, max int) error {
					creates++
					require.Equal(t, input.OrgID, orgID, "fresh retained insertion must stay in the original tenant")
					require.Equal(t, models.MaxThreadsPerSession, max, "retained creation must keep the ordinary thread capacity limit")
					require.Equal(t, expected, *row, "fresh insertion must preserve the preminted identity, frozen agent and provenance")
					row.CreatedAt = createdAt
					return nil
				},
			}
			actual, err := svc.CreateThread(context.Background(), input)
			require.Equal(t, []int{1, tt.creates}, []int{reads, creates}, "only a missing original may execute fresh preflights and exactly one retained insertion")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "retained fresh creation must retain ordinary session eligibility")
				require.Nil(t, actual, "an ineligible fresh session must produce no provisional thread")
			} else {
				expected.CreatedAt = createdAt
				require.NoError(t, err, "a fresh eligible retained identity should create one blank lane")
				require.Equal(t, &expected, actual, "creation returns the original identity and persistence timestamp without a message or job")
			}
		})
	}
}
