package agent

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestManagedSharedReviewInputsWarmVerificationAvoidsFullScansAndConfigurationGuard(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	reviewInputsGit(t, f.source, "checkout", "-q", "review")
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "large-binary.dat"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024*1024), 0o600), "warm streaming must cover an object larger than its bounded read chunks")
	reviewInputsGit(t, f.source, "add", "large-binary.dat")
	reviewInputsGit(t, f.source, "commit", "-qm", "large captured binary")
	f.head = reviewInputsGit(t, f.source, "rev-parse", "HEAD")
	reviewInputsGit(t, f.source, "push", "-q", f.remote, "review")
	reviewInputsGit(t, f.workDir, "fetch", "-q", "origin", "review")
	reviewInputsGit(t, f.workDir, "checkout", "-qB", "review", f.head)
	commands := filepath.Join(t.TempDir(), "git-commands")
	git := filepath.Join(t.TempDir(), "git")
	require.NoError(t, os.WriteFile(git, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+shellEscapeSingleQuote(commands)+"'\nexec /usr/bin/git \"$@\"\n"), 0o700), "trusted Git fixture should record actual Git invocations")
	provider := &managedReviewInputsProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{rewriteTrustedGit: git}}
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "initial evidence must pass strict creation and hydration")
	before, err := os.ReadFile(commands)
	require.NoError(t, err, "initial managed Git invocations should be recorded")
	require.Contains(t, string(before), "fsck --full", "initial preparation must retain full integrity checks")
	reviewInputsGit(t, f.workDir, "gc", "--prune=now")
	reviewInputsGit(t, f.workDir, "commit-graph", "write", "--reachable")
	require.FileExists(t, filepath.Join(f.workDir, ".git", "objects", "info", "commit-graph"), "warm reuse should exercise Git's persisted commit graph")
	reviewInputsGit(t, f.workDir, "remote", "set-url", "origin", "file:///unavailable-review-remote")
	reviewInputsGit(t, f.workDir, "remote", "add", "reviewer", "https://example.invalid/reviewer.git")
	reviewInputsGit(t, f.workDir, "config", "color.ui", "false")
	reviewInputsGit(t, f.workDir, "config", "gc.writeCommitGraph", "true")
	hook := filepath.Join(f.workDir, "unsafe-config-executed")
	reviewInputsGit(t, f.workDir, "config", "core.fsmonitor", "!printf unsafe > '"+hook+"'")
	reviewInputsGit(t, f.workDir, "config", "credential.helper", "!printf unsafe > '"+hook+"'")
	require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "changed.go"), []byte("reviewer edits\n"), 0o600), "warm evidence should tolerate workspace edits")
	provider.mu.Lock()
	stageCount := len(provider.preparations)
	provider.mu.Unlock()
	tokenCount := tokens.calls.Load()
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "local warm evidence must survive routine Git maintenance and reviewer configuration")
	after, err := os.ReadFile(commands)
	require.NoError(t, err, "warm managed Git invocations should be recorded")
	warmCommands := strings.TrimPrefix(string(after), string(before))
	require.NotContains(t, warmCommands, "fsck", "warm verification must perform zero full object scans")
	require.NotContains(t, warmCommands, "fetch", "warm verification must perform zero fetches")
	require.NoFileExists(t, hook, "local verification must not execute credential helpers or fsmonitor")
	require.Equal(t, tokenCount, tokens.calls.Load(), "warm verification must never acquire authentication")
	provider.mu.Lock()
	defer provider.mu.Unlock()
	require.Len(t, provider.preparations, stageCount+1, "warm verification should execute one managed payload")
	warm := provider.preparations[stageCount]
	require.Empty(t, warm.VerifyIntegrity, "warm payload must not contain a full object verifier")
	require.NotContains(t, warm.VerifyCapturedIntegrity, "fsck", "warm captured-object verification must not scan the full repository")
	require.Contains(t, warmCommands, "cat-file --batch", "warm evidence must hash its captured object closure")
	require.Empty(t, warm.ExpectedRepoURL, "warm payload must not constrain local remote configuration")
	require.False(t, warm.GuardConfiguration || warm.AuthenticateGit, "warm verification must not enter the authentication guard")
}

