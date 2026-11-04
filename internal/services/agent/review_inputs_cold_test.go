package agent

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func reviewInputAttemptStages(t *testing.T, output []byte) []string {
	t.Helper()
	var stages []string
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var event struct {
			Stage   string `json:"stage"`
			Message string `json:"message"`
		}
		err := decoder.Decode(&event)
		if err == io.EOF {
			return stages
		}
		require.NoError(t, err, "review preparation timing must contain structured JSON")
		if event.Message == "stage started" {
			switch event.Stage {
			case "review_inputs_verify", "review_inputs_local_prepare", "review_inputs_hydrate":
				stages = append(stages, event.Stage)
			}
		}
	}
}

func TestColdReviewInputsExpiredBudgetCannotCreateBundle(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	var logs bytes.Buffer
	provider, tokens := &managedReviewInputsProvider{}, &reviewInputsTokenSpy{}
	o := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.New(&logs)}
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	ownerCtx := reviewPreparationContext(uuid.New(), uuid.New())
	require.NoError(t, o.markCodeReviewInputsPreparing(ownerCtx, session, sandbox), "cold verification fixture must retain an existing preparation owner")
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "expired cold input identity must remain valid")
	marker := codeReviewInputPreparationMarker(sandbox, identity)
	before, err := os.ReadFile(marker)
	require.NoError(t, err, "preparation owner must be captured before expired verification")
	expired := WithCodeReviewWorkspacePreparationDeadline(ownerCtx, time.Now().Add(-time.Second))
	err = o.prepareColdCodeReviewInputs(expired, session, sandbox)
	require.ErrorIs(t, err, context.DeadlineExceeded, "expired cold preparation must never obtain a new creation allowance")
	require.Equal(t, []string{"review_inputs_verify"}, reviewInputAttemptStages(t, logs.Bytes()), "expired cold preparation must perform only the bounded verification attempt")
	require.Equal(t, int64(0), tokens.calls.Load(), "expired cold verification must never authenticate or hydrate")
	require.Equal(t, []reviewGitAuthentication{{}}, provider.authentications, "expired verification must not forward installation authentication")
	manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
	require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "expired cold preparation must leave the absent bundle uncreated")
	after, err := os.ReadFile(marker)
	require.NoError(t, err, "expired cold verification must preserve preparation ownership")
	require.Equal(t, before, after, "expired cold verification must not clear or replace the owner marker")
}

