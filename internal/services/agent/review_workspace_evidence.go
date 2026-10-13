package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/sandboxdeps"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var regexpGitObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

var errReviewHydrationRequired = errors.New("pinned review source requires authenticated hydration")

// This optional transport keeps private authentication off process arguments,
// provider command logs, and the general repository preparation payload.
type reviewPreparationStdinExecutor interface {
	ExecWithStdin(context.Context, *Sandbox, string, io.Reader, io.Writer, io.Writer) (int, error)
}

type reviewPreparationTrustedStdinExecutor interface {
	ExecWithStdinForReviewPreparation(context.Context, *Sandbox, string, io.Reader, io.Writer, io.Writer) (int, error)
}

type reviewPreparationEnvironmentValidator interface {
	ValidateReviewPreparationEnvironment(context.Context, *Sandbox) error
}

type reviewGitAuthentication struct {
	RepoURL string `json:"repo_url"`
	Token   string `json:"token"`
}

func (auth reviewGitAuthentication) validate() error {
	parsed, err := url.Parse(auth.RepoURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "file") {
		return errors.New("review Git authentication requires a credential-free repository URL")
	}
	return nil
}

func redactReviewGitAuthentication(value, token string) string {
	if token == "" {
		return value
	}
	value = strings.ReplaceAll(value, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "***")
	return redactGitToken(value, token)
}

// A legacy container's creation environment cannot be erased by an exec
// override. Fail closed so its owning lifecycle can replace that workspace.
// Empty explicit overrides also prevent Docker from inheriting these keys.
func reviewPreparationSandbox(sandbox *Sandbox) (*Sandbox, error) {
	clean := *sandbox
	clean.Env = make(map[string]string, len(sandbox.Env)+2)
	for name, value := range sandbox.Env {
		if (name == "GITHUB_TOKEN" || name == "GH_TOKEN") && value != "" {
			return nil, errors.New("review workspace contains legacy GitHub credentials and must be recreated")
		}
		if strings.HasPrefix(name, "GIT_CONFIG_") || strings.HasPrefix(name, "GIT_TRACE") || name == "GIT_CURL_VERBOSE" || name == "GIT_EXEC_PATH" {
			value = ""
		}
		clean.Env[name] = value
	}
	clean.Env["GITHUB_TOKEN"], clean.Env["GH_TOKEN"] = "", ""
	clean.Env["GIT_NO_REPLACE_OBJECTS"], clean.Env["GIT_GRAFT_FILE"] = "1", "/dev/null"
	clean.Env["GIT_CONFIG_GLOBAL"], clean.Env["GIT_CONFIG_SYSTEM"], clean.Env["GIT_CONFIG_COUNT"] = "/dev/null", "/dev/null", "0"
	clean.Env["GIT_DIR"], clean.Env["GIT_COMMON_DIR"] = path.Join(sandbox.WorkDir, ".git"), path.Join(sandbox.WorkDir, ".git")
	clean.Env["GIT_WORK_TREE"], clean.Env["GIT_OBJECT_DIRECTORY"] = sandbox.WorkDir, path.Join(sandbox.WorkDir, ".git/objects")
	clean.Env["GIT_INDEX_FILE"] = path.Join(sandbox.WorkDir, ".git/index")
	clean.Env["GIT_ALTERNATE_OBJECT_DIRECTORIES"], clean.Env["GIT_NAMESPACE"] = "", ""
	return &clean, nil
}

func reviewPreparationMarkerPath(sandbox *Sandbox) string {
	directory := sandbox.HomeDir
	if directory == "" {
		directory = path.Dir(sandbox.WorkDir)
	}
	return path.Join(directory, "143-review-cancel-"+uuid.NewString())
}

func (o *Orchestrator) validateCodeReviewGitConfiguration(ctx context.Context, sandbox *Sandbox, repoURL string) error {
	if err := (reviewGitAuthentication{RepoURL: repoURL}).validate(); err != nil {
		return err
	}
	guardCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(guardCtx)
	if err != nil {
		return err
	}
	return o.execReviewPreparationPayload(guardCtx, sandbox, reviewWorkspacePreparationPayload{
		WorkDir: sandbox.WorkDir, ExpectedRepoURL: repoURL, GuardConfiguration: true,
		Standalone: true, Prepare: "true", DeadlineUnix: deadline, CancelPath: reviewPreparationMarkerPath(sandbox),
	}, reviewGitAuthentication{}, o.logger)
}

func (o *Orchestrator) runSandboxGitBootstrapForSession(ctx context.Context, session *models.Session, sandbox *Sandbox, workDir, repoURL string, log zerolog.Logger) error {
	if session.Origin != models.SessionOriginCodeReview {
		o.runSandboxGitBootstrap(ctx, sandbox, workDir, log)
		return nil
	}
	if err := (reviewGitAuthentication{RepoURL: repoURL}).validate(); err != nil {
		return NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageDependencySetup, err)
	}
	bootstrapCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(bootstrapCtx)
	if err != nil {
		return NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageDependencySetup, err)
	}
	err = o.execReviewPreparationPayload(bootstrapCtx, sandbox, reviewWorkspacePreparationPayload{
		WorkDir: workDir, ExpectedRepoURL: repoURL, GuardConfiguration: true, PlatformCommand: true,
		Standalone: true, Prepare: "/usr/local/bin/143-tools git-bootstrap --workdir='" + shellEscapeSingleQuote(workDir) + "'", DeadlineUnix: deadline, CancelPath: reviewPreparationMarkerPath(sandbox),
	}, reviewGitAuthentication{}, log)
	return NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageDependencySetup, err)
}

func (o *Orchestrator) prepareRepositoryForContinueSession(ctx context.Context, session *models.Session, sandbox *Sandbox, mode repositoryPreparationMode, log zerolog.Logger) error {
	if session.Origin == models.SessionOriginCodeReview {
		return o.ensureCodeReviewWorkspaceEvidence(ctx, session, sandbox, log)
	}
	return prepareSandboxRepositoryForSession(ctx, o.provider, session, sandbox, sandbox.WorkDir, mode, log)
}