func TestManagedSharedReviewInputsWarmCancellationReapsBatchVerifier(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	orchestrator := &Orchestrator{provider: localReviewEvidenceProvider{}, repositories: f.orchestrator().repositories, github: &reviewInputsTokenSpy{}, logger: zerolog.Nop()}
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "complete cached source should exist before the cancellation probe")
	git := filepath.Join(t.TempDir(), "git")
	child := "import os,pathlib,signal; pathlib.Path('batch-verifier.pid').write_text(str(os.getpid())); signal.pause(); pathlib.Path('escaped-batch').write_text('escaped')"
	script := "#!/bin/sh\ncase \" $* \" in *\" cat-file --batch \"*) exec /usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(child) + "';; esac\nexec /usr/bin/git \"$@\"\n"
	require.NoError(t, os.WriteFile(git, []byte(script), 0o700), "controlled batch reader should expose a real stalled subprocess")
	orchestrator.provider = disconnectedReviewEvidenceProvider{localReviewEvidenceProvider{rewriteTrustedGit: git}}
	ctx, cancel := context.WithCancel(reviewPreparationContext(uuid.New(), uuid.New()))
	defer cancel()
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "cancellable warm verification should retain exact preparation ownership")
	completed := make(chan error, 1)
	var finished atomic.Bool
	go func() {
		completed <- orchestrator.ensureCodeReviewInputs(ctx, session, sandbox)
		finished.Store(true)
	}()
	t.Cleanup(func() {
		cancel()
		require.Eventually(t, finished.Load, 8*time.Second, 10*time.Millisecond, "batch cancellation must complete managed cleanup before fixture removal")
	})
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(f.workDir, "batch-verifier.pid"))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "cancellation must observe a complete actual batch-process PID")
	cancel()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.Canceled, "warm batch cancellation must preserve its owning caller's cause")
		require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "reaped batch children must provide complete cleanup acknowledgement")
	case <-time.After(8 * time.Second):
		t.Fatal("warm batch verification did not finish bounded cancellation cleanup")
	}
	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond, "managed cancellation must reap the stalled batch process")
	require.NoFileExists(t, filepath.Join(f.workDir, "escaped-batch"), "a canceled batch process must never resume execution")
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured input identity should remain valid")
	require.FileExists(t, codeReviewInputPreparationMarker(sandbox, identity), "canceled warm owners must retain preparation ownership for exact lease recovery")
}

func TestCapturedObjectCleanupContinuesAfterEarlierChildFailure(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"kill", "wait", "close"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			identity, err := codeReviewInputIdentityFromSession(reviewInputsSession(t, f.base, f.head))
			require.NoError(t, err, "cleanup fixture should carry complete captured identity")
			encoded, err := json.Marshal(identity)
			require.NoError(t, err, "cleanup fixture should encode its captured inputs")
			prefix, _, found := strings.Cut(prepareCodeReviewInputsScript, "\ndef digest(file):")
			require.True(t, found, "cleanup regression must exercise the actual production object verifier")
			script := prefix + `
real_popen = subprocess.Popen
created = []
fault = sys.argv[8]
def launch(args, **kwargs):
    first = not created
    body = "import signal,sys; print(" + repr(identity["head_sha"] if first else "invalid-header") + ",flush=True); signal.pause()"
    process = real_popen(["/usr/bin/python3", "-I", "-S", "-c", body], **kwargs)
    if first:
        original_kill, original_wait, original_close = process.kill, process.wait, process.stdout.close
        def fail_kill():
            process.kill = original_kill
            raise OSError("synthetic first child kill failure")
        def fail_wait(*args, **kwargs):
            process.wait = original_wait
            raise OSError("synthetic first child wait failure")
        def fail_close():
            process.stdout.close = original_close
            raise OSError("synthetic first child stream failure")
        if fault == "kill":
            process.kill = fail_kill
        elif fault == "wait":
            process.wait = fail_wait
        else:
            process.stdout.close = fail_close
    created.append(process)
    return process
subprocess.Popen = launch
try:
    try:
        verify_captured_objects(identity["base_sha"])
        raise AssertionError("cleanup failure disappeared")
    except ObjectCleanupError as error:
        assert isinstance(error.__cause__, ValueError), "original object failure disappeared"
        assert error.failures, "cleanup failures were not retained"
        assert len(created) == 2 and created[1].poll() is not None, "later batch child was not reaped"
        print("cleanup_failure_preserved_and_later_child_reaped")
finally:
    for process in created:
        if process.poll() is None:
            process.kill()
        process.wait(timeout=1)
`
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", script, string(encoded), ".git/unused", "", strconv.FormatFloat(float64(deadline.UnixNano())/float64(time.Second), 'f', 6, 64), "true", "true", "true", fault)
			command.Dir = f.workDir
			output, err := command.CombinedOutput()
			require.NoError(t, err, "every later child must receive cleanup after an earlier %s failure: %s", fault, output)
			require.Contains(t, string(output), "cleanup_failure_preserved_and_later_child_reaped", "cleanup uncertainty and its original failure must remain observable")
		})
	}
}

