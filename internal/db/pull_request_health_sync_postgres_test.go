package db

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newHealthSyncPostgres(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool, orgID, _, prID := newSchedulingPostgres(t)
	_, err := pool.Exec(context.Background(), `ALTER TABLE pull_requests ADD COLUMN status text NOT NULL DEFAULT 'open', ADD COLUMN ci_status text NOT NULL DEFAULT 'pending', ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(), ADD COLUMN head_ref text, ADD COLUMN body text`)
	require.NoError(t, err, "add ordinary PR fields to isolated scheduling fixture")
	var migrations strings.Builder
	for _, filename := range []string{"000094_pull_request_health.up.sql", "000194_pull_request_repair_head_identity.up.sql", "000257_pull_request_check_states.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", filename))
		require.NoError(t, err, "read actual health and projection schema")
		migrations.Write(migration)
		migrations.WriteByte('\n')
	}
	_, err = pool.Exec(context.Background(), migrations.String())
	require.NoError(t, err, "apply actual health and projection schema to owned test database")
	_, err = pool.Exec(context.Background(), `UPDATE pull_requests SET head_sha='old-head',base_sha='old-base' WHERE org_id=$1 AND id=$2`, orgID, prID)
	require.NoError(t, err, "establish original revision identity")
	return pool, orgID, prID
}

func healthSyncSnapshot(orgID, prID uuid.UUID, head, base string, version int64) PullRequestHealthSyncSnapshot {
	return PullRequestHealthSyncSnapshot{
		ExpectedHeadSHA: "old-head", ExpectedBaseSHA: "old-base", HeadSHA: head, BaseSHA: base, CheckStateVersion: version,
		CIStatus: models.PullRequestCIStatusSuccess,
		Summary:  models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean, ChecksConfirmed: true, Checks: []models.PullRequestCheckSummary{{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed, Provider: "test-provider"}}},
		Checks:   []models.PullRequestCheckState{{OrgID: orgID, PullRequestID: prID, HeadSHA: head, Source: models.PullRequestCheckSourceCheckRun, ExternalKey: "unit tests", Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed, Provider: "test-provider", ProviderSequence: version, ProviderUpdatedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)}},
	}
}

func TestApplyHealthSyncSlowReadCannotRegressNewHeadPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, prID := newHealthSyncPostgres(t)
	ctx := context.Background()
	store := NewPullRequestStore(pool)
	oldVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve older read before its delayed response")
	oldSnapshot := healthSyncSnapshot(orgID, prID, "old-head", "old-base", oldVersion)
	finish := make(chan struct{})
	type result struct {
		current models.PullRequestHealthCurrent
		applied bool
		err     error
	}
	oldDone := make(chan result, 1)
	go func() {
		<-finish
		current, applied, err := store.ApplyHealthSync(ctx, orgID, prID, oldSnapshot)
		oldDone <- result{current, applied, err}
	}()
	newVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve newer head-change read independently")
	newCurrent, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "new-head", "new-base", newVersion))
	require.NoError(t, err, "persist newer head and projection atomically")
	require.True(t, applied, "new read must establish current health")
	newCurrent, err = store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read canonical committed newer baseline")
	close(finish)
	old := <-oldDone
	require.NoError(t, old.err, "older completion must skip without retry failure")
	require.False(t, old.applied, "older completion must not overwrite a newer read")
	require.Equal(t, newCurrent, old.current, "older completion must return newer baseline intact")
	current, err := store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read durable health after both completions")
	require.Equal(t, newCurrent, current, "slow read must not regress summary, head, base, or reserved version")
	var head, base, ci, checkHead string
	var checkVersion, snapshots int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,base_sha,ci_status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&head, &base, &ci), "read hot PR state after delayed writer")
	require.Equal(t, []string{"new-head", "new-base", "success"}, []string{head, base, ci}, "older CI and identity values cannot escape the write guard")
	require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&checkHead, &checkVersion), "read newer check projection after delayed writer")
	require.Equal(t, "new-head", checkHead, "old completion must not delete newer-head projection")
	require.Equal(t, newVersion, checkVersion, "new projection ordering must survive old completion")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pull_request_health_snapshots WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&snapshots), "count committed health history")
	require.Equal(t, int64(1), snapshots, "skipped completion must not fabricate a health version")
}

