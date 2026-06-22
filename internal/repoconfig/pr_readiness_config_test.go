package repoconfig

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestParse_PRReadinessPromptChecks(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(`{
		"pr_readiness": {
			"checks": [{
				"id": "no_analytics_schema_drift",
				"name": "Analytics schema compatibility",
				"type": "prompt",
				"enforcement": {"builder": "blocking", "engineer": "advisory"},
				"paths": {"include": ["analytics/**"]},
				"prompt": "Check whether analytics schemas are backwards compatible."
			}]
		}
	}`))
	require.NoError(t, err, "Parse should accept schema-valid repo readiness prompt checks")
	require.Len(t, cfg.PRReadiness.Checks, 1, "Parse should preserve repo readiness checks")
	require.Equal(t, "no_analytics_schema_drift", cfg.PRReadiness.Checks[0].ID, "Parse should preserve the check id")
	require.Equal(t, models.PRReadinessEnforcementBlocking, cfg.PRReadiness.Checks[0].Enforcement.Builder, "Parse should decode builder enforcement")
	require.Equal(t, []string{"analytics/**"}, cfg.PRReadiness.Checks[0].Paths.Include, "Parse should preserve include path filters")
}

func TestParse_RejectsInvalidPRReadinessPromptCheck(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{
		"pr_readiness": {
			"checks": [{
				"id": "bad check",
				"name": "",
				"type": "shell",
				"prompt": ""
			}]
		}
	}`))
	require.Error(t, err, "Parse should reject invalid repo readiness checks before they can be materialized")
	require.Contains(t, err.Error(), "pr_readiness.checks[0]", "Parse should identify the invalid readiness check")
}