func TestGenericSharedReviewInputsPreserveCapturedObjectCleanupUncertainty(t *testing.T) {
	t.Parallel()
	provider := &markerReadinessFailureProvider{exit: 77}
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, github: tokens}
	err := orchestrator.ensureCodeReviewInputs(context.Background(), reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40)), &Sandbox{ID: "shared", WorkDir: "/workspace"})
	require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "generic providers must preserve missing object-process cleanup proof")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "unconfirmed captured object cleanup must never become an availability retry")
	require.Zero(t, tokens.calls.Load(), "unconfirmed captured object cleanup must forbid authentication")
}

func TestManagedSharedReviewInputsPreserveVerifierCleanupExit(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	provider := &managedReviewInputsProvider{}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, logger: zerolog.Nop()}
	ctx := reviewPreparationContext(uuid.New(), uuid.New())
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "uncertain managed verification must retain its preparation owner")
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "managed cleanup fixture must retain valid captured identity")
	provider.marker = codeReviewInputPreparationMarker(sandbox, identity)
	prepareCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(prepareCtx)
	require.NoError(t, err, "managed verifier must receive the existing preparation allowance")
	payload := reviewWorkspacePreparationPayload{WorkDir: f.workDir, Standalone: true, PlatformCommand: true, Prepare: "true", VerifyCapturedIntegrity: "exit 77", DeadlineUnix: deadline, CancelPath: reviewPreparationMarkerPath(sandbox)}
	err = orchestrator.execReviewPreparationPayload(prepareCtx, sandbox, payload, reviewGitAuthentication{}, zerolog.Nop())
	require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "trusted verifier uncertainty must flow through the existing managed cleanup acknowledgement")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "managed verifier cleanup uncertainty must never become a readiness retry")
	require.FileExists(t, provider.marker, "failed managed verifier cleanup must preserve preparation ownership")
	require.FileExists(t, filepath.Join(f.workDir, ".143-test-cleanup-quarantine"), "managed verifier uncertainty must quarantine the workspace")
	require.False(t, provider.clearedBeforeProof.Load(), "uncertain managed verifier cleanup must never release its owner")
}

