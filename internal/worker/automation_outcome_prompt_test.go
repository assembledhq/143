package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAutomationRunPromptSeedOutcomeContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		run          models.AutomationRun
		wantContract bool
	}{
		{
			name: "github PR run includes reporting contract",
			run: models.AutomationRun{
				TriggeredAt:    time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
				TriggeredBy:    models.AutomationTriggeredByGitHub,
				GoalSnapshot:   "Evaluate the pull request.",
				ConfigSnapshot: json.RawMessage(`{"github":{"repository":"assembledhq/143","pull_request_number":123,"pull_request_url":"https://github.com/assembledhq/143/pull/123"}}`),
			},
			wantContract: true,
		},
		{
			name: "manual run keeps lifecycle context only",
			run: models.AutomationRun{
				TriggeredAt:  time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
				TriggeredBy:  models.AutomationTriggeredByManual,
				GoalSnapshot: "Inspect the repository.",
			},
			wantContract: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prompt, err := automationRunPromptSeed(tt.run)
			require.NoError(t, err, "valid automation snapshots should render a prompt")
			hasContract := strings.Contains(prompt, "143-tools automation-run report")
			require.Equal(t, tt.wantContract, hasContract, "reporting contract should only be added to GitHub PR runs")
			if tt.wantContract {
				require.Contains(t, prompt, "Do not use `REVIEW_CLEAN`", "contract should exclude internal review-loop results")
				require.Contains(t, prompt, "pull request #123", "contract should name the target PR")
			}
		})
	}
}
