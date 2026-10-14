package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type codeReviewInputIdentity struct {
	Version      int       `json:"version"`
	OrgID        uuid.UUID `json:"org_id"`
	SessionID    uuid.UUID `json:"session_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Repository   string    `json:"repository"`
	BaseSHA      string    `json:"base_sha"`
	HeadSHA      string    `json:"head_sha"`
}

var reviewCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)
var reviewRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const codeReviewInputCleanupAcknowledgementTimeout = 10 * time.Second

func CodeReviewInputPaths(orgID, sessionID, repositoryID uuid.UUID, repository, baseSHA, headSHA string) (string, string) {
	identity := codeReviewInputIdentity{1, orgID, sessionID, repositoryID, strings.TrimSpace(repository), strings.TrimSpace(baseSHA), strings.TrimSpace(headSHA)}
	encoded := strings.Join([]string{"1", identity.OrgID.String(), identity.SessionID.String(), identity.RepositoryID.String(), identity.Repository, identity.BaseSHA, identity.HeadSHA}, "\x00")
	directory := fmt.Sprintf(".git/143-review/%x", sha256.Sum256([]byte(encoded)))
	return path.Join(directory, "manifest.json"), path.Join(directory, "changes.patch")
}

type CodeReviewInputTarget struct {
	Repository        string
	BaseSHA           string
	HeadSHA           string
	PullRequestNumber int
	ManifestPath      string
	DiffPath          string
}

func CodeReviewInputTargetFromSession(session *models.Session) (CodeReviewInputTarget, error) {
	identity, err := codeReviewInputIdentityFromSession(session)
	if err != nil {
		return CodeReviewInputTarget{}, err
	}
	number, _, _, err := codeReviewCheckoutContextFromSession(session)
	if err != nil {
		return CodeReviewInputTarget{}, err
	}
	manifest, diff := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	return CodeReviewInputTarget{Repository: identity.Repository, BaseSHA: identity.BaseSHA, HeadSHA: identity.HeadSHA, PullRequestNumber: number, ManifestPath: manifest, DiffPath: diff}, nil
}

func codeReviewInputIdentityFromSession(session *models.Session) (codeReviewInputIdentity, error) {
	if _, _, _, err := codeReviewCheckoutContextFromSession(session); err != nil {
		return codeReviewInputIdentity{}, err
	}
	if session == nil || session.Origin != models.SessionOriginCodeReview || session.RepositoryID == nil || *session.RepositoryID == uuid.Nil || session.OrgID == uuid.Nil || session.ID == uuid.Nil {
		return codeReviewInputIdentity{}, fmt.Errorf("code review input identity is incomplete")
	}
	var parsed codeReviewCheckoutContext
	if err := json.Unmarshal(session.RevisionContext, &parsed); err != nil {
		return codeReviewInputIdentity{}, fmt.Errorf("parse code review input identity: %w", err)
	}
	identity := codeReviewInputIdentity{1, session.OrgID, session.ID, *session.RepositoryID, strings.TrimSpace(parsed.GitHubRepo), strings.TrimSpace(parsed.BaseSHA), strings.TrimSpace(parsed.HeadSHA)}
	if !reviewRepositoryPattern.MatchString(identity.Repository) || !reviewCommitPattern.MatchString(identity.BaseSHA) || !reviewCommitPattern.MatchString(identity.HeadSHA) {
		return codeReviewInputIdentity{}, fmt.Errorf("code review input identity requires repository and exact base/head commit SHAs")
	}
	if session.BaseCommitSHA != nil && strings.TrimSpace(*session.BaseCommitSHA) != identity.HeadSHA {
		return codeReviewInputIdentity{}, fmt.Errorf("code review session edit baseline does not match captured head")
	}
	return identity, nil
}

func codeReviewInputPreparationMarker(sandbox *Sandbox, identity codeReviewInputIdentity) string {
	manifest, _ := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	return path.Join(path.Dir(sandbox.WorkDir), ".143-review-preparing-"+path.Base(path.Dir(manifest)))
}

type codeReviewPreparationOwner struct {
	Version             int       `json:"version"`
	OrgID               uuid.UUID `json:"org_id"`
	SessionID           uuid.UUID `json:"session_id"`
	ContainerID         string    `json:"container_id"`
	WorkspaceGeneration int64     `json:"workspace_generation"`
	JobID               uuid.UUID `json:"job_id"`
	LockToken           uuid.UUID `json:"lock_token"`
}

func codeReviewPreparationOwnerFromContext(ctx context.Context, session *models.Session, sandbox *Sandbox) codeReviewPreparationOwner {
	jobID, _ := jobctx.JobIDFromContext(ctx)
	lockToken, _ := jobctx.LockTokenFromContext(ctx)
	return codeReviewPreparationOwner{1, session.OrgID, session.ID, sandbox.ID, session.WorkspaceGeneration, jobID, lockToken}
}

func (o *Orchestrator) markCodeReviewInputsPreparing(ctx context.Context, session *models.Session, sandbox *Sandbox) error {
	if session.Origin != models.SessionOriginCodeReview {
		return nil
	}
	identity, err := codeReviewInputIdentityFromSession(session)
	if err != nil {
		return err
	}
	_, err = o.updateCodeReviewPreparationMarker(ctx, session, sandbox, identity, "mark")
	return err
}

type incompleteCodeReviewWorkspaceRecoverer interface {
	RecoverIncompleteCodeReviewWorkspace(context.Context, uuid.UUID, db.RecoverIncompleteCodeReviewWorkspaceParams) (bool, error)
}

func (o *Orchestrator) codeReviewInputsPreparationFinished(ctx context.Context, session *models.Session, sandbox *Sandbox) (bool, error) {
	identity, err := codeReviewInputIdentityFromSession(session)
	if err != nil {
		return false, err
	}
	command := fmt.Sprintf("/usr/bin/python3 -I -S -c '%s' '%s'", shellEscapeSingleQuote(readCodeReviewPreparationMarkerScript), shellEscapeSingleQuote(codeReviewInputPreparationMarker(sandbox, identity)))
	var stdout, stderr bytes.Buffer
	exit, err := o.execForReviewPreparation(ctx, sandbox, command, &stdout, &stderr)
	if err != nil {
		if errors.Is(err, ErrCodeReviewPreparationCleanup) || errors.Is(err, ErrCodeReviewSourceIntegrity) {
			return false, errors.Join(err, ctx.Err())
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, errReviewPreparationTransport) && !errors.Is(err, ErrCodeReviewPreparationCleanup) {
			return false, codeReviewInputAvailabilityError(ctx, "inspect code review input preparation", err)
		}
		return false, err
	}
	if exit == 42 {
		return true, nil
	}
	if exit != 0 {
		return false, fmt.Errorf("inspect code review input preparation: exit=%d", exit)
	}
	var owner codeReviewPreparationOwner
	if err := json.Unmarshal(stdout.Bytes(), &owner); err != nil || owner.Version != 1 || owner.JobID == uuid.Nil || owner.LockToken == uuid.Nil ||
		owner.OrgID != session.OrgID || owner.SessionID != session.ID || owner.ContainerID != sandbox.ID || owner.WorkspaceGeneration != session.WorkspaceGeneration {
		return false, fmt.Errorf("incomplete review workspace has no verifiable preparation owner; restart the review workspace")
	}
	waitingJobID, hasJob := jobctx.JobIDFromContext(ctx)
	waitingToken, hasToken := jobctx.LockTokenFromContext(ctx)
	recoverer, ok := o.sessions.(incompleteCodeReviewWorkspaceRecoverer)
	if !hasJob || !hasToken || waitingJobID == uuid.Nil || waitingToken == uuid.Nil || !ok {
		return false, fmt.Errorf("review preparation lease proof unavailable; restart the review workspace")
	}
	cleared, err := recoverer.RecoverIncompleteCodeReviewWorkspace(ctx, session.OrgID, db.RecoverIncompleteCodeReviewWorkspaceParams{
		SessionID: session.ID, ContainerID: sandbox.ID, WorkspaceGeneration: owner.WorkspaceGeneration,
		Repository: identity.Repository, BaseSHA: identity.BaseSHA, HeadSHA: identity.HeadSHA,
		PreparationJobID: owner.JobID, PreparationLockToken: owner.LockToken,
		WaitingJobID: waitingJobID, WaitingLockToken: waitingToken,
	})
	if err != nil {
		if errors.Is(err, db.ErrReviewPreparationOwnershipUnavailable) {
			return false, err
		}
		return false, codeReviewInputAvailabilityError(ctx, "prove incomplete review workspace ownership", err)
	}
	if !cleared {
		return false, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := o.provider.Destroy(cleanupCtx, sandbox); err != nil {
		o.logger.Warn().Err(err).Str("container_id", sandbox.ID).Msg("failed to destroy fenced incomplete review workspace")
	}
	return false, ErrStaleSandboxIDCleared
}

func codeReviewInputAvailabilityError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s: %w", ErrSandboxWorkspaceNotReady, operation, err)
}

const readCodeReviewPreparationMarkerScript = `import pathlib, sys
try:
    with pathlib.Path(sys.argv[1]).open() as marker:
        sys.stdout.write(marker.read())
except FileNotFoundError:
    sys.exit(42)
`

func (o *Orchestrator) updateCodeReviewPreparationMarker(ctx context.Context, session *models.Session, sandbox *Sandbox, identity codeReviewInputIdentity, mode string) (int, error) {
	encoded, err := json.Marshal(codeReviewPreparationOwnerFromContext(ctx, session, sandbox))
	if err != nil {
		return -1, fmt.Errorf("encode review preparation owner: %w", err)
	}
	command := fmt.Sprintf("/usr/bin/python3 -I -S -c '%s' '%s' '%s' '%s'", shellEscapeSingleQuote(codeReviewPreparationMarkerScript), shellEscapeSingleQuote(codeReviewInputPreparationMarker(sandbox, identity)), shellEscapeSingleQuote(string(encoded)), mode)
	var stderr bytes.Buffer
	exit, err := o.execForReviewPreparation(ctx, sandbox, command, io.Discard, &stderr)
	if err != nil {
		return exit, fmt.Errorf("update review preparation marker: %w", err)
	}
	if (exit != 0 && exit != 1) || (mode == "mark" && exit != 0) {
		return exit, fmt.Errorf("update review preparation marker: exit=%d %s", exit, stderr.String())
	}
	return exit, nil
}

const codeReviewPreparationMarkerScript = `import fcntl, json, os, pathlib, sys, tempfile
marker, owner, mode = pathlib.Path(sys.argv[1]), json.loads(sys.argv[2]), sys.argv[3]
marker.parent.mkdir(parents=True, exist_ok=True)
with pathlib.Path(str(marker) + ".lock").open("a") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    if mode == "mark":
        descriptor, temporary = tempfile.mkstemp(prefix=marker.name + ".", dir=marker.parent)
        try:
            with os.fdopen(descriptor, "w") as stream:
                json.dump(owner, stream)
            os.replace(temporary, marker)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    elif marker.exists():
        if json.loads(marker.read_text()) != owner:
            sys.exit(1)
        marker.unlink()
`

func (o *Orchestrator) ensureCodeReviewInputs(ctx context.Context, session *models.Session, sandbox *Sandbox) error {
	if session == nil || session.Origin != models.SessionOriginCodeReview {
		return nil
	}
	identity, err := codeReviewInputIdentityFromSession(session)
	if err != nil {
		return err
	}
	if sandbox == nil || sandbox.ID == "" || sandbox.WorkDir == "" || o.provider == nil || o.repositories == nil {
		return fmt.Errorf("code review input preparation dependencies unavailable")
	}
	ctx = withCodeReviewPreparationBudget(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, deadline)
	}
	prepareCtx, cancel := codeReviewPreparationContext(ctx)
	verifyOnly := errors.Is(prepareCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	if verifyOnly {
		cancel()
		prepareCtx, cancel = context.WithTimeout(ctx, CodeReviewWorkspaceVerificationTimeout)
	}
	defer cancel()
	var started atomic.Bool
	manifestPath, _ := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	result := o.reviewInputsGroup.DoChan(sandbox.ID+":"+manifestPath, func() (any, error) {
		started.Store(true)
		repo, err := o.repositories.GetByID(prepareCtx, identity.OrgID, identity.RepositoryID)
		if err != nil {
			return nil, codeReviewInputAvailabilityError(prepareCtx, "load shared review input repository", err)
		}
		exit, err := o.runCodeReviewInputPreparation(prepareCtx, sandbox, identity, repo.CloneURL, "", false, true)
		if verifyOnly {
			if err != nil || exit != 0 {
				return nil, NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageReadiness, errors.Join(context.DeadlineExceeded, err))
			}
			return nil, nil
		}
		if err != nil || exit != 42 {
			if err != nil {
				return nil, err
			}
			return nil, o.finishCodeReviewInputPreparation(prepareCtx, session, sandbox, identity)
		}
		exit, err = o.runCodeReviewInputPreparation(prepareCtx, sandbox, identity, repo.CloneURL, "", false, false)
		if err != nil || exit != 42 {
			if err != nil {
				return nil, err
			}
			return nil, o.finishCodeReviewInputPreparation(prepareCtx, session, sandbox, identity)
		}
		if o.github == nil {
			return nil, fmt.Errorf("code review inputs incomplete: missing commits or merge-base history")
		}
		if strings.TrimSpace(repo.CloneURL) == "" {
			return nil, fmt.Errorf("code review inputs incomplete: repository clone URL unavailable")
		}
		token, err := o.github.GetInstallationToken(prepareCtx, repo.InstallationID)
		if err != nil {
			return nil, fmt.Errorf("authenticate shared review input preparation: %w", err)
		}
		_, err = o.runCodeReviewInputPreparation(prepareCtx, sandbox, identity, repo.CloneURL, token, true, false)
		if err != nil {
			return nil, err
		}
		return nil, o.finishCodeReviewInputPreparation(prepareCtx, session, sandbox, identity)
	})
	select {
	case <-prepareCtx.Done():
		cause := prepareCtx.Err()
		cancel()
		if !started.Load() {
			return cause
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), codeReviewInputCleanupAcknowledgementTimeout)
		defer cleanupCancel()
		select {
		case result := <-result:
			if errors.Is(result.Err, ErrCodeReviewPreparationCleanup) || errors.Is(result.Err, context.DeadlineExceeded) {
				return errors.Join(cause, result.Err)
			}
			return cause
		case <-cleanupCtx.Done():
			return errors.Join(cause, ErrCodeReviewPreparationCleanup)
		}
	case result := <-result:
		if err := prepareCtx.Err(); err != nil {
			if errors.Is(result.Err, ErrCodeReviewPreparationCleanup) || errors.Is(result.Err, context.DeadlineExceeded) {
				return errors.Join(err, result.Err)
			}
			return err
		}
		if !started.Load() {
			if errors.Is(result.Err, ErrCodeReviewPreparationCleanup) || errors.Is(result.Err, ErrCodeReviewSourceIntegrity) || errors.Is(result.Err, ErrStalePullRequestHead) {
				return result.Err
			}
			if errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded) {
				return codeReviewInputAvailabilityError(ctx, "shared review input preparation owner stopped", errors.New("owner preparation allowance ended before shared inputs were ready"))
			}
		}
		return result.Err
	}
}

func (o *Orchestrator) finishCodeReviewInputPreparation(ctx context.Context, session *models.Session, sandbox *Sandbox, identity codeReviewInputIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exit, err := o.updateCodeReviewPreparationMarker(ctx, session, sandbox, identity, "clear")
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("review input preparation ownership changed")
	}
	return nil
}

func (o *Orchestrator) runCodeReviewInputPreparation(ctx context.Context, sandbox *Sandbox, identity codeReviewInputIdentity, repoURL, token string, hydrate, verifyOnly bool) (int, error) {
	if !verifyOnly {
		var cancel context.CancelFunc
		ctx, cancel = codeReviewPreparationContext(ctx)
		defer cancel()
	}
	deadline, err := reviewPreparationDeadlineUnix(ctx)
	if err != nil {
		return -1, err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return -1, fmt.Errorf("encode shared review input identity: %w", err)
	}
	manifestPath, _ := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	_, trusted := o.provider.(reviewPreparationTrustedStdinExecutor)
	_, stdin := o.provider.(reviewPreparationStdinExecutor)
	remote := ""
	if hydrate {
		remote = repoURL
		if trusted || stdin {
			remote = "origin"
		}
	}
	commandFor := func(hashOnly bool) string {
		return fmt.Sprintf("cd '%s' && /usr/bin/python3 -I -S -c '%s' '%s' '%s' '%s' '%f' '%t' '%t' '%t'", shellEscapeSingleQuote(sandbox.WorkDir), shellEscapeSingleQuote(prepareCodeReviewInputsScript), shellEscapeSingleQuote(string(encoded)), shellEscapeSingleQuote(path.Dir(manifestPath)), shellEscapeSingleQuote(remote), deadline, verifyOnly, hashOnly, trusted || stdin)
	}
	command := commandFor(false)
	if trusted || stdin {
		payload := reviewWorkspacePreparationPayload{
			WorkDir: sandbox.WorkDir, Standalone: true, PlatformCommand: true,
			Prepare: command, DeadlineUnix: deadline, CancelPath: reviewPreparationMarkerPath(sandbox),
			AuthenticateGit: hydrate,
		}
		if !verifyOnly {
			payload.GuardConfiguration = repoURL != ""
			payload.ExpectedRepoURL = repoURL
			payload.VerifyIntegrity = reviewGitObjectIntegrityScript(sandbox.WorkDir)
			payload.VerifyCapturedIntegrity = reviewGitObjectIntegrityScript(sandbox.WorkDir) + reviewTreeEvidenceScript(sandbox.WorkDir, identity.BaseSHA, identity.HeadSHA, "")
		} else {
			payload.VerifyCapturedIntegrity = commandFor(true)
		}
		auth := reviewGitAuthentication{}
		if hydrate {
			auth = reviewGitAuthentication{repoURL, token}
		}
		err := o.execReviewPreparationPayload(ctx, sandbox, payload, auth, o.logger)
		if errors.Is(err, errReviewHydrationRequired) && !errors.Is(err, ErrCodeReviewPreparationCleanup) && !hydrate {
			return 42, nil
		}
		if err != nil {
			if errors.Is(err, errReviewPreparationTransport) && !errors.Is(err, ErrCodeReviewPreparationCleanup) {
				return -1, codeReviewInputAvailabilityError(ctx, "prepare shared review inputs", err)
			}
			return -1, err
		}
		return 0, nil
	}
	if _, managed := o.provider.(reviewPreparationExecutor); managed {
		return -1, fmt.Errorf("review provider advertises managed preparation without private input support")
	}
	var stderr bytes.Buffer
	exit, execErr := o.provider.Exec(ctx, reviewInputFetchSandbox(sandbox, token), command, io.Discard, &stderr)
	if exit == 77 {
		return exit, NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageReadiness, errors.Join(ErrCodeReviewPreparationCleanup, ctx.Err()))
	}
	if execErr != nil {
		return exit, codeReviewInputAvailabilityError(ctx, "prepare shared review inputs", errors.New(redactGitToken(execErr.Error(), token)))
	}
	if exit != 0 && (exit != 42 || remote != "") {
		return exit, fmt.Errorf("code review inputs incomplete: exit=%d %s", exit, redactGitToken(stderr.String(), token))
	}
	return exit, nil
}

func (o *Orchestrator) checkoutCodeReviewHead(ctx context.Context, sandbox *Sandbox, repoURL, token string, pullNumber int, branch, head string) error {
	if !reviewCommitPattern.MatchString(head) || branch == "" {
		return fmt.Errorf("code review checkout requires an exact captured head and working branch")
	}
	checkoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := fmt.Sprintf("git cat-file -e '%s^{commit}'", head)
	exit, err := o.provider.Exec(checkoutCtx, sandbox, command, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("inspect captured review head: %w", err)
	}
	if exit != 0 {
		command = codeReviewFetchCommand(repoURL, head)
		var stderr bytes.Buffer
		exit, err = o.provider.Exec(checkoutCtx, reviewInputFetchSandbox(sandbox, token), command, io.Discard, &stderr)
		if err != nil {
			return fmt.Errorf("fetch captured review head: %s", redactGitToken(err.Error(), token))
		}
		if exit != 0 {
			stderr.Reset()
			command = codeReviewFetchCommand(repoURL, fmt.Sprintf("pull/%d/head", pullNumber))
			exit, err = o.provider.Exec(checkoutCtx, reviewInputFetchSandbox(sandbox, token), command, io.Discard, &stderr)
			if err != nil || exit != 0 {
				return fmt.Errorf("captured review head unavailable: exit=%d %s %s", exit, redactGitToken(fmt.Sprint(err), token), redactGitToken(stderr.String(), token))
			}
		}
	}
	command = fmt.Sprintf("git checkout -B '%s' '%s'", shellEscapeSingleQuote(branch), head)
	var stderr bytes.Buffer
	exit, err = o.provider.Exec(checkoutCtx, sandbox, command, io.Discard, &stderr)
	if err != nil || exit != 0 {
		return fmt.Errorf("checkout captured review head: exit=%d %v %s", exit, err, stderr.String())
	}
	var stdout bytes.Buffer
	stderr.Reset()
	exit, err = o.provider.Exec(checkoutCtx, sandbox, "git rev-parse HEAD", &stdout, &stderr)
	if err != nil || exit != 0 {
		return fmt.Errorf("verify captured review head: exit=%d %v %s", exit, err, stderr.String())
	}
	if actual := strings.TrimSpace(stdout.String()); actual != head {
		return fmt.Errorf("%w: expected %s, got %s", ErrStalePullRequestHead, head, actual)
	}
	return nil
}

func reviewInputFetchSandbox(sandbox *Sandbox, token string) *Sandbox {
	if token == "" {
		return sandbox
	}
	copy := *sandbox
	copy.Env = cloneStringMap(sandbox.Env)
	if copy.Env == nil {
		copy.Env = make(map[string]string)
	}
	copy.Env["GH143_REVIEW_FETCH_TOKEN"] = token
	return &copy
}

func codeReviewFetchCommand(repositoryURL, ref string) string {
	return fmt.Sprintf("python3 -c '%s' '%s' '%s'", shellEscapeSingleQuote(codeReviewFetchScript), shellEscapeSingleQuote(repositoryURL), shellEscapeSingleQuote(ref))
}

const codeReviewFetchScript = `import os, subprocess, sys, urllib.parse
remote, ref = sys.argv[1:]
token = os.environ.get("GH143_REVIEW_FETCH_TOKEN", "")
if token and remote.startswith("https://"):
    remote = "https://x-access-token:" + urllib.parse.quote(token, safe="") + "@" + remote[8:]
try:
    result = subprocess.run(["git", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, ref], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=35, env={**os.environ, "GIT_TERMINAL_PROMPT": "0"})
    sys.exit(result.returncode)
except subprocess.SubprocessError:
    print("captured review commit fetch timed out", file=sys.stderr)
    sys.exit(1)
`

const prepareCodeReviewInputsScript = `import fcntl, hashlib, json, os, pathlib, select, shutil, stat, subprocess, sys, tempfile, time, urllib.parse
identity = json.loads(sys.argv[1])
directory = pathlib.Path(sys.argv[2])
remote = sys.argv[3]
token = os.environ.get("GH143_REVIEW_FETCH_TOKEN", "")
if token and remote.startswith("https://"):
    remote = "https://x-access-token:" + urllib.parse.quote(token, safe="") + "@" + remote[8:]
deadline = time.monotonic() + max(0, float(sys.argv[4]) - time.time())
verify_only = sys.argv[5] == "true"
hash_only = sys.argv[6] == "true"
managed_verification = sys.argv[7] == "true"
fetch = ["fetch", "--quiet", "--no-tags", "--no-write-fetch-head"]
if remote == "origin":
    fetch.append("--filter=blob:none")

def remaining():
    seconds = deadline - time.monotonic()
    if seconds <= 0:
        raise TimeoutError("shared review input preparation deadline exceeded")
    return seconds

def git_environment():
    environment = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_NO_LAZY_FETCH": "1"}
    if not remote:
        environment["GIT_ALLOW_PROTOCOL"] = ""
    return environment

def git_command(*args):
    return ["/usr/bin/git", "-c", "core.commitGraph=false", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "remote.origin.promisor=false", *args]

def git(*args, check=True, stdout=subprocess.PIPE, input=None):
    return subprocess.run(git_command(*args), input=input, stdout=stdout, stderr=subprocess.PIPE, check=check, timeout=remaining(), env=git_environment())

class ObjectReader:
    def __init__(self, stream):
        self.fd = stream.fileno()
        self.buffer = bytearray()

    def receive(self, size):
        if not select.select([self.fd], [], [], remaining())[0]:
            raise TimeoutError("captured review object verification timed out")
        return os.read(self.fd, size)

    def line(self):
        while b"\n" not in self.buffer:
            if len(self.buffer) > 128:
                raise ValueError("captured review object header is invalid")
            chunk = self.receive(4096)
            if not chunk:
                if self.buffer:
                    raise ValueError("captured review object header is incomplete")
                return None
            self.buffer.extend(chunk)
        end = self.buffer.index(b"\n")
        if end > 128:
            raise ValueError("captured review object header is invalid")
        line = bytes(self.buffer[:end])
        del self.buffer[:end + 1]
        return line

    def chunk(self, size):
        if self.buffer:
            count = min(size, len(self.buffer))
            data = bytes(self.buffer[:count])
            del self.buffer[:count]
        else:
            data = self.receive(min(size, 1024 * 1024))
        if not data:
            raise ValueError("captured review object body is incomplete")
        return data

class ObjectCleanupError(RuntimeError):
    def __init__(self, failures):
        super().__init__("captured review object process cleanup could not be confirmed")
        self.failures = failures

def valid_oid(value):
    return len(value) in (40, 64) and all(byte in b"0123456789abcdef" for byte in value)

def verify_captured_objects(merge_base):
    if not isinstance(merge_base, str) or not valid_oid(merge_base.encode()):
        raise ValueError("captured review merge base is invalid")
    processes = []
    try:
        objects = subprocess.Popen(git_command("rev-list", "--objects", "--no-object-names", "--missing=print", "--no-walk", identity["base_sha"], identity["head_sha"], merge_base), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=git_environment(), bufsize=0)
        processes.append(objects)
        batch = subprocess.Popen(git_command("cat-file", "--batch"), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=git_environment(), bufsize=0)
        processes.append(batch)
        os.set_blocking(batch.stdin.fileno(), False)
        names, contents = ObjectReader(objects.stdout), ObjectReader(batch.stdout)
        while (oid := names.line()) is not None:
            if not valid_oid(oid):
                raise ValueError("captured review object is missing or invalid")
            request = oid + b"\n"
            while request:
                if not select.select([], [batch.stdin.fileno()], [], remaining())[1]:
                    raise TimeoutError("captured review object verification timed out")
                request = request[os.write(batch.stdin.fileno(), request):]
            header = contents.line()
            fields = header.split() if header is not None else []
            if len(fields) != 3 or fields[0] != oid or fields[1] not in (b"commit", b"tree", b"blob") or not fields[2].isdigit():
                raise ValueError("captured review object is missing or invalid")
            size = int(fields[2])
            digest = hashlib.sha1() if len(oid) == 40 else hashlib.sha256()
            digest.update(fields[1] + b" " + fields[2] + b"\0")
            while size:
                remaining()
                data = contents.chunk(size)
                digest.update(data)
                size -= len(data)
            if contents.chunk(1) != b"\n" or digest.hexdigest().encode() != oid:
                raise ValueError("captured review source object hash mismatch")
        if objects.wait(timeout=remaining()) != 0:
            raise ValueError("captured review object closure is incomplete")
        batch.stdin.close()
        if batch.wait(timeout=remaining()) != 0:
            raise ValueError("captured review object verification failed")
    finally:
        original = sys.exc_info()[1]
        failures = []
        for process in processes:
            try:
                if process.poll() is None:
                    process.kill()
            except Exception as error:
                failures.append(error)
            try:
                process.wait(timeout=3)
            except Exception as error:
                failures.append(error)
            for stream in (process.stdin, process.stdout):
                try:
                    if stream is not None:
                        stream.close()
                except Exception as error:
                    failures.append(error)
        if failures:
            raise ObjectCleanupError(failures) from original

def digest(file):
    with file.open("rb") as stream:
        digest = hashlib.sha256()
        while chunk := stream.read(1024 * 1024):
            remaining()
            digest.update(chunk)
        return digest.hexdigest()

def missing_objects(merge_base):
    objects = git("rev-list", "--objects", "--no-object-names", "--missing=print", "--no-walk", identity["base_sha"], identity["head_sha"], merge_base).stdout.decode().splitlines()
    return [line[1:] for line in objects if line.startswith("?")]

def prepare():
    if verify_only and not os.path.lexists(directory):
        sys.exit(42)
    if os.path.lexists(directory):
        for ancestor in [pathlib.Path(".git"), directory.parent, directory]:
            if not stat.S_ISDIR(ancestor.lstat().st_mode):
                raise ValueError("prepared review input directory must be local and regular")
        for file in [directory / "manifest.json", directory / "changes.patch"]:
            if not stat.S_ISREG(file.lstat().st_mode):
                raise ValueError("prepared review input evidence must be regular")
    if git("rev-parse", "HEAD").stdout.decode().strip() != identity["head_sha"]:
        raise ValueError("workspace HEAD differs from captured review head")
    if directory.exists():
        manifest = json.loads((directory / "manifest.json").read_text())
        if any(manifest.get(key) != value for key, value in identity.items()):
            raise ValueError("prepared review input identity mismatch")
        merge_base = git("merge-base", identity["base_sha"], identity["head_sha"]).stdout.decode().strip()
        if manifest.get("merge_base_sha") != merge_base or manifest.get("patch_sha256") != digest(directory / "changes.patch"):
            raise ValueError("prepared review input digest or merge base mismatch")
        if verify_only and not managed_verification:
            verify_captured_objects(merge_base)
        return
    for sha in [identity["base_sha"], identity["head_sha"]]:
        if git("cat-file", "-e", sha + "^{commit}", check=False).returncode != 0:
            if not remote:
                sys.exit(42)
            git(*fetch, remote, sha)
            git("cat-file", "-e", sha + "^{commit}")
    merge = git("merge-base", identity["base_sha"], identity["head_sha"], check=False)
    if merge.returncode != 0:
        if not remote:
            sys.exit(42)
        for deepen in [200, 1000]:
            git(*fetch, "--deepen=" + str(deepen), remote, identity["base_sha"], identity["head_sha"])
            merge = git("merge-base", identity["base_sha"], identity["head_sha"], check=False)
            if merge.returncode == 0:
                break
    if merge.returncode != 0:
        raise ValueError("captured commits have no available merge base after bounded history fetch")
    merge_base = merge.stdout.decode().strip()
    missing = missing_objects(merge_base)
    if missing:
        if not remote:
            sys.exit(42)
        git("-c", "fetch.negotiationAlgorithm=noop", *fetch, "--stdin", remote, input=("\n".join(missing) + "\n").encode())
        if missing_objects(merge_base):
            raise ValueError("captured review trees remain incomplete after authenticated hydration")
    temporary = pathlib.Path(tempfile.mkdtemp(prefix=directory.name + ".tmp-", dir=directory.parent))
    try:
        patch = temporary / "changes.patch"
        with patch.open("wb") as stream:
            git("diff", "--no-ext-diff", "--no-textconv", "--binary", merge_base, identity["head_sha"], "--", stdout=stream)
        manifest = {**identity, "merge_base_sha": merge_base, "patch_sha256": digest(patch)}
        (temporary / "manifest.json").write_text(json.dumps(manifest, sort_keys=True) + "\n")
        for file in temporary.iterdir():
            file.chmod(0o444)
        temporary.chmod(0o555)
        temporary.rename(directory)
    finally:
        if temporary.exists():
            temporary.chmod(0o700)
            shutil.rmtree(temporary)

try:
    if hash_only:
        manifest = json.loads((directory / "manifest.json").read_text())
        if any(manifest.get(key) != value for key, value in identity.items()):
            raise ValueError("prepared review input identity mismatch")
        verify_captured_objects(manifest.get("merge_base_sha"))
    elif verify_only:
        prepare()
    else:
        directory.parent.mkdir(parents=True, exist_ok=True)
        with (directory.parent / (directory.name + ".lock")).open("a") as lock:
            while True:
                remaining()
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    time.sleep(min(0.02, remaining()))
            prepare()
except ObjectCleanupError:
    print("captured review object process cleanup could not be confirmed", file=sys.stderr)
    sys.exit(77)
except (TimeoutError, subprocess.TimeoutExpired):
    print("captured review input verification timed out", file=sys.stderr)
    sys.exit(1)
except subprocess.SubprocessError as error:
    print("captured review input Git operation failed (" + type(error).__name__ + ")", file=sys.stderr)
    sys.exit(125 if hash_only else 1)
except (ValueError, OSError) as error:
    print(str(error), file=sys.stderr)
    sys.exit(125 if hash_only else 1)
`