func TestApplyHealthSyncPartialWebhookAndRollbackPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                               string
		headWebhook, baseWebhook, rejectCI bool
	}{
		{name: "partial head webhook wins", headWebhook: true},
		{name: "partial base webhook wins", baseWebhook: true},
		{name: "CI failure rolls all writes back", rejectCI: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newHealthSyncPostgres(t)
			ctx := context.Background()
			store := NewPullRequestStore(pool)
			version, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve baseline read")
			baseline, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", version))
			require.NoError(t, err, "persist original health baseline")
			require.True(t, applied, "baseline must be committed")
			baseline, err = store.GetHealthCurrent(ctx, orgID, prID)
			require.NoError(t, err, "read canonical committed original baseline")
			version, err = store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve delayed candidate")
			candidate := healthSyncSnapshot(orgID, prID, "old-head", "old-base", version)
			if tt.headWebhook {
				require.NoError(t, store.UpdateHeadSHA(ctx, orgID, prID, "webhook-head"), "simulate webhook invalidating only the hot PR head")
			}
			if tt.baseWebhook {
				require.NoError(t, store.UpdateGitHubSnapshot(ctx, orgID, prID, PullRequestGitHubSnapshot{GitHubPRURL: "https://github.com/example/repository/pull/1", Title: "Change", HeadSHA: new("old-head"), BaseSHA: new("webhook-base")}), "simulate webhook invalidating only the hot PR base")
			}
			if tt.rejectCI {
				_, err = pool.Exec(ctx, `ALTER TABLE pull_requests ADD CONSTRAINT test_ci_accept CHECK (ci_status <> 'failure')`)
				require.NoError(t, err, "inject a CI persistence failure after projection and summary savepoints")
				candidate.CIStatus = models.PullRequestCIStatusFailure
				candidate.Summary.NeedsAgentAction = true
			}
			_, applied, err = store.ApplyHealthSync(ctx, orgID, prID, candidate)
			if tt.rejectCI {
				require.Error(t, err, "CI failure must abort the outer atomic write")
			} else {
				require.NoError(t, err, "partial-row invalidation must produce a skipped result")
			}
			require.False(t, applied, "failed or invalidated candidate cannot change current health")
			current, err := store.GetHealthCurrent(ctx, orgID, prID)
			require.NoError(t, err, "read current health after skipped or aborted write")
			require.Equal(t, baseline, current, "guard or rollback must preserve full health baseline")
			var head string
			var projectionVersion int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,projection_version FROM pull_request_check_states WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&head, &projectionVersion), "read projection after skipped or aborted write")
			require.Equal(t, "old-head", head, "candidate must not delete the prior projection")
			require.Equal(t, baseline.CheckStateVersion, projectionVersion, "candidate must not leak a newer projection version through a savepoint")
			var ci string
			require.NoError(t, pool.QueryRow(ctx, `SELECT ci_status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&ci), "read CI after aborted or skipped update")
			require.Equal(t, "success", ci, "CI must remain consistent with the preserved baseline")
		})
	}
}

func TestApplyHealthSyncSurvivesWebhookRebuildPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, prID := newHealthSyncPostgres(t)
	ctx := context.Background()
	store := NewPullRequestStore(pool)
	baselineVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve baseline authoritative read")
	baselineSnapshot := healthSyncSnapshot(orgID, prID, "old-head", "old-base", baselineVersion)
	baselineSnapshot.Summary.MergeState = models.PullRequestMergeStateBlocked
	baselineSnapshot.Summary.ChecksConfirmed = false
	baselineSnapshot.Summary.Checks[0].Status = models.PullRequestCheckStatusPending
	baselineSnapshot.Checks[0].Status = models.PullRequestCheckStatusPending
	baselineSnapshot.CIStatus = models.PullRequestCIStatusPending
	_, applied, err := store.ApplyHealthSync(ctx, orgID, prID, baselineSnapshot)
	require.NoError(t, err, "persist blocked baseline")
	require.True(t, applied, "baseline must be authoritative")
	freshVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve fresh GitHub read before the webhook")
	webhook := baselineSnapshot.Checks[0]
	webhook.Status = models.PullRequestCheckStatusPassed
	webhook.ProviderSequence++
	webhook.ProviderUpdatedAt = webhook.ProviderUpdatedAt.Add(time.Minute)
	applied, err = store.UpsertCheckState(ctx, orgID, webhook)
	require.NoError(t, err, "commit webhook after fresh read began")
	require.True(t, applied, "webhook must advance its projection sequence")
	baseline, err := store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read baseline before local rebuild")
	states, err := store.ListCheckStatesAfter(ctx, orgID, prID, "old-head", baseline.CheckStateVersion)
	require.NoError(t, err, "read committed webhook for local rebuild")
	require.Len(t, states, 1, "one webhook must be newer than the baseline")
	webhookVersion := states[0].ProjectionVersion
	require.Greater(t, webhookVersion, freshVersion, "webhook counter must advance beyond the in-flight authoritative reservation")
	var projectedSummary models.PullRequestHealthSummary
	require.NoError(t, json.Unmarshal(baseline.SummaryJSON, &projectedSummary), "decode authoritative summary before projection")
	projectedSummary.Checks = []models.PullRequestCheckSummary{states[0].CheckSummary()}
	projectedSummary.ChecksConfirmed = true
	projected, applied, _, err := store.UpsertProjectedHealthSummary(ctx, orgID, prID, "old-head", "old-base", projectedSummary, nil, ProjectedHealthSummaryOptions{ExpectedHealthVersion: baseline.Version, ExpectedHeadSHA: "old-head", ExpectedCheckStateVersion: baseline.CheckStateVersion, AppliedCheckStateVersion: webhookVersion})
	require.NoError(t, err, "persist local rebuild while GitHub read is still in flight")
	require.True(t, applied, "local rebuild must advance check projection")
	require.Greater(t, projected.CheckStateVersion, freshVersion, "rebuild reproduces the reviewed race")
	freshSnapshot := healthSyncSnapshot(orgID, prID, "old-head", "old-base", freshVersion)
	freshSnapshot.Summary.ChecksConfirmed = false
	freshSnapshot.Summary.Checks[0].Status = models.PullRequestCheckStatusPending
	freshSnapshot.Checks[0].Status = models.PullRequestCheckStatusPending
	freshSnapshot.Checks[0].ProviderSequence = baselineSnapshot.Checks[0].ProviderSequence
	freshSnapshot.CIStatus = models.PullRequestCIStatusPending
	fresh, applied, err := store.ApplyHealthSync(ctx, orgID, prID, freshSnapshot)
	require.NoError(t, err, "fresh GitHub state must survive projection-only advancement")
	require.True(t, applied, "webhook rebuild is not a newer authoritative read")
	require.Equal(t, freshVersion, fresh.CheckStateVersion, "fresh snapshot restores its read barrier so later webhook rows can overlay")
	var freshSummary models.PullRequestHealthSummary
	require.NoError(t, json.Unmarshal(fresh.SummaryJSON, &freshSummary), "decode persisted fresh mergeability")
	require.Equal(t, models.PullRequestMergeStateClean, freshSummary.MergeState, "fresh clean mergeability cannot be silently discarded")
	require.Equal(t, freshVersion, freshSummary.GitHubSyncVersion, "only GitHub read advances the authoritative watermark")
	pending, err := store.HasCheckStatesAfter(ctx, orgID, prID, "old-head", freshVersion)
	require.NoError(t, err, "detect the webhook that raced the authoritative read")
	require.True(t, pending, "newer webhook row must survive reconciliation for the existing overlay path")
	states, err = store.ListCheckStatesAfter(ctx, orgID, prID, "old-head", fresh.CheckStateVersion)
	require.NoError(t, err, "load the retained webhook for overlay")
	require.Len(t, states, 1, "retained webhook must still be newer than the fresh read barrier")
	freshSummary.Checks = []models.PullRequestCheckSummary{states[0].CheckSummary()}
	freshSummary.ChecksConfirmed = true
	final, applied, _, err := store.UpsertProjectedHealthSummary(ctx, orgID, prID, "old-head", "old-base", freshSummary, nil, ProjectedHealthSummaryOptions{ExpectedHealthVersion: fresh.Version, ExpectedHeadSHA: "old-head", ExpectedCheckStateVersion: fresh.CheckStateVersion, AppliedCheckStateVersion: webhookVersion})
	require.NoError(t, err, "overlay newer webhook onto fresh GitHub mergeability")
	require.True(t, applied, "overlay must restore the latest check status")
	require.NoError(t, store.UpdateCIStatus(ctx, orgID, prID, models.PullRequestCIStatusSuccess), "complete the normal overlay CI follow-through")
	var finalSummary models.PullRequestHealthSummary
	require.NoError(t, json.Unmarshal(final.SummaryJSON, &finalSummary), "decode final merge and check state")
	require.Equal(t, models.PullRequestMergeStateClean, finalSummary.MergeState, "overlay must preserve fresh mergeability")
	require.Equal(t, models.PullRequestCheckStatusPassed, finalSummary.Checks[0].Status, "overlay must preserve newer webhook check status")
	require.Equal(t, freshVersion, finalSummary.GitHubSyncVersion, "overlay cannot advance the authoritative watermark")
	require.Equal(t, webhookVersion, final.CheckStateVersion, "projection watermark remains independently advanced")
	_, applied, err = store.ApplyHealthSync(ctx, orgID, prID, baselineSnapshot)
	require.NoError(t, err, "older actual GitHub read must skip without retry failure")
	require.False(t, applied, "older authoritative read is still fenced on the same head")
	noOpVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve unchanged follow-up read")
	noOpSnapshot := healthSyncSnapshot(orgID, prID, "old-head", "old-base", noOpVersion)
	noOpSnapshot.Summary = finalSummary
	noOpSnapshot.Checks = []models.PullRequestCheckState{webhook}
	noOp, applied, err := store.ApplyHealthSync(ctx, orgID, prID, noOpSnapshot)
	require.NoError(t, err, "unchanged follow-up must still advance authoritative freshness")
	require.True(t, applied, "latest unchanged read must be applied")
	require.Equal(t, final.Version, noOp.Version, "metadata-only freshness cannot create a health snapshot version")
	var noOpSummary models.PullRequestHealthSummary
	require.NoError(t, json.Unmarshal(noOp.SummaryJSON, &noOpSummary), "decode latest freshness metadata")
	require.Equal(t, noOpVersion, noOpSummary.GitHubSyncVersion, "no-op health update still advances authoritative ordering")
}

func TestApplyHealthSyncRepairsTypeMismatchedLegacySummaryPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                       string
		invalidMarker, newerMarker bool
	}{
		{name: "unrelated legacy field mismatch is repaired"},
		{name: "undecodable marker is repaired with bounded warning", invalidMarker: true},
		{name: "valid newer marker retains its fence", newerMarker: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, prID := newHealthSyncPostgres(t)
			ctx := context.Background()
			store := NewPullRequestStore(pool)
			baselineVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve original authoritative state")
			_, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", baselineVersion))
			require.NoError(t, err, "persist original authoritative state")
			require.True(t, applied, "baseline must be committed")
			candidateVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
			require.NoError(t, err, "reserve recovery candidate")
			marker := any(baselineVersion)
			if tt.newerMarker {
				newerVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
				require.NoError(t, err, "reserve a newer authoritative owner")
				_, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", newerVersion))
				require.NoError(t, err, "persist newer authoritative owner")
				require.True(t, applied, "newer owner must establish its fence")
				marker = newerVersion
			}
			if tt.invalidMarker {
				marker = "legacy-marker-type"
			}
			legacyJSON, err := json.Marshal(map[string]any{"github_sync_version": marker, "merge_state": []string{"legacy-field-type"}, "checks": "legacy-summary-canary"})
			require.NoError(t, err, "encode syntactically valid but type-mismatched legacy JSONB")
			_, err = pool.Exec(ctx, `UPDATE pull_request_health_current SET summary_json=$3,summary_preview_json=$3 WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID, legacyJSON)
			require.NoError(t, err, "seed legacy summary shape inside the owned fixture")
			var legacy models.PullRequestHealthSummary
			require.Error(t, json.Unmarshal(legacyJSON, &legacy), "fixture must reproduce the legacy typed-summary decoding failure")
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			current, applied, err := store.ApplyHealthSync(logger.WithContext(ctx), orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", candidateVersion))
			require.NoError(t, err, "legacy field types cannot permanently block authoritative recovery")
			require.Equal(t, !tt.newerMarker, applied, "a valid newer marker still fences an older recovery candidate")
			if tt.newerMarker {
				require.JSONEq(t, string(legacyJSON), string(current.SummaryJSON), "newer owner must not be overwritten despite unrelated malformed legacy fields")
			} else {
				var recovered models.PullRequestHealthSummary
				require.NoError(t, json.Unmarshal(current.SummaryJSON, &recovered), "full sync must replace the malformed shape with valid fresh state")
				require.Equal(t, candidateVersion, recovered.GitHubSyncVersion, "successful recovery must establish a valid authoritative watermark")
				require.Equal(t, models.PullRequestMergeStateClean, recovered.MergeState, "recovery must persist fresh mergeability")
				require.Equal(t, healthSyncSnapshot(orgID, prID, "old-head", "old-base", candidateVersion).Summary.Checks, recovered.Checks, "recovery must persist the full fresh check set")
				var head, base, ci string
				require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha,base_sha,ci_status FROM pull_requests WHERE org_id=$1 AND id=$2`, orgID, prID).Scan(&head, &base, &ci), "read fresh hot-row state after recovery")
				require.Equal(t, []string{"old-head", "old-base", "success"}, []string{head, base, ci}, "recovery must keep revision identity and CI consistent")
			}
			if tt.invalidMarker {
				var warning map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &warning), "invalid marker should emit one bounded warning")
				require.Equal(t, map[string]any{"level": "warn", "org_id": orgID.String(), "pull_request_id": prID.String(), "message": "stored pull request health summary has an invalid authoritative sync version"}, warning, "warning must contain only fixed fields and omit raw summary and error contents")
			} else {
				require.Empty(t, logs.String(), "unrelated legacy fields cannot produce an undecodable-marker warning")
			}
		})
	}
}

func TestApplyHealthSyncIdenticalContentPreservesSnapshotAndEnrichmentPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, prID := newHealthSyncPostgres(t)
	ctx := context.Background()
	store := NewPullRequestStore(pool)
	firstVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve first authoritative read")
	first, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", firstVersion))
	require.NoError(t, err, "persist first JSONB summary")
	require.True(t, applied, "first read must establish health")
	require.NoError(t, store.UpdateHealthEnrichment(ctx, orgID, prID, first.Version, json.RawMessage(`{"files":["example.go"]}`), json.RawMessage(`{"tests":["unit tests"]}`), models.PullRequestHealthEnrichmentStatusReady), "persist enrichment for the first semantic snapshot")
	firstCurrent, err := store.GetHealthCurrent(ctx, orgID, prID)
	require.NoError(t, err, "read canonical JSONB and enrichment before identical write")
	firstSnapshot, err := store.GetHealthSnapshot(ctx, orgID, prID, first.Version)
	require.NoError(t, err, "capture immutable enriched snapshot before identical write")
	secondVersion, err := store.ReserveCheckStateVersion(ctx, orgID, prID)
	require.NoError(t, err, "reserve second read with identical health content")
	second, applied, err := store.ApplyHealthSync(ctx, orgID, prID, healthSyncSnapshot(orgID, prID, "old-head", "old-base", secondVersion))
	require.NoError(t, err, "persist semantically identical summary with fresh authoritative ordering")
	require.True(t, applied, "identical read must still update freshness")
	require.Equal(t, firstCurrent.Version, second.Version, "JSONB formatting and reservation metadata cannot create a semantic health version")
	require.Equal(t, firstCurrent.EnrichmentStatus, second.EnrichmentStatus, "identical content must retain ready enrichment")
	require.Equal(t, firstCurrent.EnrichedAt, second.EnrichedAt, "identical content must retain the existing enrichment timestamp")
	var summary models.PullRequestHealthSummary
	require.NoError(t, json.Unmarshal(second.SummaryJSON, &summary), "decode latest authoritative metadata")
	require.Equal(t, secondVersion, summary.GitHubSyncVersion, "freshness watermark advances independently of health version")
	require.Equal(t, secondVersion, second.CheckStateVersion, "latest authoritative read updates its projection barrier")
	secondSnapshot, err := store.GetHealthSnapshot(ctx, orgID, prID, first.Version)
	require.NoError(t, err, "read immutable enriched snapshot after identical write")
	require.Equal(t, firstSnapshot, secondSnapshot, "identical content cannot alter snapshot history or cached detail payloads")
	var snapshots int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pull_request_health_snapshots WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID).Scan(&snapshots), "count semantic snapshot history after identical writes")
	require.Equal(t, int64(1), snapshots, "two identical summaries must leave one semantic snapshot")
}
