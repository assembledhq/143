package agent

import (
	"context"
	"encoding/json"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

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