func TestManagedSharedReviewInputsWarmVerificationHashesCapturedObjects(t *testing.T) {
	t.Parallel()
	for _, object := range []string{"head blob", "base blob", "merge-base blob", "head tree", "head commit", "uncaptured history"} {
		t.Run(object, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			f.workDir = filepath.Join(t.TempDir(), "loose-review")
			clone := exec.Command("git", "clone", "-q", "--no-hardlinks", "--branch=review", strings.TrimPrefix(f.remote, "file://"), f.workDir)
			clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			output, err := clone.CombinedOutput()
			require.NoError(t, err, "loose-object fixture should clone without hardlinks: %s", output)
			reviewInputsGit(t, f.workDir, "remote", "set-url", "origin", f.remote)
			reviewInputsWritableCleanup(t, f.workDir)
			provider, tokens := &managedReviewInputsProvider{}, &reviewInputsTokenSpy{}
			orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
			session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
			require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "initial bundle should pass strict object integrity checks")
			manifestPath, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			manifest, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
			require.NoError(t, err, "captured manifest should exist before source corruption")
			patch, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
			require.NoError(t, err, "captured patch should exist before source corruption")
			target, kind := f.head+":changed.go", "blob"
			switch object {
			case "base blob":
				target = f.base + ":changed.go"
			case "merge-base blob":
				target = reviewInputsGit(t, f.workDir, "merge-base", f.base, f.head) + ":changed.go"
			case "head tree":
				target, kind = f.head+"^{tree}", "tree"
			case "head commit":
				target, kind = f.head, "commit"
			case "uncaptured history":
				target = f.historyBlob
			}
			oid := reviewInputsGit(t, f.source, "rev-parse", target)
			body, err := exec.Command("git", "-C", f.source, "cat-file", kind, oid).Output()
			require.NoError(t, err, "fixture must expose canonical captured content")
			modified := append(bytes.Clone(body), []byte("tampered review source\n")...)
			if kind == "tree" {
				modified = bytes.Replace(body, []byte("changed.go"), []byte("altered.go"), 1)
			}
			require.NotEqual(t, body, modified, "valid Git content must differ under its original object ID")
			var encoded bytes.Buffer
			compressed := zlib.NewWriter(&encoded)
			_, err = fmt.Fprintf(compressed, "%s %d%c", kind, len(modified), byte(0))
			require.NoError(t, err, "loose object should retain a valid canonical header")
			_, err = compressed.Write(modified)
			require.NoError(t, err, "loose object should contain valid zlib data")
			require.NoError(t, compressed.Close(), "loose object stream should finish completely")
			loose := filepath.Join(f.workDir, ".git", "objects", oid[:2], oid[2:])
			require.FileExists(t, loose, "the captured object must already exist solely as a loose file")
			require.NoError(t, os.Chmod(loose, 0o600), "corruption fixture should permit overwriting its owned loose object")
			require.NoError(t, os.WriteFile(loose, encoded.Bytes(), 0o600), "corrupt content should shadow any packed object under the original ID")
			observed, err := exec.Command("git", "-C", f.workDir, "cat-file", kind, oid).Output()
			require.NoError(t, err, "ordinary Git object reads must still accept valid zlib content")
			require.Equal(t, modified, observed, "the regression must expose incorrect source despite an unchanged requested object ID")
			provider.mu.Lock()
			stages := len(provider.preparations)
			provider.mu.Unlock()
			calls := tokens.calls.Load()
			err = orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox)
			if object == "uncaptured history" {
				require.NoError(t, err, "warm verification must not scan uncaptured historical objects")
			} else {
				require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "captured content substitution must remain a terminal integrity failure")
				require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "corrupted captured source must never become a readiness retry")
			}
			require.Equal(t, calls, tokens.calls.Load(), "warm source verification must never authenticate or hydrate")
			manifestAfter, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
			require.NoError(t, err, "warm integrity checks must retain the immutable manifest")
			patchAfter, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
			require.NoError(t, err, "warm integrity checks must retain the immutable patch")
			require.Equal(t, manifest, manifestAfter, "source corruption must not rewrite captured identity")
			require.Equal(t, patch, patchAfter, "source corruption must not rebuild captured evidence")
			provider.mu.Lock()
			defer provider.mu.Unlock()
			require.Len(t, provider.preparations, stages+1, "warm integrity checks must never enter a creation or hydration attempt")
			require.False(t, provider.preparations[stages].AuthenticateGit || provider.preparations[stages].GuardConfiguration, "captured hashes must remain local-only")
			require.NotContains(t, provider.preparations[stages].VerifyCapturedIntegrity, "fsck", "captured-only integrity checks must perform zero full repository scans")
		})
	}
}

