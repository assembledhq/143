package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewInputTimingProvider struct {
	SandboxProvider
	exit   int
	err    error
	cancel context.CancelFunc
}

func (p reviewInputTimingProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	if p.cancel != nil {
		p.cancel()
	}
	return p.exit, p.err
}

func TestReviewInputAttemptStageBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		hydrate bool
		verify  bool
		exit    int
		err     error
		cancel  bool
		stage   string
		outcome string
	}{
		{name: "warm verification", verify: true, stage: "review_inputs_verify", outcome: "succeeded"},
		{name: "missing evidence needs preparation", verify: true, exit: 42, stage: "review_inputs_verify", outcome: "incomplete"},
		{name: "local preparation needs hydration", exit: 42, stage: "review_inputs_local_prepare", outcome: "incomplete"},
		{name: "authenticated hydration", hydrate: true, stage: "review_inputs_hydrate", outcome: "succeeded"},
		{name: "failed hydration", hydrate: true, exit: 1, stage: "review_inputs_hydrate", outcome: "failed"},
		{name: "cancelled verification", verify: true, err: context.Canceled, cancel: true, stage: "review_inputs_verify", outcome: "cancelled"},
		{name: "transport failure", verify: true, err: errors.New("synthetic transport failure"), stage: "review_inputs_verify", outcome: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			provider := reviewInputTimingProvider{exit: tt.exit, err: tt.err}
			if tt.cancel {
				provider.cancel = cancel
			}
			var output bytes.Buffer
			o := &Orchestrator{provider: provider, logger: zerolog.New(&output)}
			identity := codeReviewInputIdentity{Version: 1, OrgID: uuid.New(), SessionID: uuid.New(), RepositoryID: uuid.New(), Repository: "example/repo", BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("a", 40)}
			jobID := uuid.New()
			ctx = jobctx.WithJobID(ctx, jobID)
			exit, err := o.runCodeReviewInputPreparation(ctx, &Sandbox{ID: "sandbox", WorkDir: "/workspace"}, identity, "https://example.invalid/repo.git", "synthetic-secret", tt.hydrate, tt.verify)
			if tt.err != nil || tt.exit == 1 {
				require.Error(t, err, "failed preparation must retain its error")
			} else {
				require.NoError(t, err, "successful or incomplete attempt must preserve fallback semantics")
				require.Equal(t, tt.exit, exit, "incomplete evidence must retain its sentinel exit")
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			require.Len(t, lines, 3, "every attempt must emit one start, executor profile, and end")
			for index, line := range lines {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event), "boundary must be structured JSON")
				if index == 1 {
					require.Equal(t, tt.stage, event["parent_stage"], "executor profile must identify its bounded operation")
					require.Equal(t, "executor", event["profile_stage"], "the middle record must report executor time")
					require.Equal(t, "orchestrator", event["profile_source"], "executor duration must be measured outside the sandbox")
				} else {
					require.Equal(t, tt.stage, event["stage"], "boundary must identify its bounded operation")
				}
				require.Equal(t, []string{"stage started", "review preparation profile", "stage ended"}[index], event["message"], "profile must preserve the ordered stage boundaries")
				require.Equal(t, identity.OrgID.String(), event["org_id"], "boundary must include tenant scope")
				require.Equal(t, identity.SessionID.String(), event["session_id"], "boundary must identify the review")
				require.Equal(t, jobID.String(), event["job_id"], "boundary must identify the preparation job")
				if index > 0 {
					require.Equal(t, tt.outcome, event["outcome"], "end must distinguish incomplete, failed, and successful work")
				}
			}
			require.NotContains(t, output.String(), "synthetic-secret", "timing must exclude credentials")
			require.NotContains(t, output.String(), "example.invalid", "timing must exclude clone URLs")
		})
	}
}
