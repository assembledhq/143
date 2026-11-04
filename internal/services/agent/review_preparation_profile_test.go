package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewPreparationProfileBoundedStream(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	profile := &reviewPreparationProfile{log: zerolog.New(&output), parent: "review_inputs_hydrate"}
	inputs := []string{
		"provider printed synthetic-token /private/workspace\n",
		"143-prep-v1 unknown 10 1\n",
		"143-prep-v1 object_fetch +10 1\n",
		"143-prep-v1 object_fetch 600001 1\n",
		"143-prep-v1 object_fetch 1 100000001\n",
		"143-prep-v1 object_fetch 1 1 synthetic-token\n",
		strings.Repeat("x", 4096),
		"143-prep-v1 object_fetch 1 1\n",
		"143-prep-v1 object_",
		"fetch 17 42\n143-prep-v1 object_fetch 21 8",
	}
	for _, input := range inputs {
		n, err := profile.Write([]byte(input))
		require.NoError(t, err, "profiling should never propagate output errors")
		require.Equal(t, len(input), n, "profiling should consume the entire input")
	}
	require.Equal(t, "{\"level\":\"info\",\"parent_stage\":\"review_inputs_hydrate\",\"profile_stage\":\"object_fetch\",\"elapsed_ms\":17,\"profile_source\":\"sandbox_reported\",\"count\":42,\"message\":\"review preparation profile\"}\n", output.String(), "only a complete allowlisted record should reach the logger")

	_, err := profile.Write([]byte("\n" + strings.Repeat("143-prep-v1 object_fetch 1 1\n", 2*reviewProfileMaxRecords)))
	require.NoError(t, err, "profiling should discard excess records")
	require.Equal(t, reviewProfileMaxRecords, strings.Count(output.String(), "\n"), "one executor stream should have a finite record allowance")
}

func TestReviewPreparationProfileInputScript(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reverse bool
		hydrate bool
		exit    int
		stages  []string
		outcome string
	}{
		{name: "authenticated hydration", hydrate: true, stages: []string{"pre_integrity", "authenticated_prepare", "commit_check", "merge_base", "missing_enumeration", "object_fetch", "patch_generation", "patch_hashing", "post_integrity", "executor"}, outcome: "succeeded"},
		{name: "wrong captured head", reverse: true, hydrate: true, exit: -1, stages: []string{"pre_integrity", "authenticated_prepare", "commit_check", "executor"}, outcome: "failed"},
		{name: "missing local objects", exit: 42, stages: []string{"pre_integrity", "local_prepare", "commit_check", "merge_base", "missing_enumeration", "executor"}, outcome: "incomplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReviewEvidenceGitFixture(t, "", false)
			reviewInputsWritableCleanup(t, fixture.workDir)
			base, head := fixture.base, fixture.head
			if tt.reverse {
				base, head = head, base
			}
			session := reviewInputsSession(t, base, head)
			identity, err := codeReviewInputIdentityFromSession(session)
			require.NoError(t, err, "synthetic revision should satisfy the captured identity format")
			var output bytes.Buffer
			orchestrator := fixture.orchestrator()
			orchestrator.logger = zerolog.New(&output)
			ctx := reviewPreparationContext(identity.OrgID, identity.SessionID)
			exit, err := orchestrator.runCodeReviewInputPreparation(ctx, fixture.sandbox(), identity, fixture.remote, "", tt.hydrate, false)
			if tt.reverse {
				require.ErrorContains(t, err, "workspace HEAD differs", "profiling should preserve the Git identity failure")
				require.NotContains(t, output.String(), "\"profile_stage\":\"post_integrity\"", "a failed preparation should not report an unattempted check")
			} else {
				require.NoError(t, err, "profiling should preserve nonterminal preparation status")
			}
			require.Equal(t, tt.exit, exit, "profiling should preserve the managed exit status")
			for _, stage := range tt.stages {
				require.Contains(t, output.String(), "\"profile_stage\":\""+stage+"\"", "actual preparation should report stage %s", stage)
			}
			require.Contains(t, output.String(), "\"profile_stage\":\"executor\"", "preparation should report its executor")
			require.Contains(t, output.String(), "\"profile_source\":\"orchestrator\",\"outcome\":\""+tt.outcome+"\"", "executor outcome should match preparation")
			parent := "review_inputs_local_prepare"
			if tt.hydrate {
				parent = "review_inputs_hydrate"
			}
			require.Contains(t, output.String(), "\"parent_stage\":\""+parent+"\"", "profiling should retain the existing stage name")
			require.Contains(t, output.String(), "\"org_id\":\""+identity.OrgID.String()+"\"", "profiling should correlate the review organization")
			require.Contains(t, output.String(), "\"session_id\":\""+identity.SessionID.String()+"\"", "profiling should correlate the review session")
			require.Contains(t, output.String(), "\"job_id\":", "profiling should correlate the owning job")
			require.NotContains(t, output.String(), fixture.workDir, "profile must exclude workspace paths")
			require.NotContains(t, output.String(), fixture.remote, "profile must exclude repository URLs")
			require.NotContains(t, output.String(), fixture.head, "profile must exclude revision contents")
		})
	}
}