func TestColdReviewInputsRetainIdentityAndSourceValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		wantStages     []string
		wantIntegrity  bool
		wantTokenCalls int64
	}{
		{name: "invalid tenant identity"},
		{name: "wrong checkout head", wantStages: []string{"review_inputs_hydrate"}, wantTokenCalls: 1},
		{name: "substituted source object", wantStages: []string{"review_inputs_hydrate"}, wantIntegrity: true, wantTokenCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			f.workDir = filepath.Join(t.TempDir(), "cold-inputs")
			clone := exec.Command("git", "clone", "-q", "--no-hardlinks", "--branch=review", strings.TrimPrefix(f.remote, "file://"), f.workDir)
			clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			output, err := clone.CombinedOutput()
			require.NoError(t, err, "complete isolated source fixture must clone without hardlinks: %s", output)
			reviewInputsGit(t, f.workDir, "remote", "set-url", "origin", f.remote)
			reviewInputsWritableCleanup(t, f.workDir)
			session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
			switch tt.name {
			case "invalid tenant identity":
				session.OrgID = uuid.Nil
			case "wrong checkout head":
				reviewInputsGit(t, f.workDir, "checkout", "-q", "--detach", f.base)
			case "substituted source object":
				oid := reviewInputsGit(t, f.workDir, "rev-parse", f.head+":changed.go")
				modified := []byte("substituted implementation\n")
				var encoded bytes.Buffer
				compressed := zlib.NewWriter(&encoded)
				_, err := fmt.Fprintf(compressed, "blob %d%c", len(modified), byte(0))
				require.NoError(t, err, "substituted object must retain a valid Git header")
				_, err = compressed.Write(modified)
				require.NoError(t, err, "substituted object must contain valid zlib content")
				require.NoError(t, compressed.Close(), "substituted object stream must finish")
				loose := filepath.Join(f.workDir, ".git/objects", oid[:2], oid[2:])
				require.NoError(t, os.Chmod(loose, 0o600), "isolated loose object must permit corruption")
				require.NoError(t, os.WriteFile(loose, encoded.Bytes(), 0o600), "substituted content must retain its original requested object ID")
				observed, err := exec.Command("git", "-C", f.workDir, "cat-file", "blob", oid).Output()
				require.NoError(t, err, "ordinary Git reads must accept the structurally valid substituted object")
				require.Equal(t, modified, observed, "the fixture must expose substituted source to ordinary Git reads")
			}
			var logs bytes.Buffer
			provider, tokens := &managedReviewInputsProvider{}, &reviewInputsTokenSpy{}
			o := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.New(&logs)}
			err = o.prepareColdCodeReviewInputs(context.Background(), session, sandbox)
			require.Error(t, err, "cold preparation must reject invalid identity, checkout, or captured source")
			if tt.wantIntegrity {
				require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "cold preparation must preserve terminal source-integrity failures")
			}
			require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "invalid cold inputs must not become transient readiness retries")
			require.Equal(t, tt.wantStages, reviewInputAttemptStages(t, logs.Bytes()), "direct cold hydration must retain identity and integrity validation")
			require.Equal(t, tt.wantTokenCalls, tokens.calls.Load(), "identity validation must precede scoped authentication and captured source validation")
			manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "invalid cold inputs must never publish an immutable review bundle")
		})
	}
}

func TestColdReviewInputsCompleteSourceNeedsNoFetch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		withoutCredentials bool
		wantStages         []string
		wantTokenCalls     int
	}{
		{name: "current credentials", wantStages: []string{"review_inputs_hydrate"}, wantTokenCalls: 1},
		{name: "without credential provider", withoutCredentials: true, wantStages: []string{"review_inputs_local_prepare"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			f.workDir = filepath.Join(t.TempDir(), "complete-cold-inputs")
			clone := exec.Command("git", "clone", "-q", "--no-hardlinks", "--branch=review", f.remote, f.workDir)
			clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			output, err := clone.CombinedOutput()
			require.NoError(t, err, "complete cold source fixture must clone all captured objects: %s", output)
			reviewInputsWritableCleanup(t, f.workDir)
			expectedPatch, err := exec.Command("git", "-C", f.workDir, "diff", "--binary", "--no-ext-diff", "--no-textconv", f.base+"..."+f.head, "--").Output()
			require.NoError(t, err, "complete fixture must yield its independently captured diff")
			remote := strings.TrimPrefix(f.remote, "file://")
			require.NoError(t, os.Rename(remote, remote+"-offline"), "remote must be unavailable before cold preparation")
			var logs bytes.Buffer
			provider := &managedReviewInputsProvider{}
			tokens := &reviewConfigGitHubTokens{token: "synthetic-current-token"}
			o := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.New(&logs)}
			if tt.withoutCredentials {
				o.github = nil
			}
			session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
			require.NoError(t, o.prepareColdCodeReviewInputs(context.Background(), session, sandbox), "complete cold inputs must prepare while their remote is physically unavailable")
			require.Equal(t, tt.wantStages, reviewInputAttemptStages(t, logs.Bytes()), "complete source must use the supported single preparation pass")
			require.Equal(t, tt.wantTokenCalls, tokens.calls, "direct hydration must resolve credentials once while offline fallback needs none")
			manifestPath, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			manifest, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
			require.NoError(t, err, "offline cold preparation must publish captured input identity")
			patch, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
			require.NoError(t, err, "offline cold preparation must publish its immutable diff")
			require.Equal(t, expectedPatch, patch, "cold preparation must preserve the exact captured diff")
			require.NotContains(t, reviewInputsGit(t, f.workDir, "config", "--list"), tokens.token, "cold preparation must not persist authentication in Git configuration")
			require.NotContains(t, logs.String(), tokens.token, "cold preparation must not log authentication")
			logs.Reset()
			require.NoError(t, o.ensureCodeReviewInputs(context.Background(), session, sandbox), "completed cold inputs must remain reusable offline")
			require.Equal(t, []string{"review_inputs_verify"}, reviewInputAttemptStages(t, logs.Bytes()), "warm reuse must keep its verification-only fast path")
			after, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
			require.NoError(t, err, "warm verification must retain the captured manifest")
			require.Equal(t, manifest, after, "warm verification must not rewrite captured identity")
			require.Equal(t, tt.wantTokenCalls, tokens.calls, "warm verification must not acquire a token")
		})
	}
}