type reviewWorkspaceEvidenceReceipt struct {
	Version    int    `json:"version"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
	ConfigHash string `json:"config_hash"`
}

func reviewWorkspaceRevision(session *models.Session) (codeReviewCheckoutContext, error) {
	if _, _, ok, err := codeReviewCheckoutContextFromSession(session); err != nil || !ok {
		if err != nil {
			return codeReviewCheckoutContext{}, err
		}
		return codeReviewCheckoutContext{}, fmt.Errorf("review evidence requires a code review session")
	}
	var revision codeReviewCheckoutContext
	if err := json.Unmarshal(session.RevisionContext, &revision); err != nil {
		return revision, fmt.Errorf("parse review evidence revision: %w", err)
	}
	revision.BaseSHA = strings.TrimSpace(revision.BaseSHA)
	revision.HeadSHA = strings.TrimSpace(revision.HeadSHA)
	if revision.BaseSHA == "" {
		return revision, fmt.Errorf("code review revision context is missing pinned base SHA")
	}
	// Pinning is meaningful only for a complete object ID, never a branch,
	// ref expression, or abbreviated hash that can resolve differently later.
	for _, sha := range []string{revision.BaseSHA, revision.HeadSHA} {
		if !regexpGitObjectID.MatchString(sha) {
			return revision, fmt.Errorf("code review evidence requires complete pinned Git object IDs")
		}
	}
	return revision, nil
}

// reviewTreeEvidenceScript enumerates only the pinned base/head and their
// comparison merge-base, including surrounding implementations and deleted
// files. It deliberately does not download blobs from the remaining history.
// Every verification disables promisor lazy-fetch: success proves the content
// is local even when the per-turn credential socket has been closed.
func reviewTreeEvidenceScript(workDir, base, head, remote string) string {
	quote := func(value string) string { return "'" + shellEscapeSingleQuote(value) + "'" }
	git := "/usr/bin/git -c core.commitGraph=false -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0"
	script := "set -eu\ncd " + quote(workDir) + "\nexport GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null\n"
	if remote != "" {
		script += reviewGitAuthenticationConfigScript(workDir, remote)
		script += git + " fetch --quiet --no-tags --no-write-fetch-head --filter=blob:none origin " + quote(base) + " " + quote(head) + "\n"
	}
	script += "comparison=$(" + git + " merge-base " + quote(base) + " " + quote(head) + ")\n"
	script += "evidence=$(/usr/bin/mktemp -d)\ntrap '/bin/rm -rf \"$evidence\"' EXIT HUP INT TERM\n"
	// --missing=print suppresses lazy fetching even on older sandbox Git
	// versions without GIT_NO_LAZY_FETCH. --no-walk restricts object traversal
	// to these three trees rather than all historical commits.
	probe := git + " rev-list --objects --no-object-names --missing=print --no-walk " + quote(base) + " " + quote(head) + " \"$comparison\" > \"$evidence/objects\"\n"
	script += probe
	if remote != "" {
		script += "/usr/bin/awk '/^\\?/ { print substr($0, 2) }' \"$evidence/objects\" > \"$evidence/missing\"\n"
		// Explicit raw-OID fetches use server-held, short-lived authentication.
		// No remote URL or helper configuration is written to .git/config.
		script += "if test -s \"$evidence/missing\"; then\n" + reviewGitAuthenticationConfigScript(workDir, remote) + "  " + git + " fetch --quiet --no-tags --no-write-fetch-head --filter=blob:none --stdin origin < \"$evidence/missing\"\nfi\n"
		script += probe
	}
	script += "/usr/bin/awk '/^\\?/ { missing = 1 } END { exit missing }' \"$evidence/objects\"\n"
	return script
}

// Full fsck hashes the available objects and checks their pack/index metadata.
// Promisor repositories can legitimately omit unrelated historical objects, so
// the independent pinned-tree closure probe is still required. Rootless full
// fsck validates all available objects before any captured graph/tree readers.
func reviewGitObjectIntegrityScript(workDir string) string {
	quote := func(value string) string { return "'" + shellEscapeSingleQuote(value) + "'" }
	git := "/usr/bin/git -c core.commitGraph=false -c core.multiPackIndex=true -c core.hooksPath=/dev/null -c core.fsmonitor=false -c credential.helper= -c maintenance.auto=false -c gc.auto=0"
	script := "set -eu\ncd " + quote(workDir) + "\nexport GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null\n"
	return script + "if ! " + git + " fsck --full --no-reflogs --no-dangling --no-progress >/dev/null 2>/dev/null; then exit 125; fi\n"
}

func reviewPinnedCheckoutVerificationScript(workDir, base, head string) string {
	// A worktree diff can invoke configured clean/process filters, fsmonitor,
	// index-change hooks, external diffs, or promisor fetches. Verification executes
	// no repository-provided commands, including after the setup budget expires.
	// Probe local objects before diff for older Git versions that do not honor
	// GIT_NO_LAZY_FETCH, and override all executable filters without evaluating
	// their names or values in a shell. A fresh owned index verifies Git tracked
	// contents/modes without trusting mutable index flags or stat caches. Tracked
	// .gitattributes conversions retain Git's normal semantics.
	script := reviewGitObjectIntegrityScript(workDir)
	script += reviewTreeEvidenceScript(workDir, base, head, "")
	script += "test \"$(/usr/bin/git -c core.commitGraph=false -c core.hooksPath=/dev/null rev-parse HEAD)\" = '" + shellEscapeSingleQuote(head) + "'\n"
	script += "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(reviewGitWorktreeVerificationPython) + "' '" + shellEscapeSingleQuote(head) + "'\n"
	return script
}

const reviewGitWorktreeVerificationPython = `
import os, subprocess, sys, tempfile
args = ["/usr/bin/git", "-c", "core.commitGraph=false", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.filemode=true", "-c", "core.symlinks=true"]
configured = subprocess.run(args + ["config", "--null", "--name-only", "--get-regexp", r"^filter\..*\.(clean|process)$"], stdout=subprocess.PIPE)
if configured.returncode not in (0, 1):
    sys.exit(configured.returncode)
for raw_key in configured.stdout.split(b"\0"):
    if raw_key:
        key = os.fsdecode(raw_key)
        args.extend(["-c", key + "=", "-c", key.rsplit(".", 1)[0] + ".required=false"])
with tempfile.TemporaryDirectory(prefix="143-review-index-") as owned:
    environment = dict(os.environ)
    environment["GIT_INDEX_FILE"] = os.path.join(owned, "index")
    loaded = subprocess.run(args + ["read-tree", "--no-sparse-checkout", sys.argv[1]], env=environment)
    if loaded.returncode:
        sys.exit(loaded.returncode)
    sys.exit(subprocess.run(args + ["diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--quiet", sys.argv[1], "--"], env=environment).returncode)
`

// Authentication accepts only the configuration produced by clone and the
// platform's git-bootstrap. Parse the literal file without includes from a
// neutral directory: repository discovery, included/worktree configuration,
// transport helpers and URL-scoped proxy/TLS settings must not receive auth.
const reviewGitAuthenticationConfigPython = `
import json, os, pathlib, stat, subprocess, sys
settings = json.loads(sys.argv[1])
root = pathlib.Path(settings["work_dir"])
gitdir = root / ".git"
def reject():
    raise RuntimeError("review Git configuration is unsafe for authenticated preparation; workspace must be recreated")
if not stat.S_ISDIR(os.lstat(root).st_mode) or not stat.S_ISDIR(os.lstat(gitdir).st_mode):
    reject()
for relative in ("objects", "objects/info", "objects/pack", "info", "refs", "refs/heads", "refs/remotes", "refs/remotes/origin"):
    target = gitdir / relative
    if os.path.lexists(target) and not stat.S_ISDIR(os.lstat(target).st_mode):
        reject()
for relative in ("commondir", "config.worktree", "objects/info/alternates", "objects/info/http-alternates", "objects/info/commit-graph", "objects/info/commit-graphs", "info/grafts", "info/attributes", "refs/replace"):
    if os.path.lexists(gitdir / relative):
        reject()
for relative in ("config", "objects"):
    target = gitdir / relative
    mode = os.lstat(target).st_mode
    if relative == "config" and (not stat.S_ISREG(mode) or target.stat().st_size > 65536):
        reject()
    if relative == "objects" and not stat.S_ISDIR(mode):
        reject()
for relative in ("143-review-preparation.lock", "143-review-evidence.json"):
    target = gitdir / relative
    if os.path.lexists(target) and not stat.S_ISREG(os.lstat(target).st_mode):
        reject()
packed = gitdir / "packed-refs"
if os.path.lexists(packed):
    if not stat.S_ISREG(os.lstat(packed).st_mode) or packed.stat().st_size > 16 * 1024 * 1024:
        reject()
    with packed.open("rb") as saved:
        for line in saved:
            if len(line.split()) == 2 and line.split()[1].startswith(b"refs/replace/"):
                reject()
environment = {name: value for name, value in os.environ.items() if not name.startswith("GIT_")}
environment.update({"PATH": "/usr/bin:/bin", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"})
result = subprocess.Popen(["/usr/bin/git", "config", "--file", str(gitdir / "config"), "--no-includes", "--null", "--list"], cwd="/", env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, close_fds=True)
listing = result.stdout.read(1024 * 1024 + 1)
if len(listing) > 1024 * 1024:
    result.kill()
    result.wait()
    reject()
if result.wait():
    reject()
allowed = {
    "core.repositoryformatversion": {"0", "1"},
    "core.filemode": {"true", "false"},
    "core.bare": {"false"},
    "core.logallrefupdates": {"true"},
    "core.ignorecase": {"true", "false"},
    "core.precomposeunicode": {"true", "false"},
    "remote.origin.url": {settings["repo_url"]},
    "remote.origin.fetch": {"+refs/heads/*:refs/remotes/origin/*"},
    "remote.origin.promisor": {"true"},
    "remote.origin.partialclonefilter": {"blob:none"},
    "extensions.partialclone": {"origin"},
    "extensions.objectformat": {"sha1", "sha256"},
    "push.autosetupremote": {"true"},
    "credential.helper": {"!143-tools git-credential"},
}
def valid_branch(value):
    return value and not value.startswith(("-", "/")) and not value.endswith(("/", ".")) and not any(part.startswith(".") or part.endswith(".lock") for part in value.split("/")) and not any(character.isspace() or ord(character) < 32 or ord(character) == 127 or character in "~^:?*[\\" for character in value) and not any(fragment in value for fragment in ("..", "//", "@{"))
seen = {}
for record in listing.split(b"\0"):
    if not record:
        continue
    raw_key, separator, raw_value = record.partition(b"\n")
    key, value = os.fsdecode(raw_key), os.fsdecode(raw_value)
    if not separator or key in seen or len(seen) >= 128:
        reject()
    seen[key] = value
    if key in allowed:
        if value not in allowed[key]:
            reject()
    elif key in {"user.name", "user.email"}:
        if not value or len(value.encode()) > 512 or any(ord(character) < 32 or ord(character) == 127 for character in value):
            reject()
    elif key.startswith("branch."):
        branch, separator, terminal = key[7:].rpartition(".")
        if not separator or not valid_branch(branch) or terminal not in {"remote", "merge"} or value != ("origin" if terminal == "remote" else "refs/heads/" + branch):
            reject()
    else:
        reject()
if seen.get("remote.origin.url") != settings["repo_url"]:
    reject()
for key in seen:
    if key.startswith("branch.") and key.endswith(".remote") and seen.get(key[:-6] + "merge") != "refs/heads/" + key[7:-7]:
        reject()
    if key.startswith("branch.") and key.endswith(".merge") and seen.get(key[:-5] + "remote") != "origin":
        reject()
`

// The handoff probe reads only platform-created ref files. Git discovery can
// read includes before evidence preparation rejects them, so it cannot serve
// as this pre-evidence readiness check.
const reviewWorkspaceReadyProbePython = `
import json, os, pathlib, re, stat, sys
settings = json.loads(sys.argv[1])
gitdir = pathlib.Path(settings["work_dir"]) / ".git"
if not stat.S_ISDIR(os.lstat(pathlib.Path(settings["work_dir"])).st_mode) or not stat.S_ISDIR(os.lstat(gitdir).st_mode):
    raise RuntimeError("review readiness requires literal repository metadata")
def read_regular(target, limit):
    current = gitdir
    relative = target.relative_to(gitdir)
    for part in relative.parts[:-1]:
        current = current / part
        if not stat.S_ISDIR(os.lstat(current).st_mode):
            raise RuntimeError("review readiness rejects linked metadata")
    descriptor = os.open(target, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise RuntimeError("review readiness requires bounded regular ref files")
        return os.read(descriptor, limit + 1).decode("ascii").strip()
    finally:
        os.close(descriptor)
def valid_ref(ref):
    return ref.startswith("refs/heads/") and ref != "refs/heads/" and not any(part in ("", ".", "..") for part in ref.split("/")) and not any(character.isspace() or ord(character) < 32 or character in "~^:?*[\\" for character in ref) and not any(fragment in ref for fragment in ("..", "@{"))
def resolve(ref):
    if not valid_ref(ref):
        raise RuntimeError("review readiness rejects unsafe ref paths")
    target = gitdir / ref
    try:
        value = read_regular(target, 1024)
    except FileNotFoundError:
        value = ""
        for line in read_regular(gitdir / "packed-refs", 16 * 1024 * 1024).splitlines():
            fields = line.split()
            if len(fields) == 2 and fields[1] == ref:
                value = fields[0]
                break
    if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", value):
        raise RuntimeError("review readiness requires a full pinned ref")
    return value
head = read_regular(gitdir / "HEAD", 1024)
branch = ""
if head.startswith("ref: "):
    ref = head[5:]
    head = resolve(ref)
    branch = ref[len("refs/heads/"):]
elif not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", head):
    raise RuntimeError("review readiness requires a full pinned HEAD")
prepared = resolve("refs/heads/" + settings["expected_branch"]) if settings["expected_branch"] else None
print(head)
print(branch)
if prepared is not None:
    print(prepared)
`

func reviewWorkspaceReadyProbeCommand(workDir, expectedBranch string) (string, error) {
	settings, err := json.Marshal(map[string]string{"work_dir": workDir, "expected_branch": expectedBranch})
	if err != nil {
		return "", err
	}
	return "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(reviewWorkspaceReadyProbePython) + "' '" + shellEscapeSingleQuote(string(settings)) + "'", nil
}

func reviewGitAuthenticationConfigScript(workDir, repoURL string) string {
	settings, err := json.Marshal(map[string]string{"work_dir": workDir, "repo_url": repoURL})
	if err != nil {
		return "exit 1\n"
	}
	return "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(reviewGitAuthenticationConfigPython) + "' '" + shellEscapeSingleQuote(string(settings)) + "'\n"
}

func (o *Orchestrator) hydrateCodeReviewTrees(ctx context.Context, session *models.Session, sandbox *Sandbox, repoURL, token string, log zerolog.Logger) (returnErr error) {
	defer func() {
		returnErr = NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, returnErr)
	}()
	revision, err := reviewWorkspaceRevision(session)
	if err != nil {
		return err
	}
	hydrateCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(hydrateCtx)
	if err != nil {
		return err
	}
	stage := observability.BeginStage(true, log, "review_evidence_hydration")
	cancelPath := reviewPreparationMarkerPath(sandbox)
	payload := reviewWorkspacePreparationPayload{
		WorkDir: sandbox.WorkDir, Standalone: true, CancelPath: cancelPath,
		ExpectedRepoURL:         repoURL,
		Prepare:                 reviewTreeEvidenceScript(sandbox.WorkDir, revision.BaseSHA, revision.HeadSHA, repoURL),
		VerifyIntegrity:         reviewGitObjectIntegrityScript(sandbox.WorkDir),
		VerifyCapturedIntegrity: reviewGitObjectIntegrityScript(sandbox.WorkDir) + reviewTreeEvidenceScript(sandbox.WorkDir, revision.BaseSHA, revision.HeadSHA, ""),
		AuthenticateGit:         true,
		DeadlineUnix:            deadline,
	}
	err = o.execReviewPreparationPayload(hydrateCtx, sandbox, payload, reviewGitAuthentication{repoURL, token}, log)
	stage.End(observability.StageOutcome(hydrateCtx, err))
	return err
}

// Review checkout fetches the mutable PR ref to detect a moved head, then
// checks out only the expected, already-hydrated commit. Network operations use
// the same bounded process-group/cancellation wrapper as evidence preparation.
// PR repair reconstruction keeps its existing checkout helper.
func (o *Orchestrator) checkoutReviewPullRequestHead(ctx context.Context, sandbox *Sandbox, repoURL, token string, number int, branch, expected string) (returnErr error) {
	defer func() {
		returnErr = NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageCheckout, returnErr)
	}()
	ctx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	fetch := reviewGitAuthenticationConfigScript(sandbox.WorkDir, repoURL) + fmt.Sprintf("/usr/bin/git -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0 fetch --quiet --no-tags --filter=blob:none origin 'pull/%d/head'", number)
	if err := o.runBoundedReviewCommandAuthenticated(ctx, sandbox, fetch, reviewGitAuthentication{repoURL, token}, path.Join(sandbox.WorkDir, ".git")); err != nil {
		return fmt.Errorf("fetch review pull request head: %w", err)
	}
	var stdout, stderr bytes.Buffer
	code, err := o.execForReviewPreparation(ctx, sandbox, "/usr/bin/git -c core.hooksPath=/dev/null rev-parse FETCH_HEAD", &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("verify fetched review head: %s", redactReviewGitAuthentication(err.Error(), token))
	}
	if code != 0 {
		return fmt.Errorf("verify fetched review head: exit=%d stderr=%s", code, redactReviewGitAuthentication(stderr.String(), token))
	}
	actual := strings.TrimSpace(stdout.String())
	if actual != expected {
		return fmt.Errorf("%w: expected %s, got %s", ErrStalePullRequestHead, expected, actual)
	}
	checkout := fmt.Sprintf("GIT_NO_LAZY_FETCH=1 /usr/bin/git -c core.hooksPath=/dev/null -c remote.origin.promisor=false checkout -B '%s' '%s'", shellEscapeSingleQuote(branch), shellEscapeSingleQuote(expected))
	if err := o.runBoundedReviewCommand(ctx, sandbox, checkout, ""); err != nil {
		return fmt.Errorf("checkout review pull request head: %w", err)
	}
	return o.execReviewEvidence(ctx, sandbox, fmt.Sprintf("test \"$(/usr/bin/git -c core.hooksPath=/dev/null rev-parse HEAD)\" = '%s'", shellEscapeSingleQuote(expected)), "")
}

func (o *Orchestrator) runBoundedReviewCommand(ctx context.Context, sandbox *Sandbox, script, token string) error {
	return o.runBoundedReviewCommandAt(ctx, sandbox, script, token, path.Join(sandbox.WorkDir, ".git"))
}

func (o *Orchestrator) runBoundedReviewCommandAt(ctx context.Context, sandbox *Sandbox, script, token, cancelDir string) error {
	return o.runBoundedReviewCommandAuthenticated(ctx, sandbox, script, reviewGitAuthentication{Token: token}, cancelDir)
}

func (o *Orchestrator) runBoundedReviewCommandAuthenticated(ctx context.Context, sandbox *Sandbox, script string, auth reviewGitAuthentication, cancelDir string) error {
	commandCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(commandCtx)
	if err != nil {
		return err
	}
	cancelPath := path.Join(cancelDir, "143-review-cancel-"+uuid.NewString())
	if auth.RepoURL != "" || cancelDir == path.Join(sandbox.WorkDir, ".git") {
		cancelPath = reviewPreparationMarkerPath(sandbox)
	}
	payload := reviewWorkspacePreparationPayload{
		WorkDir: sandbox.WorkDir, Standalone: true, CancelPath: cancelPath,
		Prepare: "set -eu\n" + script, DeadlineUnix: deadline,
		AuthenticateGit: auth.RepoURL != "",
	}
	return o.execReviewPreparationPayload(commandCtx, sandbox, payload, auth, o.logger)
}

func (o *Orchestrator) cloneRepositoryForReview(ctx context.Context, sandbox *Sandbox, repoURL, branch, token string) (returnErr error) {
	defer func() { returnErr = NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageClone, returnErr) }()
	ctx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Start outside the destination so cloning can create the empty checkout.
	// Authenticated clone deliberately skips checkout, so smudge/process filters
	// cannot inherit the Git-only HTTP header. Pinned checkout happens later.
	cloneSandbox := *sandbox
	cloneSandbox.WorkDir = path.Dir(sandbox.WorkDir)
	if sandbox.HomeDir != "" {
		cloneSandbox.WorkDir = sandbox.HomeDir
	}
	deadline, err := reviewPreparationDeadlineUnix(ctx)
	if err != nil {
		return err
	}
	payload := reviewWorkspacePreparationPayload{
		WorkDir: cloneSandbox.WorkDir, Standalone: true, AuthenticateGit: true,
		CloneDestination: sandbox.WorkDir, ExpectedRepoURL: repoURL,
		Prepare: reviewCloneScript(repoURL, branch, sandbox.WorkDir), DeadlineUnix: deadline,
		CancelPath: path.Join(cloneSandbox.WorkDir, "143-review-cancel-"+uuid.NewString()),
	}
	return o.execReviewPreparationPayload(ctx, &cloneSandbox, payload, reviewGitAuthentication{repoURL, token}, o.logger)
}

func reviewCloneScript(repoURL, branch, destination string) string {
	quote := func(value string) string { return "'" + shellEscapeSingleQuote(value) + "'" }
	return "/usr/bin/git -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0 clone --template=/dev/null --no-checkout --filter=blob:none --branch " + quote(branch) + " " + quote(repoURL) + " " + quote(destination)
}

func (o *Orchestrator) execForReviewPreparation(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	return execForReviewPreparation(ctx, o.provider, sandbox, command, stdout, stderr)
}

func execForReviewPreparation(ctx context.Context, provider SandboxProvider, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	clean, err := reviewPreparationSandbox(sandbox)
	if err != nil {
		return -1, err
	}
	sandbox = clean
	if validator, ok := provider.(reviewPreparationEnvironmentValidator); ok {
		if err := validator.ValidateReviewPreparationEnvironment(ctx, sandbox); err != nil {
			return -1, err
		}
	}
	if provider, ok := provider.(reviewPreparationExecutor); ok {
		return provider.ExecForReviewPreparation(ctx, sandbox, command, stdout, stderr)
	}
	return provider.Exec(ctx, sandbox, command, stdout, stderr)
}

func (o *Orchestrator) readReviewPreparationFile(ctx context.Context, sandbox *Sandbox, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := o.provider.(reviewPreparationExecutor); !ok {
		return o.provider.ReadFile(ctx, sandbox, name)
	}
	var stdout, stderr bytes.Buffer
	code, err := o.execForReviewPreparation(ctx, sandbox, "/bin/cat '"+shellEscapeSingleQuote(name)+"'", &stdout, &stderr)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("read review config: exit=%d: %s", code, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (o *Orchestrator) execReviewEvidence(ctx context.Context, sandbox *Sandbox, command, token string) error {
	return o.execReviewEvidenceInput(ctx, sandbox, command, token, nil)
}

func (o *Orchestrator) execReviewEvidenceInput(ctx context.Context, sandbox *Sandbox, command, token string, input io.Reader) error {
	var stderr bytes.Buffer
	var exitCode int
	var err error
	if input == nil {
		exitCode, err = o.execForReviewPreparation(ctx, sandbox, command, io.Discard, &stderr)
	} else {
		var clean *Sandbox
		clean, err = reviewPreparationSandbox(sandbox)
		if err == nil {
			if validator, ok := o.provider.(reviewPreparationEnvironmentValidator); ok {
				err = validator.ValidateReviewPreparationEnvironment(ctx, clean)
			}
		}
		if err == nil {
			if trusted, ok := o.provider.(reviewPreparationTrustedStdinExecutor); ok {
				exitCode, err = trusted.ExecWithStdinForReviewPreparation(ctx, clean, command, input, io.Discard, &stderr)
			} else if provider, ok := o.provider.(reviewPreparationStdinExecutor); ok {
				exitCode, err = provider.ExecWithStdin(ctx, clean, command, input, io.Discard, &stderr)
			} else {
				err = errors.New("review provider does not support private Git authentication input")
			}
		}
	}
	if err != nil {
		// Preserve cancellation and sanitize private HTTP-header diagnostics,
		// even if a provider or Git fails while handling the private input.
		if ctx.Err() != nil {
			return fmt.Errorf("prepare review evidence: %w", ctx.Err())
		}
		return fmt.Errorf("prepare review evidence: %s", redactReviewGitAuthentication(err.Error(), token))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode == 124 {
		return context.DeadlineExceeded
	}
	if exitCode == 126 {
		return errReviewHydrationRequired
	}
	if input != nil && exitCode == 125 {
		return NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, ErrCodeReviewSourceIntegrity)
	}
	if exitCode != 0 {
		return fmt.Errorf("prepare review evidence: exit=%d stderr=%s", exitCode, redactReviewGitAuthentication(strings.TrimSpace(stderr.String()), token))
	}
	return nil
}

// ensureCodeReviewWorkspaceEvidence is shared by cold, restored, and warm
// workspaces. A sibling may see the final HEAD before setup finishes; the
// cancellable lock makes it join the same preparation instead of running an
// agent early. A legacy workspace with no receipt is repaired through this
// path, rather than waiting forever for a receipt nobody will publish.
func (o *Orchestrator) ensureCodeReviewWorkspaceEvidence(ctx context.Context, session *models.Session, sandbox *Sandbox, log zerolog.Logger) (returnErr error) {
	defer func() {
		returnErr = NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageDependencySetup, returnErr)
	}()
	prepareCtx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, deadlineErr := reviewPreparationDeadlineUnix(prepareCtx)
	verifyOnly := errors.Is(deadlineErr, context.DeadlineExceeded) && ctx.Err() == nil
	if deadlineErr != nil && !verifyOnly {
		return deadlineErr
	}
	if verifyOnly {
		// Expired attempts may validate already-complete evidence, but cannot
		// fetch, install, bootstrap, or write a replacement receipt.
		cancel()
		prepareCtx, cancel = context.WithTimeout(ctx, CodeReviewWorkspaceVerificationTimeout)
		defer cancel()
		deadline, deadlineErr = reviewPreparationDeadlineUnix(prepareCtx)
		if deadlineErr != nil {
			return deadlineErr
		}
	}
	revision, err := reviewWorkspaceRevision(session)
	if err != nil {
		return err
	}
	if session.RepositoryID == nil || o.repositories == nil {
		return errors.New("review evidence repository binding unavailable")
	}
	repo, err := o.repositories.GetByID(prepareCtx, session.OrgID, *session.RepositoryID)
	if err != nil {
		return fmt.Errorf("load review evidence repository: %w", err)
	}
	if err := (reviewGitAuthentication{RepoURL: repo.CloneURL}).validate(); err != nil {
		return err
	}
	// Strict config reads prevent malformed or unavailable setup instructions
	// from becoming a successful evidence receipt. Repos without config retain
	// the normal empty-setup behavior.
	raw, err := o.readReviewPreparationFile(prepareCtx, sandbox, path.Join(sandbox.WorkDir, repoconfig.ConfigPath))
	if err != nil && !isSandboxFileMissing(err) {
		return fmt.Errorf("read review repository config: %w", err)
	}
	var cfg repoconfig.Config
	if len(raw) > 0 {
		cfg, err = repoconfig.Parse(raw)
		if err != nil {
			return fmt.Errorf("parse review repository config: %w", err)
		}
	}
	receipt := reviewWorkspaceEvidenceReceipt{Version: 2, BaseSHA: revision.BaseSHA, HeadSHA: revision.HeadSHA, ConfigHash: fmt.Sprintf("%x", sha256.Sum256(raw))}
	install, verifyTools, err := sandboxdeps.RequiredCommands(cfg.Dependencies)
	if err != nil {
		return err
	}
	verifyGit := reviewPinnedCheckoutVerificationScript(sandbox.WorkDir, revision.BaseSHA, revision.HeadSHA)
	verifyTrees := reviewTreeEvidenceScript(sandbox.WorkDir, revision.BaseSHA, revision.HeadSHA, "")
	prepareCommands := append([]string(nil), install...)
	setupCommands := cfg.Bootstrap.Commands
	var evidence []repoconfig.ReviewEvidenceEntry
	var verifyCommands []string
	if cfg.Review != nil {
		setupCommands = cfg.Review.Commands
		evidence = cfg.Review.Evidence
		verifyCommands = cfg.Review.VerifyCommands
	}
	prepareCommands = append(prepareCommands, setupCommands...)
	prepareScript := "set -eu\ncd '" + shellEscapeSingleQuote(sandbox.WorkDir) + "'\n"
	// Subshell each command: installer scripts and repo bootstrap may install
	// EXIT traps or change directory; those must not affect neighboring recipes.
	for _, command := range prepareCommands {
		prepareScript += "(\n" + command + "\n)\n"
	}
	verifyToolsScript := "set -eu\n" + strings.Join(verifyTools, "\n")
	verifyRecipeScript := "set -eu\n"
	for _, command := range verifyCommands {
		verifyRecipeScript += "(\n" + command + "\n)\n"
	}
	encodedReceipt, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode review evidence receipt: %w", err)
	}
	cancelPath := reviewPreparationMarkerPath(sandbox)
	payload := reviewWorkspacePreparationPayload{
		WorkDir: sandbox.WorkDir, Receipt: encodedReceipt,
		ExpectedRepoURL: repo.CloneURL,
		VerifyIntegrity: reviewGitObjectIntegrityScript(sandbox.WorkDir),
		VerifyGit:       "set -eu\n" + verifyGit, VerifyTrees: verifyTrees,
		Prepare: prepareScript, VerifyTools: verifyToolsScript,
		BootstrapDeclared:      len(setupCommands) > 0,
		ArtifactRecipeDeclared: len(setupCommands) > 0 || len(verifyCommands) > 0 || len(evidence) > 0,
		CacheableArtifacts:     cfg.Review != nil && len(evidence) > 0 && len(verifyCommands) > 0,
		Evidence:               evidence, VerifyRecipe: verifyRecipeScript,
		DeadlineUnix: deadline,
		VerifyOnly:   verifyOnly,
		CancelPath:   cancelPath,
	}
	execute := func(auth reviewGitAuthentication) error {
		return o.execReviewPreparationPayload(prepareCtx, sandbox, payload, auth, log)
	}
	stage := observability.BeginStage(true, log, "review_workspace_evidence_preparation")
	// The locked local-only pass asks for auth before any repository command
	// only when pinned trees are missing. Healthy warm reuse and dependency
	// repair with complete Git evidence need no installation-token lookup.
	err = execute(reviewGitAuthentication{})
	if errors.Is(err, errReviewHydrationRequired) && !verifyOnly {
		if o.github == nil {
			err = errors.New("review evidence hydration dependencies unavailable")
		} else {
			token, tokenErr := o.github.GetInstallationToken(prepareCtx, repo.InstallationID)
			if tokenErr != nil {
				err = fmt.Errorf("get review evidence installation token: %w", tokenErr)
			} else {
				payload.AuthenticateGit = true
				payload.HydrateTrees = reviewTreeEvidenceScript(sandbox.WorkDir, revision.BaseSHA, revision.HeadSHA, repo.CloneURL)
				err = execute(reviewGitAuthentication{repo.CloneURL, token})
			}
		}
	}
	stage.End(observability.StageOutcome(prepareCtx, err))
	if verifyOnly && err != nil && !errors.Is(err, ErrCodeReviewSourceIntegrity) {
		return context.DeadlineExceeded
	}
	return err
}

// Docker attach cancellation does not terminate an exec's processes. Signal
// this particular preparation through a private filesystem marker, then wait
// for its process-group cleanup before returning the cancellation to the caller.
func reviewPreparationCommand(payload reviewWorkspacePreparationPayload, auth reviewGitAuthentication) (string, io.Reader, error) {
	if auth.RepoURL != "" {
		if err := auth.validate(); err != nil {
			return "", nil, err
		}
	}
	control, err := json.Marshal(reviewWorkspacePreparationControl{WorkDir: payload.WorkDir, DeadlineUnix: payload.DeadlineUnix, TimeoutSeconds: payload.TimeoutSeconds, CancelPath: payload.CancelPath})
	if err != nil {
		return "", nil, err
	}
	message, err := json.Marshal(struct {
		Payload        reviewWorkspacePreparationPayload `json:"payload"`
		Authentication *reviewGitAuthentication          `json:"authentication,omitempty"`
	}{Payload: payload, Authentication: func() *reviewGitAuthentication {
		if auth.RepoURL != "" {
			return &auth
		}
		return nil
	}()})
	if err != nil {
		return "", nil, err
	}
	if len(message) > 1024*1024 {
		return "", nil, errors.New("review preparation input exceeds capacity")
	}
	command := "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(reviewWorkspacePreparationPython) + "' '" + shellEscapeSingleQuote(string(control)) + "'"
	return command, bytes.NewReader(message), nil
}

func (o *Orchestrator) execReviewPreparationPayload(ctx context.Context, sandbox *Sandbox, payload reviewWorkspacePreparationPayload, auth reviewGitAuthentication, log zerolog.Logger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := o.provider.(reviewPreparationTrustedStdinExecutor); !ok {
		if _, ok := o.provider.(reviewPreparationStdinExecutor); !ok {
			return errors.New("review provider does not support private Git authentication input")
		}
	}
	if auth.RepoURL != "" {
		payload.ExpectedRepoURL = auth.RepoURL
	}
	command, input, err := reviewPreparationCommand(payload, auth)
	if err != nil {
		return err
	}
	return o.execReviewPreparationInput(ctx, sandbox, command, payload.CancelPath, auth.Token, input, log)
}

func (o *Orchestrator) execReviewPreparationInput(ctx context.Context, sandbox *Sandbox, command, cancelPath, token string, input io.Reader, log zerolog.Logger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- o.execReviewEvidenceInput(ctx, sandbox, command, token, input) }()
	var err error
	finished := false
	select {
	case err = <-result:
		finished = true
	case <-ctx.Done():
		err = ctx.Err()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	quotedCancel := "'" + shellEscapeSingleQuote(cancelPath) + "'"
	quotedDone := "'" + shellEscapeSingleQuote(cancelPath+".done") + "'"
	cleanupCommand := "/bin/rm -f " + quotedCancel + " " + quotedDone
	if ctx.Err() != nil || err != nil {
		cleanupCommand = "printf cancelled > " + quotedCancel + "; i=0; until test -f " + quotedDone + "; do i=$((i+1)); test \"$i\" -lt 40 || exit 1; /bin/sleep 0.1; done; " + cleanupCommand
	}
	if cleanupErr := o.execReviewEvidence(cleanupCtx, sandbox, cleanupCommand, ""); cleanupErr != nil {
		log.Warn().Err(cleanupErr).Msg("failed to confirm review preparation process cleanup")
	}
	if ctx.Err() != nil && !finished {
		select {
		case <-result:
		case <-cleanupCtx.Done():
			log.Warn().Msg("review preparation output stream did not close after cancellation cleanup")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// All executors sharing a container serialize through a kernel advisory lock,
// including executors in different server processes. Child process groups are
// killed before the lock is released on timeout or a termination signal.
const reviewWorkspacePreparationPython = `
import base64, fcntl, json, os, select, signal, stat, subprocess, sys, tempfile, time
git_config_guard = r'''` + reviewGitAuthenticationConfigPython + `'''
p = json.loads(sys.argv[1])
auth = None
workdir = p["work_dir"]
gitdir = os.path.join(workdir, ".git")
receipt_path = os.path.join(gitdir, "143-review-evidence.json")
deadline = time.monotonic() + (max(0, p["deadline_unix"] - time.time()) if p.get("deadline_unix") else p["timeout_seconds"])
active = None
cancel_path = p.get("cancel_path", "")

def check_cancelled():
    if cancel_path and os.path.exists(cancel_path):
        stop_child()
        raise RuntimeError("review workspace preparation cancelled")

def stop_child():
    global active
    if active is not None:
        group = active.pid
        try:
            os.killpg(group, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            active.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass
        # The shell leader can exit while descendants ignore TERM. Always
        # kill the captured group, even after waiting observes leader exit.
        try:
            os.killpg(group, signal.SIGKILL)
        except ProcessLookupError:
            pass
        active.wait()
    active = None

def terminated(signum, frame):
    stop_child()
    raise SystemExit(128 + signum)

signal.signal(signal.SIGTERM, terminated)
signal.signal(signal.SIGINT, terminated)

def child_environment(authenticate=False, platform=False):
    env = {name: value for name, value in os.environ.items() if name not in {"GITHUB_TOKEN", "GH_TOKEN", "CURL_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR", "http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "no_proxy", "NO_PROXY"} and not name.startswith("GIT_")}
    env.update({"GIT_NO_REPLACE_OBJECTS": "1", "GIT_GRAFT_FILE": "/dev/null"})
    if authenticate or platform:
        env = {name: value for name, value in env.items() if not name.startswith("GIT_")}
        env.update({"PATH": "/usr/bin:/bin", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_NO_REPLACE_OBJECTS": "1", "GIT_GRAFT_FILE": "/dev/null", "GIT_TERMINAL_PROMPT": "0"})
        if not p.get("clone_destination"):
            env.update({"GIT_DIR": gitdir, "GIT_COMMON_DIR": gitdir, "GIT_WORK_TREE": workdir, "GIT_OBJECT_DIRECTORY": os.path.join(gitdir,"objects")})
    if authenticate:
        env.update({"PATH": "/usr/bin:/bin", "GIT_ALLOW_PROTOCOL": auth["repo_url"].split(":",1)[0]})
    if authenticate and auth and auth["token"]:
        encoded = base64.b64encode(("x-access-token:" + auth["token"]).encode()).decode()
        owned = [("http.extraHeader", ""), ("http." + auth["repo_url"] + ".extraHeader", "AUTHORIZATION: basic " + encoded), ("http.proxy", ""), ("http.sslVerify", "true")]
        env["GIT_CONFIG_COUNT"] = str(len(owned))
        for index, (key, value) in enumerate(owned):
            env["GIT_CONFIG_KEY_" + str(index)] = key
            env["GIT_CONFIG_VALUE_" + str(index)] = value
    return env

def run(script, required=True, authenticate=False, platform=False):
    global active
    if not p.get("standalone", False) and script in (p.get("prepare"), p.get("verify_recipe"), p.get("verify_tools")) and auth is not None:
        raise RuntimeError("private review authentication was not released before repository preparation")
    check_cancelled()
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("review workspace preparation timed out")
    child_directory = "/" if authenticate and p.get("clone_destination") else workdir
    active = subprocess.Popen(["/bin/sh", "-c", script], cwd=child_directory, env=child_environment(authenticate,platform), stdin=subprocess.DEVNULL, close_fds=True, start_new_session=True)
    while True:
        check_cancelled()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            stop_child()
            raise TimeoutError("review workspace preparation timed out")
        try:
            code = active.wait(timeout=min(0.1, remaining))
            break
        except subprocess.TimeoutExpired:
            pass
    stop_child()
    if required and code:
        if platform and script in (p.get("verify_git"), p.get("verify_captured_integrity")) and code == 125:
            raise SourceIntegrityFailure("pinned review source object integrity validation failed; workspace must be recreated")
        raise RuntimeError("review workspace preparation command failed (exit=%d)" % code)
    return code == 0

def capture(args):
    global active
    check_cancelled()
    if time.monotonic() >= deadline:
        raise TimeoutError("review workspace preparation timed out")
    active = subprocess.Popen(args, cwd=workdir, env=child_environment(platform=True), stdin=subprocess.DEVNULL, close_fds=True, stdout=subprocess.PIPE, start_new_session=True)
    while True:
        check_cancelled()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            stop_child()
            raise TimeoutError("review workspace preparation timed out")
        try:
            data, ignored = active.communicate(timeout=min(0.1, remaining))
            code = active.returncode
            break
        except subprocess.TimeoutExpired:
            pass
    stop_child()
    if code:
        raise RuntimeError("review workspace output verification failed (exit=%d)" % code)
    return data

def check_budget():
    check_cancelled()
    if time.monotonic() >= deadline:
        raise TimeoutError("review workspace preparation timed out")

def validate_git_configuration(directory=None):
    if p.get("expected_repo_url"):
        capture(["/usr/bin/python3", "-I", "-S", "-c", git_config_guard, json.dumps({"work_dir": directory or workdir, "repo_url": p["expected_repo_url"]})])

def validate_clone_destination():
    target = p["clone_destination"]
    if os.path.lexists(target):
        if not stat.S_ISDIR(os.lstat(target).st_mode) or os.listdir(target):
            raise RuntimeError("authenticated review clone requires a fresh empty destination")

def read_preparation_input():
    # Control metadata is nonsecret and small. All commands and authentication
    # arrive through bounded stdin after cancellation/deadline handling exists.
    data = bytearray()
    try:
        while True:
            check_budget()
            readable, ignored, ignored = select.select([0], [], [], min(0.1, max(0, deadline - time.monotonic())))
            if readable:
                block = os.read(0, 4096)
                if not block:
                    break
                data.extend(block)
                if len(data) > 1024 * 1024:
                    raise RuntimeError("private review preparation input exceeds capacity")
        return json.loads(data)
    finally:
        sys.stdin.close()
        try:
            os.close(0)
        except OSError:
            pass

credential_roots = {".npmrc", ".pypirc", ".netrc", ".git-credentials", ".ssh", ".aws", ".azure"}
real_workdir = os.path.realpath(workdir)

def check_evidence_relative(relative):
    check_budget()
    if not isinstance(relative, str) or not relative or relative == "." or os.path.isabs(relative) or os.path.normpath(relative) != relative or "\\" in relative:
        raise RuntimeError("review evidence path is not canonical and repository-relative")
    parts = relative.split("/")
    if ".." in parts or ".git" in parts:
        raise RuntimeError("review evidence path must exclude Git metadata")
    root = parts[0]
    if root in credential_roots or root == ".env" or root.startswith(".env.") or relative == ".config/gh" or relative.startswith(".config/gh/"):
        raise RuntimeError("review evidence must exclude workspace credentials")

def safe_evidence_path(relative):
    check_evidence_relative(relative)
    target = os.path.join(workdir, relative)
    resolved = os.path.realpath(target)
    if os.path.commonpath([real_workdir, resolved]) != real_workdir or resolved == real_workdir:
        raise RuntimeError("review evidence symlink escapes repository source roots")
    resolved_relative = os.path.relpath(resolved, real_workdir)
    resolved_parts = resolved_relative.split("/")
    resolved_root = resolved_parts[0]
    if ".git" in resolved_parts or resolved_root in credential_roots or resolved_root == ".env" or resolved_root.startswith(".env.") or resolved_relative == ".config/gh" or resolved_relative.startswith(".config/gh/"):
        raise RuntimeError("review evidence symlink targets metadata or workspace credentials")
    return target, resolved

def evidence_manifest(required=False, safety_only=False):
    manifest = []
    complete = True
    total_files = 0
    total_path_bytes = 0
    def account_entry(relative):
        nonlocal total_files, total_path_bytes
        check_budget()
        total_files += 1
        total_path_bytes += len(os.fsencode(relative))
        if total_files > 300000 or total_path_bytes > 64 * 1024 * 1024:
            raise RuntimeError("declared review evidence exceeds bounded source manifest capacity")
    directory_categories = {"javascript_dependencies", "go_dependencies", "python_dependencies", "ruby_dependencies", "external_caches", "submodules"}
    for entry in p.get("evidence", []):
        root = entry["path"]
        target, resolved = safe_evidence_path(root)
        if not os.path.exists(target):
            if required:
                raise RuntimeError("declared review evidence root is missing: " + root)
            complete = False
            continue
        if entry["category"] in directory_categories and not os.path.isdir(target):
            raise RuntimeError("declared dependency evidence must be a source directory: " + root)
        account_entry(root)
        files = []
        pending = [(root, None, None, os.path.relpath(resolved, real_workdir))]
        visited = set()
        regular_files = 0
        while pending:
            relative, child, parent_resolved, parent_relative = pending.pop()
            check_evidence_relative(relative)
            resolved_relative = parent_relative
            if child is not None:
                target = child.path
                resolved = os.path.join(parent_resolved, child.name)
                resolved_relative = parent_relative + "/" + child.name
                if resolved_relative != relative:
                    check_evidence_relative(resolved_relative)
            if safety_only and child is not None:
                mode = stat.S_IFLNK if child.is_symlink() else stat.S_IFDIR if child.is_dir(follow_symlinks=False) else stat.S_IFREG if child.is_file(follow_symlinks=False) else 0
                info = None
            else:
                info = child.stat(follow_symlinks=False) if child is not None else os.lstat(target)
                mode = info.st_mode
            directory = stat.S_ISDIR(mode)
            # Non-symlink entries inherit the validated containing directory.
            # Only roots and symlinks require ancestor resolution; every entry
            # retains lexical and resolved workspace-credential checks. This
            # avoids repeatedly resolving deep SDK paths for ordinary files
            # and directories while preserving source-root containment.
            if stat.S_ISLNK(mode):
                target, resolved = safe_evidence_path(relative)
                resolved_relative = os.path.relpath(resolved, real_workdir)
            if stat.S_ISLNK(mode):
                if not safety_only:
                    files.append([relative, "symlink", os.readlink(target)])
                try:
                    followed = os.stat(target)
                except FileNotFoundError:
                    followed = None
                if followed is not None and stat.S_ISREG(followed.st_mode):
                    if not safety_only and not os.access(target, os.R_OK):
                        raise RuntimeError("declared review source is not readable: " + relative)
                    if not safety_only:
                        files.append([relative, "resolved_file", followed.st_size])
                    regular_files += 1
                elif followed is not None and not stat.S_ISDIR(followed.st_mode):
                    raise RuntimeError("declared review evidence contains a non-source filesystem object: " + relative)
                directory = followed is not None and stat.S_ISDIR(followed.st_mode)
                # Safe dangling SDK fixture links are recorded but do not
                # count as regular source files.
            elif stat.S_ISREG(mode):
                if not safety_only and not os.access(target, os.R_OK):
                    raise RuntimeError("declared review source is not readable: " + relative)
                if not safety_only:
                    files.append([relative, "file", info.st_size])
                regular_files += 1
            elif not directory:
                raise RuntimeError("declared review evidence contains a non-source filesystem object: " + relative)
            if directory and resolved not in visited:
                visited.add(resolved)
                if not safety_only:
                    files.append([relative, "directory"])
                with os.scandir(target) as children:
                    bounded_children = []
                    for child in children:
                        check_budget()
                        if child.name != ".git":
                            account_entry(relative + "/" + child.name)
                            bounded_children.append(child)
                    for child in sorted(bounded_children, key=lambda child: child.name, reverse=True):
                        pending.append((relative + "/" + child.name, child, resolved, resolved_relative))
        if not regular_files:
            if required:
                raise RuntimeError("declared review evidence root contains no regular source files: " + root)
            complete = False
        manifest.append({"path": root, "files": files})
    return manifest if complete else None

class HydrationRequired(Exception):
    pass

class SourceIntegrityFailure(Exception):
    pass

def verify_object_integrity(script):
    if script and not run(script, required=False, platform=True):
        raise SourceIntegrityFailure("pinned review source object integrity validation failed; workspace must be recreated")

def verify_pinned_source():
    # Detect corrupt present objects before graph/tree readers can mistake
    # their failure for missing evidence, then prove captured closure and HEAD.
    run(p["verify_git"], platform=True)

def validate_managed_metadata():
    # This also runs for local-only payloads without a repository URL. Never
    # open a FIFO or follow a linked metadata directory before the budget loop.
    for target in (workdir, gitdir):
        if not stat.S_ISDIR(os.lstat(target).st_mode):
            raise RuntimeError("review preparation requires literal repository metadata")
    for target in (os.path.join(gitdir, "143-review-preparation.lock"), receipt_path):
        if os.path.lexists(target) and not stat.S_ISREG(os.lstat(target).st_mode):
            raise RuntimeError("review preparation requires regular managed metadata")

def read_receipt():
    try:
        descriptor = os.open(receipt_path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except FileNotFoundError:
        return {}
    try:
        info = os.fstat(descriptor)
        # The manifest has separate entry/path limits; this finite serialized
        # bound also covers malformed or externally replaced regular receipts.
        limit = 256 * 1024 * 1024
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise RuntimeError("review preparation requires a bounded regular receipt")
        data = bytearray()
        while True:
            check_budget()
            block = os.read(descriptor, min(1024 * 1024, limit + 1 - len(data)))
            if not block:
                break
            data.extend(block)
            if len(data) > limit:
                raise RuntimeError("review preparation receipt exceeds capacity")
        try:
            value = json.loads(data)
        except ValueError:
            return {}
        return value if isinstance(value, dict) else {}
    finally:
        os.close(descriptor)

def prepare_workspace():
    global auth
    validate_git_configuration()  # Reject linked metadata before opening the shared lock.
    validate_managed_metadata()
    descriptor = os.open(os.path.join(gitdir, "143-review-preparation.lock"), os.O_RDWR | os.O_CREAT | os.O_NONBLOCK | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "a") as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise RuntimeError("review preparation requires a regular shared lock")
        while True:
            check_cancelled()
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise TimeoutError("review workspace preparation lock timed out")
                time.sleep(0.1)
        validate_git_configuration()
        verify_object_integrity(p.get("verify_integrity"))
        if not run(p["verify_trees"], required=False,platform=True):
            if p.get("verify_only", False):
                raise TimeoutError("review preparation budget exhausted; pinned source repair required")
            if not p.get("hydrate_trees"):
                raise HydrationRequired("pinned review source requires authenticated hydration")
            validate_git_configuration()
            run(p["hydrate_trees"], authenticate=True)
        auth = None  # No private authentication state remains for recipes.
        verify_pinned_source()
        previous = read_receipt()
        artifact_recipe = p.get("artifact_recipe_declared", p.get("bootstrap_declared", False))
        can_reuse = previous.get("revision") == p["receipt"]
        # Unrelated npm outputs cannot establish that an external Go/Python
        # cache exists. Only an explicit source map plus verifiers can make an
        # artifact recipe reusable; legacy bootstrap remains compatible but is
        # rerun within the original setup budget on each preparation request.
        can_reuse = can_reuse and (not artifact_recipe or p.get("cacheable_artifacts", False))
        if not p.get("verify_only", False):
            # Reject unsafe or incomplete roots before arbitrary repository
            # commands without reading every file's metadata twice. Readiness
            # still requires the complete exact manifest after the verifier.
            can_reuse = evidence_manifest(safety_only=True) is not None and can_reuse
        tools_valid = can_reuse and run(p["verify_tools"], required=False)
        recipe_valid = tools_valid
        if recipe_valid and not p.get("verify_only", False):
            recipe_valid = run(p["verify_recipe"], required=False)
            validate_git_configuration()
            # Repository verifiers are arbitrary commands: they may never
            # alter pinned tracked source or erase declared source evidence.
            verify_pinned_source()
        if recipe_valid:
            current_evidence = evidence_manifest()
            recipe_valid = current_evidence is not None and previous.get("evidence") == current_evidence
        if not recipe_valid:
            if p.get("verify_only", False):
                raise TimeoutError("review preparation budget exhausted; dependency repair required")
            evidence_manifest(safety_only=True)  # Reject unsafe roots even after a failed verifier.
            run(p["prepare"])
            validate_git_configuration()
            verify_pinned_source()
            run(p["verify_tools"])
            run(p["verify_recipe"])
            validate_git_configuration()
            verify_pinned_source()
            run(p["verify_trees"],platform=True)
            record = {"revision": p["receipt"], "evidence": evidence_manifest(required=True)}
            check_budget()
            fd, temporary = tempfile.mkstemp(prefix="143-review-receipt-", dir=gitdir)
            try:
                with os.fdopen(fd, "w") as saved:
                    json.dump(record, saved)
                os.replace(temporary, receipt_path)
            finally:
                if os.path.exists(temporary):
                    os.unlink(temporary)
        validate_git_configuration()
        verify_pinned_source()

try:
    message = read_preparation_input()
    p = message.pop("payload")
    auth = message.pop("authentication", None)
    del message
    if p.get("standalone", False):
        if p.get("clone_destination"):
            validate_clone_destination()
        elif p.get("authenticate_git", False) or p.get("guard_configuration", False):
            validate_git_configuration()
        verify_object_integrity(p.get("verify_integrity"))
        run(p["prepare"], authenticate=p.get("authenticate_git", False),platform=p.get("platform_command",False))
        if p.get("clone_destination"):
            validate_git_configuration(p["clone_destination"])
        elif p.get("guard_configuration",False):
            validate_git_configuration()
        if p.get("verify_captured_integrity"):
            run(p["verify_captured_integrity"], platform=True)
    else:
        prepare_workspace()
except Exception as error:
    stop_child()
    print(str(error), file=sys.stderr)
    sys.exit(124 if isinstance(error, TimeoutError) else 125 if isinstance(error, SourceIntegrityFailure) else 126 if isinstance(error, HydrationRequired) else 1)
finally:
    stop_child()
    if cancel_path:
        with open(cancel_path + ".done", "w"):
            pass
`

type reviewWorkspacePreparationPayload struct {
	WorkDir                 string                           `json:"work_dir"`
	Receipt                 json.RawMessage                  `json:"receipt"`
	VerifyGit               string                           `json:"verify_git"`
	VerifyTrees             string                           `json:"verify_trees"`
	VerifyIntegrity         string                           `json:"verify_integrity,omitempty"`
	VerifyCapturedIntegrity string                           `json:"verify_captured_integrity,omitempty"`
	HydrateTrees            string                           `json:"hydrate_trees"`
	Prepare                 string                           `json:"prepare"`
	VerifyTools             string                           `json:"verify_tools"`
	ArtifactRecipeDeclared  bool                             `json:"artifact_recipe_declared"`
	CacheableArtifacts      bool                             `json:"cacheable_artifacts"`
	Evidence                []repoconfig.ReviewEvidenceEntry `json:"evidence,omitempty"`
	VerifyRecipe            string                           `json:"verify_recipe"`
	BootstrapDeclared       bool                             `json:"bootstrap_declared"`
	TimeoutSeconds          float64                          `json:"timeout_seconds,omitempty"`
	DeadlineUnix            float64                          `json:"deadline_unix,omitempty"`
	VerifyOnly              bool                             `json:"verify_only,omitempty"`
	CancelPath              string                           `json:"cancel_path,omitempty"`
	Standalone              bool                             `json:"standalone,omitempty"`
	AuthenticateGit         bool                             `json:"authenticate_git,omitempty"`
	ExpectedRepoURL         string                           `json:"expected_repo_url,omitempty"`
	CloneDestination        string                           `json:"clone_destination,omitempty"`
	GuardConfiguration      bool                             `json:"guard_configuration,omitempty"`
	PlatformCommand         bool                             `json:"platform_command,omitempty"`
}

// Only this bounded nonsecret control metadata enters process arguments.
type reviewWorkspacePreparationControl struct {
	WorkDir        string  `json:"work_dir"`
	DeadlineUnix   float64 `json:"deadline_unix,omitempty"`
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
	CancelPath     string  `json:"cancel_path,omitempty"`
}
