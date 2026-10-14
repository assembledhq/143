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
	command := fmt.Sprintf("python3 -c '%s' '%s'", shellEscapeSingleQuote(readCodeReviewPreparationMarkerScript), shellEscapeSingleQuote(codeReviewInputPreparationMarker(sandbox, identity)))
	var stdout, stderr bytes.Buffer
	exit, err := o.provider.Exec(ctx, sandbox, command, &stdout, &stderr)
	if err != nil {
		return false, codeReviewInputAvailabilityError(ctx, "inspect code review input preparation", err)
	}
	if exit == 42 {
		return true, nil
	}
	if exit != 0 {
		return false, codeReviewInputAvailabilityError(ctx, "inspect code review input preparation", fmt.Errorf("exit=%d", exit))
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
	command := fmt.Sprintf("python3 -c '%s' '%s' '%s' '%s'", shellEscapeSingleQuote(codeReviewPreparationMarkerScript), shellEscapeSingleQuote(codeReviewInputPreparationMarker(sandbox, identity)), shellEscapeSingleQuote(string(encoded)), mode)
	var stderr bytes.Buffer
	exit, err := o.provider.Exec(ctx, sandbox, command, io.Discard, &stderr)
	if err != nil || (exit != 0 && exit != 1) || (mode == "mark" && exit != 0) {
		return exit, fmt.Errorf("update review preparation marker: exit=%d %v %s", exit, err, stderr.String())
	}
	return exit, nil
}

