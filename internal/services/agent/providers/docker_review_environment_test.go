package providers

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestDockerProviderValidateReviewPreparationEnvironment(t *testing.T) {
	t.Parallel()
	const unverifiable = "review workspace environment could not be verified; create a fresh workspace before review preparation"
	const storedCredential = "review workspace stores a GitHub credential in its container environment; create a fresh workspace before review preparation"
	tests := []struct {
		name            string
		env             []string
		missingConfig   bool
		inspectError    bool
		alreadyCanceled bool
		alreadyExpired  bool
		expectedError   string
	}{
		{name: "no stored environment"},
		{name: "both credentials empty", env: []string{"GITHUB_TOKEN=", "GH_TOKEN="}},
		{name: "keys without values", env: []string{"GITHUB_TOKEN", "GH_TOKEN"}},
		{name: "unrelated keys", env: []string{"HOME=/sandbox", "PATH=/sandbox/bin:/usr/bin", "GITHUB_TOKEN_BACKUP=fake-unrelated", "gh_token=fake-unrelated"}},
		{name: "stored installation credential", env: []string{"GITHUB_TOKEN=fake-installation-secret"}, expectedError: storedCredential},
		{name: "stored CLI credential", env: []string{"GH_TOKEN=fake-cli-secret"}, expectedError: storedCredential},
		{name: "stored credentials survive empty duplicates", env: []string{"GITHUB_TOKEN=fake-secret", "GITHUB_TOKEN="}, expectedError: storedCredential},
		{name: "nonempty whitespace value", env: []string{"GH_TOKEN= "}, expectedError: storedCredential},
		{name: "equals inside credential value", env: []string{"GH_TOKEN=fake=secret"}, expectedError: storedCredential},
		{name: "missing actual config", missingConfig: true, expectedError: unverifiable},
		{name: "inspection fails safely", inspectError: true, expectedError: unverifiable},
		{name: "canceled admission", alreadyCanceled: true, expectedError: unverifiable},
		{name: "expired inspection budget", alreadyExpired: true, expectedError: unverifiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if tt.alreadyExpired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
			}
			if tt.alreadyCanceled {
				cancel()
			}
			var logs bytes.Buffer
			calls := 0
			inspectionCause := errors.New("fake SDK detail includes GITHUB_TOKEN=fake-sensitive-inspection-detail")
			mock := &mockDockerClient{}
			mock.containerInspectFn = func(gotCtx context.Context, containerID string) (container.InspectResponse, error) {
				calls++
				require.Equal(t, "review-environment", containerID, "must inspect the actual sandbox container")
				require.True(t, gotCtx == ctx, "must pass the caller's preparation deadline to Docker inspection")
				if tt.inspectError {
					return container.InspectResponse{}, inspectionCause
				}
				if tt.missingConfig {
					return container.InspectResponse{}, nil
				}
				return container.InspectResponse{Config: &container.Config{Env: append([]string(nil), tt.env...)}}, nil
			}
			sandbox := &agent.Sandbox{ID: "review-environment", Env: map[string]string{"GITHUB_TOKEN": "", "GH_TOKEN": ""}}
			err := NewDockerProvider(mock, zerolog.New(&logs)).ValidateReviewPreparationEnvironment(ctx, sandbox)
			if tt.expectedError == "" {
				require.NoError(t, err, "review must accept containers without stored GitHub credentials")
			} else {
				require.EqualError(t, err, tt.expectedError, "unsafe or unverifiable containers must return fixed guidance without inspected values")
			}
			if tt.inspectError {
				require.ErrorIs(t, err, inspectionCause, "safe inspection errors must preserve their internal cause")
			}
			if tt.alreadyCanceled {
				require.ErrorIs(t, err, context.Canceled, "cancellation must remain recognizable without exposing SDK details")
				require.Equal(t, 0, calls, "canceled admission must not inspect the container")
			} else if tt.alreadyExpired {
				require.ErrorIs(t, err, context.DeadlineExceeded, "expired preparation budgets must remain recognizable")
				require.Equal(t, 0, calls, "expired admission must not inspect the container")
			} else {
				require.Equal(t, 1, calls, "validation must read actual container configuration once")
			}
			require.Equal(t, "", logs.String(), "environment validation must never log credential values or raw inspection details")
		})
	}
}

func TestDockerProviderReviewEnvironmentInspectionCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		ignoreCanceled bool
	}{
		{name: "SDK honors cancellation"},
		{name: "SDK returns success after cancellation", ignoreCanceled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{})
			mock := &mockDockerClient{}
			mock.containerInspectFn = func(ctx context.Context, _ string) (container.InspectResponse, error) {
				close(entered)
				<-ctx.Done()
				if tt.ignoreCanceled {
					return container.InspectResponse{Config: &container.Config{}}, nil
				}
				return container.InspectResponse{}, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- NewDockerProvider(mock, newTestLogger()).ValidateReviewPreparationEnvironment(ctx, &agent.Sandbox{ID: "review-environment"})
			}()
			<-entered
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled, "inspection must propagate cancellation even after a late successful SDK response")
				require.EqualError(t, err, "review workspace environment could not be verified; create a fresh workspace before review preparation", "cancellation guidance must not include container environment values")
			case <-time.After(time.Second):
				t.Fatal("Docker environment inspection did not terminate after cancellation")
			}
		})
	}
}
