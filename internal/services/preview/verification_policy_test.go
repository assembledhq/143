package preview

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestPlanVerification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		diff        string
		config      models.PreviewVerificationConfig
		expectedDue bool
		expectedLen int
	}{
		{name: "plans user-facing diff across routes and viewports", diff: "+++ b/frontend/src/page.tsx", config: models.PreviewVerificationConfig{Auto: true, SmokePaths: []string{"/settings", "/"}, Viewports: []models.ViewportSpec{{Width: 390, Height: 844}, {Width: 1440, Height: 900}}}, expectedDue: true, expectedLen: 4},
		{name: "skips backend diff", diff: "+++ b/internal/db/issues.go", config: models.PreviewVerificationConfig{Auto: true}, expectedDue: false},
		{name: "skips tests", diff: "+++ b/frontend/src/page.test.tsx", config: models.PreviewVerificationConfig{Auto: true}, expectedDue: false},
		{name: "honors disabled policy", diff: "+++ b/frontend/src/page.tsx", config: models.PreviewVerificationConfig{Auto: false}, expectedDue: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := PlanVerification(tt.diff, tt.config)
			require.Equal(t, tt.expectedDue, actual.Due, "decision should match automatic verification policy")
			require.Len(t, actual.Plan, tt.expectedLen, "plan should contain the expected route and viewport combinations")
		})
	}
}