func (o *Orchestrator) clearCodeReviewInputPreparation(ctx context.Context, session *models.Session, sandbox *Sandbox) {
	identity, err := codeReviewInputIdentityFromSession(session)
	if err != nil {
		o.logger.Warn().Err(err).Msg("failed to identify cancelled review input preparation")
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	alive, err := o.provider.IsAlive(cleanupCtx, sandbox)
	if err != nil {
		o.logger.Warn().Err(err).Msg("failed to inspect cancelled review input preparation")
		return
	}
	if !alive {
		return
	}
	if _, err := o.updateCodeReviewPreparationMarker(cleanupCtx, session, sandbox, identity, "clear"); err != nil {
		o.logger.Warn().Err(err).Msg("failed to clear cancelled review input preparation")
	}
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
	manifestPath, _ := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	result := o.reviewInputsGroup.DoChan(sandbox.ID+":"+manifestPath, func() (any, error) {
		prepareCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		repo, err := o.repositories.GetByID(prepareCtx, identity.OrgID, identity.RepositoryID)
		if err != nil {
			return nil, codeReviewInputAvailabilityError(prepareCtx, "load shared review input repository", err)
		}
		exit, err := o.runCodeReviewInputPreparation(prepareCtx, sandbox, identity, session.WorkspaceGeneration, "", "")
		if err != nil || exit != 42 {
			return nil, err
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
		_, err = o.runCodeReviewInputPreparation(prepareCtx, sandbox, identity, session.WorkspaceGeneration, repo.CloneURL, token)
		return nil, err
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-result:
		return result.Err
	}
}

func (o *Orchestrator) runCodeReviewInputPreparation(ctx context.Context, sandbox *Sandbox, identity codeReviewInputIdentity, generation int64, remote, token string) (int, error) {
	encoded, err := json.Marshal(identity)
	if err != nil {
		return -1, fmt.Errorf("encode shared review input identity: %w", err)
	}
	manifestPath, _ := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, identity.BaseSHA, identity.HeadSHA)
	budget := 100.0
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, max(0.1, time.Until(deadline).Seconds()-1))
	}
	owner, err := json.Marshal(codeReviewPreparationOwnerFromContext(ctx, &models.Session{ID: identity.SessionID, OrgID: identity.OrgID, WorkspaceGeneration: generation}, sandbox))
	if err != nil {
		return -1, fmt.Errorf("encode review preparation owner: %w", err)
	}
	command := fmt.Sprintf("cd '%s' && python3 -c '%s' '%s' '%s' '%s' '%f' '%s' '%s'", shellEscapeSingleQuote(sandbox.WorkDir), shellEscapeSingleQuote(prepareCodeReviewInputsScript), shellEscapeSingleQuote(string(encoded)), shellEscapeSingleQuote(path.Dir(manifestPath)), shellEscapeSingleQuote(remote), budget, shellEscapeSingleQuote(codeReviewInputPreparationMarker(sandbox, identity)), shellEscapeSingleQuote(string(owner)))
	var stderr bytes.Buffer
	exit, execErr := o.provider.Exec(ctx, reviewInputFetchSandbox(sandbox, token), command, io.Discard, &stderr)
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

const prepareCodeReviewInputsScript = `import fcntl, hashlib, json, os, pathlib, shutil, subprocess, sys, tempfile, time, urllib.parse
identity = json.loads(sys.argv[1])
directory = pathlib.Path(sys.argv[2])
remote = sys.argv[3]
token = os.environ.get("GH143_REVIEW_FETCH_TOKEN", "")
if token and remote.startswith("https://"):
    remote = "https://x-access-token:" + urllib.parse.quote(token, safe="") + "@" + remote[8:]
deadline = time.monotonic() + float(sys.argv[4])

def remaining():
    seconds = deadline - time.monotonic()
    if seconds <= 0:
        raise TimeoutError("shared review input preparation deadline exceeded")
    return seconds

def git(*args, check=True, stdout=subprocess.PIPE):
    return subprocess.run(["git", *args], stdout=stdout, stderr=subprocess.PIPE, check=check, timeout=min(35, remaining()), env={**os.environ, "GIT_TERMINAL_PROMPT": "0"})

def digest(file):
    with file.open("rb") as stream:
        digest = hashlib.sha256()
        while chunk := stream.read(1024 * 1024):
            remaining()
            digest.update(chunk)
        return digest.hexdigest()

def prepare():
    if git("rev-parse", "HEAD").stdout.decode().strip() != identity["head_sha"]:
        raise ValueError("workspace HEAD differs from captured review head")
    if directory.exists():
        manifest = json.loads((directory / "manifest.json").read_text())
        if any(manifest.get(key) != value for key, value in identity.items()):
            raise ValueError("prepared review input identity mismatch")
        merge_base = git("merge-base", identity["base_sha"], identity["head_sha"]).stdout.decode().strip()
        if manifest.get("merge_base_sha") != merge_base or manifest.get("patch_sha256") != digest(directory / "changes.patch"):
            raise ValueError("prepared review input digest or merge base mismatch")
        return
    for sha in [identity["base_sha"], identity["head_sha"]]:
        if git("cat-file", "-e", sha + "^{commit}", check=False).returncode != 0:
            if not remote:
                sys.exit(42)
            git("fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, sha)
            git("cat-file", "-e", sha + "^{commit}")
    merge = git("merge-base", identity["base_sha"], identity["head_sha"], check=False)
    if merge.returncode != 0:
        if not remote:
            sys.exit(42)
        for deepen in [200, 1000]:
            git("fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--deepen=" + str(deepen), remote, identity["base_sha"], identity["head_sha"])
            merge = git("merge-base", identity["base_sha"], identity["head_sha"], check=False)
            if merge.returncode == 0:
                break
    if merge.returncode != 0:
        raise ValueError("captured commits have no available merge base after bounded history fetch")
    merge_base = merge.stdout.decode().strip()
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
        marker = pathlib.Path(sys.argv[5])
        with pathlib.Path(str(marker) + ".lock").open("a") as marker_lock:
            fcntl.flock(marker_lock, fcntl.LOCK_EX)
            if marker.exists():
                if json.loads(marker.read_text()) != json.loads(sys.argv[6]):
                    raise ValueError("review input preparation ownership changed")
                marker.unlink()
except subprocess.SubprocessError as error:
    print("captured review input Git operation failed (" + type(error).__name__ + ")", file=sys.stderr)
    sys.exit(1)
except (ValueError, OSError) as error:
    print(str(error), file=sys.stderr)
    sys.exit(1)
`