func TestManagedSharedReviewInputsDoNotRepairExistingInvalidBundles(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"patch", "manifest", "symlink", "missing tree"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			reviewInputsWritableCleanup(t, f.workDir)
			provider, tokens := &managedReviewInputsProvider{}, &reviewInputsTokenSpy{}
			orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
			session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
			require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "valid evidence should exist before corruption")
			manifest, patch := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			file := filepath.Join(f.workDir, patch)
			switch invalid {
			case "patch", "manifest":
				if invalid == "manifest" {
					file = filepath.Join(f.workDir, manifest)
				}
				require.NoError(t, os.Chmod(file, 0o600), "corruption fixture should permit a single evidence edit")
				require.NoError(t, os.WriteFile(file, []byte("{}"), 0o600), "corrupted evidence must remain an explicit fatal failure")
			case "symlink":
				require.NoError(t, os.Chmod(filepath.Dir(file), 0o700), "symlink fixture should permit replacing its patch")
				require.NoError(t, os.Remove(file), "original patch should be absent before link substitution")
				require.NoError(t, os.Symlink(filepath.Join(f.workDir, manifest), file), "warm evidence must not follow symlink substitutions")
			case "missing tree":
				missing := reviewInputsGit(t, f.workDir, "rev-parse", f.head+":changed.go")
				objects := filepath.Join(f.workDir, ".git", "objects")
				require.NoError(t, os.RemoveAll(filepath.Join(objects, "pack")), "fixture should remove packed captured objects without hydrating them")
				require.NoError(t, os.RemoveAll(filepath.Join(objects, missing[:2])), "captured source must remain unavailable locally")
			}
			provider.mu.Lock()
			stages := len(provider.preparations)
			provider.mu.Unlock()
			calls := tokens.calls.Load()
			err := orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox)
			require.Error(t, err, "invalid existing evidence must fail closed")
			require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "identity and evidence violations must remain fatal")
			require.Equal(t, calls, tokens.calls.Load(), "invalid existing bundles must never acquire hydration authentication")
			provider.mu.Lock()
			defer provider.mu.Unlock()
			require.Len(t, provider.preparations, stages+1, "invalid existing bundles must never enter creation or hydration")
			require.Empty(t, provider.preparations[stages].VerifyIntegrity, "warm failures must remain bounded to the local bundle verifier")
		})
	}
}

func TestManagedSharedReviewInputsMissingBundleRetainsStrictCreationGuard(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	reviewInputsGit(t, f.workDir, "config", "http.extraHeader", "AUTHORIZATION: synthetic-unsafe-header")
	provider, tokens := &managedReviewInputsProvider{}, &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
	session, sandbox := reviewInputsSession(t, f.base, f.head), f.sandbox()
	err := orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox)
	require.ErrorContains(t, err, "configuration is unsafe", "absent evidence must not bypass the strict initial configuration guard")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "unsafe configuration must remain fatal")
	require.Zero(t, tokens.calls.Load(), "unsafe initial configuration must fail before acquiring authentication")
	manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
	require.NoFileExists(t, filepath.Join(f.workDir, manifest), "unsafe source must never publish a new bundle")
	provider.mu.Lock()
	defer provider.mu.Unlock()
	require.Len(t, provider.preparations, 2, "missing warm evidence must enter exactly one strict creation attempt")
	require.True(t, provider.preparations[1].GuardConfiguration, "initial creation must retain the authentication configuration guard")
	require.NotEmpty(t, provider.preparations[1].VerifyIntegrity, "initial creation must retain full integrity validation")
}

func TestSharedReviewInputFollowerRetriesExpiredLeaderWithLiveOwnBudget(t *testing.T) {
	t.Parallel()
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged cleanup", true: "uncertain cleanup"}[uncertain], func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				provider := &sharedInputsFlightProvider{entered: make(chan struct{}), release: make(chan struct{}), cleanupEntered: make(chan struct{}), cleanupRelease: make(chan struct{}), cleanupFailure: uncertain}
				orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, logger: zerolog.Nop()}
				session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
				sandbox := &Sandbox{ID: "shared", WorkDir: "/workspace"}
				ownerCtx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(time.Minute))
				ownerResult, followerResult := make(chan error, 1), make(chan error, 1)
				go func() { ownerResult <- orchestrator.ensureCodeReviewInputs(ownerCtx, session, sandbox) }()
				<-provider.entered
				go func() { followerResult <- orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox) }()
				<-provider.cleanupEntered
				close(provider.cleanupRelease)
				require.ErrorIs(t, <-ownerResult, context.DeadlineExceeded, "the expired leader must retain its own terminal deadline")
				err := <-followerResult
				if uncertain {
					require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "uncertain owner cleanup must remain terminal for every follower")
					require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "unconfirmed owner termination must never become an availability retry")
				} else {
					require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "a follower with a live allowance must retry after the shared leader expires")
					require.NotErrorIs(t, err, context.DeadlineExceeded, "a leader's allowance must not terminate its follower's own budget")
					require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "acknowledged cleanup must not become uncertainty")
				}
				require.Equal(t, int64(1), provider.started.Load(), "overlapping callers must still share one preparation owner")
			})
		})
	}
}
