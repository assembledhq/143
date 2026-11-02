package agent

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
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
	manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
	require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "expired cold preparation must leave the absent bundle uncreated")
	after, err := os.ReadFile(marker)
	require.NoError(t, err, "expired cold verification must preserve preparation ownership")
	require.Equal(t, before, after, "expired cold verification must not clear or replace the owner marker")
}

func TestColdReviewInputsRetainIdentityAndSourceValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		wantStages    []string
		wantIntegrity bool
	}{
		{name: "invalid tenant identity"},
		{name: "wrong checkout head", wantStages: []string{"review_inputs_local_prepare"}},
		{name: "substituted source object", wantStages: []string{"review_inputs_local_prepare"}, wantIntegrity: true},
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
			require.Equal(t, tt.wantStages, reviewInputAttemptStages(t, logs.Bytes()), "cold validation must skip only the initial cache verification attempt")
			require.Equal(t, int64(0), tokens.calls.Load(), "invalid cold inputs must fail before authentication or repair")
			manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "invalid cold inputs must never publish an immutable review bundle")
		})
	}
}
