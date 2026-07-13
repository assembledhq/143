package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeVerificationRuns struct {
	created   models.PreviewVerificationRun
	completed models.PreviewVerificationRun
}

func (f *fakeVerificationRuns) Create(_ context.Context, orgID uuid.UUID, run models.PreviewVerificationRun) (models.PreviewVerificationRun, error) {
	// The plan/steps/artifacts columns enforce a jsonb-array CHECK constraint, so a
	// nil slice marshaled to JSON null would fail the real INSERT. Mirror that here.
	for name, raw := range map[string]json.RawMessage{"plan": run.Plan, "steps": run.Steps, "artifacts": run.Artifacts} {
		if len(raw) == 0 {
			continue
		}
		// Mirror the jsonb_typeof(...) = 'array' CHECK constraint: JSON null (from a
		// marshaled nil slice) unmarshals fine into a slice but violates the DB.
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return models.PreviewVerificationRun{}, fmt.Errorf("%s is invalid json: %s", name, raw)
		}
		if _, ok := decoded.([]any); !ok {
			return models.PreviewVerificationRun{}, fmt.Errorf("%s must be a json array, got %s", name, raw)
		}
	}
	run.ID, run.OrgID = uuid.New(), orgID
	f.created = run
	return run, nil
}

func (f *fakeVerificationRuns) Complete(_ context.Context, _ uuid.UUID, runID uuid.UUID, status models.PreviewVerificationStatus, attempt int, steps, artifacts json.RawMessage, consoleErrors int, summary, failureReason string) (models.PreviewVerificationRun, error) {
	f.completed = models.PreviewVerificationRun{ID: runID, Status: status, Attempt: attempt, Steps: steps, Artifacts: artifacts, ConsoleErrorCount: consoleErrors, Summary: summary, FailureReason: failureReason}
	return f.completed, nil
}

type fakeVerificationObserver struct {
	observations []*models.PreviewObservation
	calls        int
}

func (f *fakeVerificationObserver) Observe(_ context.Context, _ string, _ models.ViewportSpec) (*models.PreviewObservation, error) {
	observation := f.observations[f.calls]
	f.calls++
	return observation, nil
}

type fakeVerificationFixer struct{ calls int }

func (f *fakeVerificationFixer) FixVerificationFailure(_ context.Context, _ int, _ string) error {
	f.calls++
	return nil
}

func TestVerificationCoordinatorRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		auto            bool
		observations    []*models.PreviewObservation
		maxAttempts     int
		withoutFixer    bool
		withoutObserver bool
		expectedStatus  models.PreviewVerificationStatus
		expectedFixes   int
		expectedAttempt int
	}{
		{name: "passes and records screenshot", auto: true, observations: []*models.PreviewObservation{{Ready: true, Screenshot: &models.ScreenshotResult{Artifact: &models.PreviewArtifact{ID: "shot-1"}}}}, maxAttempts: 1, expectedStatus: models.PreviewVerificationStatusPassed, expectedAttempt: 1},
		{name: "fixes console failure and retries", auto: true, observations: []*models.PreviewObservation{{Ready: true, Console: []models.ConsoleMessage{{Level: "error"}}}, {Ready: true}}, maxAttempts: 2, expectedStatus: models.PreviewVerificationStatusPassed, expectedFixes: 1, expectedAttempt: 2},
		{name: "records disabled skip", auto: false, maxAttempts: 1, expectedStatus: models.PreviewVerificationStatusSkipped},
		{name: "reports the real attempt when no fixer is wired", auto: true, observations: []*models.PreviewObservation{{Ready: false}}, maxAttempts: 3, withoutFixer: true, expectedStatus: models.PreviewVerificationStatusFailed, expectedAttempt: 1},
		{name: "fails when the observer is unavailable", auto: true, maxAttempts: 3, withoutObserver: true, expectedStatus: models.PreviewVerificationStatusFailed, expectedAttempt: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runs := &fakeVerificationRuns{}
			observer := &fakeVerificationObserver{observations: tt.observations}
			fixer := &fakeVerificationFixer{}
			request := VerificationRequest{
				OrgID: uuid.New(), SessionID: uuid.New(), WorkspaceRevision: 4,
				Diff: "+++ b/frontend/src/page.tsx", Observer: observer, Fixer: fixer,
				Config: models.PreviewVerificationConfig{Auto: tt.auto, MaxAttempts: tt.maxAttempts, FailOnConsoleError: true, SmokePaths: []string{"/"}, Viewports: []models.ViewportSpec{{Width: 1440, Height: 900}}},
			}
			if tt.withoutFixer {
				request.Fixer = nil
			}
			if tt.withoutObserver {
				request.Observer = nil
			}
			coordinator := NewVerificationCoordinator(runs)
			actual, err := coordinator.Run(context.Background(), request)
			require.NoError(t, err, "coordinator should persist the verification outcome")
			require.Equal(t, tt.expectedStatus, actual.Status, "coordinator should return the expected terminal status")
			require.Equal(t, tt.expectedFixes, fixer.calls, "coordinator should use the bounded fix callback as expected")
			if tt.expectedAttempt > 0 && tt.expectedStatus != models.PreviewVerificationStatusSkipped {
				require.Equal(t, tt.expectedAttempt, actual.Attempt, "coordinator should record the attempt where the run resolved")
			}
		})
	}
}
