package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRequestScopeUsesCurrentJob(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"no job", "current job replaces previous job", "nil job"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			expected := map[string]any{"message": "scope"}
			if name != "no job" {
				ctx = jobctx.WithJobID(ctx, uuid.New())
				id := uuid.Nil
				if name != "nil job" {
					id = uuid.New()
					expected["job_id"] = id.String()
				}
				ctx = jobctx.WithJobID(ctx, id)
			}
			ctx = jobctx.WithLockToken(ctx, uuid.New())
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			addScope(ctx, logger.Log(), RequestMetadata{}).Msg("scope")
			var actual map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "scope should be structured JSON")
			require.Equal(t, expected, actual, "only the current non-nil job identity should be logged; never its lock token")
		})
	}
}

func TestSandboxReportDoesNotInheritJobOwnership(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	metadata := RequestMetadata{
		Kind: RequestKindAPI, AuthType: AuthTypeAppInstallation, InstallationID: 42,
		Caller: "sandbox_gh", SessionID: "session-fixture", OrgID: "org-fixture",
		AssessmentID: uuid.New(), PullRequestID: uuid.New(),
	}
	ctx := jobctx.WithJobID(context.Background(), uuid.New())
	RecordExternalRequest(ctx, zerolog.New(&logs), metadata, http.MethodGet, "/repos/example/fixture/pulls/1", http.StatusOK, http.Header{}, time.Millisecond)
	var actual map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "sandbox observations should be structured JSON")
	scope := map[string]any{}
	for _, key := range []string{"session_id", "org_id", "job_id", "assessment_id", "pull_request_id", "github_observation_trusted", "github_observation_source"} {
		if value, ok := actual[key]; ok {
			scope[key] = value
		}
	}
	require.Equal(t, map[string]any{
		"session_id": "session-fixture", "org_id": "org-fixture",
		"github_observation_trusted": false, "github_observation_source": "sandbox",
	}, scope, "session-lived sandbox reports must not imply ownership by the job that acquired their reporting socket")
}