func TestColdReviewInputsLegacyProviderKeepsLocalFallback(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	tokens := &reviewInputsTokenSpy{}
	var logs bytes.Buffer
	o := &Orchestrator{provider: &reviewInputsProvider{}, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.New(&logs)}
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	require.NoError(t, o.prepareColdCodeReviewInputs(context.Background(), session, sandbox), "legacy provider must retain the local-first fallback")
	require.Equal(t, []string{"review_inputs_local_prepare", "review_inputs_hydrate"}, reviewInputAttemptStages(t, logs.Bytes()), "legacy fallback must detect missing evidence before authenticating hydration")
	require.Equal(t, int64(1), tokens.calls.Load(), "legacy fallback must acquire authentication for the current repository")
}

func TestColdReviewInputsHydrationFailurePreservesOwner(t *testing.T) {
	t.Parallel()
	tokenError := errors.New("installation authentication unavailable")
	tests := []struct {
		name       string
		tokenError error
		wantStages []string
	}{
		{name: "token resolution fails", tokenError: tokenError},
		{name: "missing source fetch fails", wantStages: []string{"review_inputs_hydrate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			reviewInputsWritableCleanup(t, f.workDir)
			provider := &managedReviewInputsProvider{}
			var logs bytes.Buffer
			o := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: testInternalGitHubTokens{token: "synthetic-current-token", err: tt.tokenError}, logger: zerolog.New(&logs)}
			session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
			ctx := reviewPreparationContext(uuid.New(), uuid.New())
			require.NoError(t, o.markCodeReviewInputsPreparing(ctx, session, sandbox), "failure fixture must retain its existing preparation owner")
			identity, err := codeReviewInputIdentityFromSession(session)
			require.NoError(t, err, "failed hydration fixture must have valid captured identity")
			marker := codeReviewInputPreparationMarker(sandbox, identity)
			before, err := os.ReadFile(marker)
			require.NoError(t, err, "preparation owner must be captured before hydration failure")
			remote := strings.TrimPrefix(f.remote, "file://")
			require.NoError(t, os.Rename(remote, remote+"-offline"), "missing-source fixture must fail any hydration fetch")
			err = o.prepareColdCodeReviewInputs(ctx, session, sandbox)
			require.Error(t, err, "authentication or fetch failure must prevent input publication")
			if tt.tokenError != nil {
				require.ErrorIs(t, err, tt.tokenError, "authentication failure must retain its cause")
				require.Empty(t, provider.preparations, "failed token resolution must not launch a hydration command")
			}
			require.Equal(t, tt.wantStages, reviewInputAttemptStages(t, logs.Bytes()), "direct hydration must avoid a redundant local attempt even when it fails")
			require.NotContains(t, err.Error(), "synthetic-current-token", "failed hydration must not expose its installation token")
			manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "failed hydration must not publish an immutable bundle")
			after, err := os.ReadFile(marker)
			require.NoError(t, err, "failed hydration must preserve preparation ownership")
			require.Equal(t, before, after, "failed hydration must never clear the preparation owner's marker")
		})
	}
}
