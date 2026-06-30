package readiness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEvaluator_CoreGapChecks(t *testing.T) {
	t.Parallel()

	snapshotKey := "snap-current"
	revisionUpdatedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	session := models.Session{
		ID:                         uuid.New(),
		OrgID:                      uuid.New(),
		WorkspaceRevision:          12,
		WorkspaceRevisionUpdatedAt: revisionUpdatedAt,
		SnapshotKey:                &snapshotKey,
		DiffStats:                  json.RawMessage(`{"files_changed":42,"additions":450,"deletions":100}`),
	}
	loop := &models.SessionReviewLoop{
		Status:              models.ReviewLoopStatusClean,
		LatestCheckpointKey: &snapshotKey,
	}
	policy := models.DefaultPRReadinessPolicyConfig()
	policy.LargeDiffFileThreshold = 25
	policy.LargeDiffLineThreshold = 500
	policy.SensitivePaths = []string{"analytics/**"}

	result, err := NewEvaluator(policy.EffectivePolicy()).Evaluate(context.Background(), EvaluationInput{
		Session:                    session,
		EvaluatedWorkspaceRevision: session.WorkspaceRevision,
		EvaluatedSnapshotKey:       snapshotKey,
		LatestReviewLoop:           loop,
		Logs: []models.SessionLog{
			{ID: 1, Timestamp: revisionUpdatedAt.Add(-time.Minute), Message: "go test ./... ok"},
			{ID: 2, Timestamp: revisionUpdatedAt.Add(time.Minute), Message: "go test ./... passed exit code 0"},
		},
		ChangedFiles: []string{
			"migrations/000210_pr_readiness_core_gaps.up.sql",
			"package-lock.json",
			"analytics/schema.json",
			"internal/generated/client.pb.go",
		},
		LinkedIssueCount: 0,
		IssueLessReason:  "maintenance follow-up requested in Slack",
		PolicyConfig:     policy,
	})
	require.NoError(t, err, "Evaluate should produce readiness checks for the current revision")

	checks := checksByType(t, result.Checks)
	require.Equal(t, models.PRReadinessCheckStatusPassed, checks[models.PRReadinessCheckTypeTestEvidencePresent].Status, "test evidence should pass only when a successful command is captured after the workspace revision timestamp")
	require.JSONEq(t, `{"log_id":2}`, string(checks[models.PRReadinessCheckTypeTestEvidencePresent].Details), "test evidence should identify the fresh successful command")
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeDependencyConfigRisk].Status, "dependency and runtime config changes should be called out")
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeGeneratedFileChurn].Status, "generated file churn should be called out")
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeRiskFlags].Status, "large diffs, migrations, and sensitive configured paths should be risk flags")
	require.JSONEq(t, `{"flags":["large_diff","migration","sensitive_path","dependency_or_config"],"files":["migrations/000210_pr_readiness_core_gaps.up.sql","package-lock.json","analytics/schema.json","internal/generated/client.pb.go"]}`, string(checks[models.PRReadinessCheckTypeRiskFlags].Details), "risk flag details should include expanded built-in and configured signals")
	require.Equal(t, models.PRReadinessCheckStatusPassed, checks[models.PRReadinessCheckTypeContextComplete].Status, "issue-less sessions with an explicit reason should satisfy context completeness")

	var packet map[string]any
	require.NoError(t, json.Unmarshal(result.ReviewPacket, &packet), "review packet should be valid JSON")
	require.Contains(t, packet, "what_changed", "review packet should summarize changed files")
	require.Contains(t, packet, "why_changed", "review packet should include issue or issue-less context")
	require.Contains(t, packet, "risk_flags", "review packet should surface risk flags")
	require.Contains(t, packet, "unknowns", "review packet should disclose unknowns for reviewers")
}

func TestEvaluator_IgnoresStaleTestEvidence(t *testing.T) {
	t.Parallel()

	snapshotKey := "snap-current"
	revisionUpdatedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	session := models.Session{
		ID:                         uuid.New(),
		OrgID:                      uuid.New(),
		WorkspaceRevision:          12,
		WorkspaceRevisionUpdatedAt: revisionUpdatedAt,
		SnapshotKey:                &snapshotKey,
		DiffStats:                  json.RawMessage(`{"files_changed":1,"additions":1,"deletions":0}`),
	}

	result, err := NewEvaluator(models.DefaultPRReadinessPolicy()).Evaluate(context.Background(), EvaluationInput{
		Session:                    session,
		EvaluatedWorkspaceRevision: session.WorkspaceRevision,
		EvaluatedSnapshotKey:       snapshotKey,
		Logs: []models.SessionLog{
			{ID: 1, Timestamp: revisionUpdatedAt.Add(-time.Minute), Message: "go test ./... passed exit code 0"},
			{ID: 2, Timestamp: revisionUpdatedAt.Add(time.Minute), Message: "go test ./... failed exit code 1"},
		},
		ChangedFiles:     []string{"internal/api/foo.go"},
		LinkedIssueCount: 1,
	})
	require.NoError(t, err, "Evaluate should complete even when no fresh successful test evidence exists")

	checks := checksByType(t, result.Checks)
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeTestEvidencePresent].Status, "stale success and fresh failure output should not satisfy test evidence")
}

func TestEvaluator_LargeDiffUsesPersistedAddedRemovedStats(t *testing.T) {
	t.Parallel()

	snapshotKey := "snap-current"
	session := models.Session{
		ID:                uuid.New(),
		OrgID:             uuid.New(),
		WorkspaceRevision: 12,
		SnapshotKey:       &snapshotKey,
		DiffStats:         json.RawMessage(`{"files_changed":1,"added":400,"removed":200}`),
	}
	cfg := models.DefaultPRReadinessPolicyConfig()
	cfg.LargeDiffFileThreshold = 25
	cfg.LargeDiffLineThreshold = 500

	result, err := NewEvaluator(cfg.EffectivePolicy()).Evaluate(context.Background(), EvaluationInput{
		Session:                    session,
		EvaluatedWorkspaceRevision: session.WorkspaceRevision,
		EvaluatedSnapshotKey:       snapshotKey,
		ChangedFiles:               []string{"internal/api/session.go"},
		LinkedIssueCount:           1,
		PolicyConfig:               cfg,
	})
	require.NoError(t, err, "Evaluate should complete with persisted diff stats")

	checks := checksByType(t, result.Checks)
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeRiskFlags].Status, "added and removed diff stats should count toward the large diff line threshold")
	require.JSONEq(t, `{"flags":["large_diff"],"files":["internal/api/session.go"]}`, string(checks[models.PRReadinessCheckTypeRiskFlags].Details), "risk flag details should include large diff from persisted stats")
}

func TestEvaluator_GeneratedFileChurnHonorsAllowedPaths(t *testing.T) {
	t.Parallel()

	snapshotKey := "snap-current"
	session := models.Session{
		ID:                uuid.New(),
		OrgID:             uuid.New(),
		WorkspaceRevision: 12,
		SnapshotKey:       &snapshotKey,
		DiffStats:         json.RawMessage(`{"files_changed":2,"additions":4,"deletions":1}`),
	}
	cfg := models.DefaultPRReadinessPolicyConfig()
	cfg.GeneratedFileAllowedPaths = []string{"frontend/build/**"}

	result, err := NewEvaluator(cfg.EffectivePolicy()).Evaluate(context.Background(), EvaluationInput{
		Session:                    session,
		EvaluatedWorkspaceRevision: session.WorkspaceRevision,
		EvaluatedSnapshotKey:       snapshotKey,
		ChangedFiles: []string{
			"frontend/build/asset-manifest.json",
			"internal/generated/client.pb.go",
		},
		LinkedIssueCount: 1,
		PolicyConfig:     cfg,
	})
	require.NoError(t, err, "Evaluate should complete with generated allowed paths configured")

	checks := checksByType(t, result.Checks)
	require.Equal(t, models.PRReadinessCheckStatusWarning, checks[models.PRReadinessCheckTypeGeneratedFileChurn].Status, "generated churn should warn only for generated files outside allowed paths")
	require.JSONEq(t, `{"files":["internal/generated/client.pb.go"]}`, string(checks[models.PRReadinessCheckTypeGeneratedFileChurn].Details), "generated churn details should exclude configured allowed paths")
}

func TestEvaluator_SkipsChecksThatAreOffForAllRoles(t *testing.T) {
	t.Parallel()

	snapshotKey := "snap-current"
	session := models.Session{
		ID:                uuid.New(),
		OrgID:             uuid.New(),
		WorkspaceRevision: 12,
		SnapshotKey:       &snapshotKey,
		DiffStats:         json.RawMessage(`{"files_changed":1,"additions":1,"deletions":0}`),
	}
	cfg := models.DefaultPRReadinessPolicyConfig()
	check := cfg.Checks[models.PRReadinessCheckTypeGeneratedFileChurn]
	check.Enforcement = models.PRReadinessEnforcementByRole{
		Builder:  models.PRReadinessEnforcementOff,
		Engineer: models.PRReadinessEnforcementOff,
		Admin:    models.PRReadinessEnforcementOff,
	}
	cfg.Checks[models.PRReadinessCheckTypeGeneratedFileChurn] = check

	result, err := NewEvaluator(cfg.EffectivePolicy()).Evaluate(context.Background(), EvaluationInput{
		Session:                    session,
		EvaluatedWorkspaceRevision: session.WorkspaceRevision,
		EvaluatedSnapshotKey:       snapshotKey,
		ChangedFiles:               []string{"internal/generated/client.pb.go"},
		LinkedIssueCount:           1,
		PolicyConfig:               cfg,
	})
	require.NoError(t, err, "Evaluate should complete with an all-role-off check")

	for _, check := range result.Checks {
		require.NotEqual(t, models.PRReadinessCheckTypeGeneratedFileChurn, check.CheckType, "checks configured off for every role should not be evaluated")
	}
}

func checksByType(t *testing.T, checks []models.PRReadinessCheck) map[models.PRReadinessCheckType]models.PRReadinessCheck {
	t.Helper()

	byType := make(map[models.PRReadinessCheckType]models.PRReadinessCheck, len(checks))
	for _, check := range checks {
		byType[check.CheckType] = check
	}
	require.Contains(t, byType, models.PRReadinessCheckTypeDependencyConfigRisk, "dependency config check should be present")
	require.Contains(t, byType, models.PRReadinessCheckTypeGeneratedFileChurn, "generated churn check should be present")
	return byType
}
