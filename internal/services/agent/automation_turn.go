package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

// Per-target continuity: the orchestrator turn path (design doc 125,
// "Workspace Preparation", "Prompt", "Completion"). A per-target automation
// turn runs in the pull request's owned session. Before the agent starts,
// the workspace is checked out at the run's exact head, the dependency
// inputs are fingerprinted, and the visible user message is replaced by
// the rendered continuation prompt. Every attempt end writes a run-keyed
// result marker in the same transaction as the session status write, and
// every checkpoint of an owned session is published together with its
// provenance on the generation.

// AutomationTurnStore is the store surface the turn path needs. It is
// implemented by the automations package over the db stores; the agent
// package does not import db.
type AutomationTurnStore interface {
	LoadRun(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error)
	LoadTarget(ctx context.Context, orgID, targetID uuid.UUID) (models.AutomationTarget, error)
	LoadGeneration(ctx context.Context, orgID, targetID uuid.UUID, generation int) (models.AutomationTargetSession, error)
	ListCompletedTurnSummaries(ctx context.Context, orgID, targetID uuid.UUID, generation, limit int) ([]models.AutomationTurnSummary, error)
	RecordTurnWorkspace(ctx context.Context, orgID, runID, lockToken uuid.UUID, ws models.AutomationTurnWorkspace) (bool, error)
	UpdateTurnPrompt(ctx context.Context, orgID, runID uuid.UUID, content string) (bool, error)
	TagAssistantMessage(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runID uuid.UUID) error
	PublishCheckpointWithProvenance(ctx context.Context, orgID, sessionID, runID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, stopReason models.RuntimeStopReason, provenance models.CheckpointProvenance) (bool, error)
	// ReleaseTurnHold releases the owned session's turn hold under the
	// attempt fence; owned is false when a later turn owns the session.
	ReleaseTurnHold(ctx context.Context, orgID, sessionID, runID, lockToken uuid.UUID) (owned bool, destroyNow bool, containerID string, err error)
	// FailThreadTurn drives the primary thread terminal under the fence.
	FailThreadTurn(ctx context.Context, orgID, runID, lockToken, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) (bool, error)
	// EndAttempt runs fn in one transaction with a transaction-bound
	// session store, so the attempt's session status write and its result
	// marker commit together.
	EndAttempt(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx, sessions SessionStore) error) error
	WriteResult(ctx context.Context, tx pgx.Tx, orgID, jobID uuid.UUID, result *models.AutomationRunResult) (bool, error)
	RecordTurnDuration(ctx context.Context, tx pgx.Tx, orgID, runID, lockToken uuid.UUID, durationMS int) (bool, error)
	CompletePreflight(ctx context.Context, orgID, runID, lockToken uuid.UUID, outcome models.AutomationRunOutcomeReason, summary string) (bool, error)
	// LockAttempt locks the attempt's run and job rows in tx and reports
	// whether the lease is still held; a concurrent claim waits for tx.
	LockAttempt(ctx context.Context, tx pgx.Tx, orgID, runID, lockToken uuid.UUID) (bool, error)
	RecordTurnBaseline(ctx context.Context, orgID, runID, lockToken uuid.UUID, baselineHeadSHA *string) (bool, error)
	// EndInterruptedAttempt restores the session's pre-turn status for a
	// drained attempt in one fenced statement; no marker is written.
	EndInterruptedAttempt(ctx context.Context, orgID, runID, sessionID, lockToken uuid.UUID, status models.SessionStatus) (bool, error)
	// CompleteThreadTurn returns the primary thread to idle under the
	// attempt fence, so a worker that lost its lease cannot overwrite the
	// thread state of a turn another run has since claimed.
	CompleteThreadTurn(ctx context.Context, orgID, runID, lockToken, threadID uuid.UUID, turn int, agentSessionID string) (bool, error)
	RecordContinuationFallback(ctx context.Context, orgID, runID, lockToken uuid.UUID, reason models.AutomationRunContinuationReason, baselineHeadSHA *string) (bool, error)
	RetireGeneration(ctx context.Context, orgID, generationID uuid.UUID, reason models.AutomationTargetRetiredReason) error
}

// SetAutomationTurnStore enables the per-target turn path.
func (o *Orchestrator) SetAutomationTurnStore(store AutomationTurnStore) {
	o.automationTurns = store
}

var (
	// ErrAutomationStaleHead means the run's head is not reachable from the
	// repository any more (force-pushed past before dispatch).
	ErrAutomationStaleHead = errors.New("automation turn head is unreachable")
	// ErrAutomationUnsupportedWorkspace means the checkout contains a nested
	// repository or Git LFS content, which this version does not support.
	ErrAutomationUnsupportedWorkspace = errors.New("automation turn workspace is unsupported")
	// ErrAutomationAttemptLost means the attempt fence rejected the end-of-
	// attempt write: another worker holds the job lease.
	ErrAutomationAttemptLost = errors.New("automation turn attempt lost its lease")
	// ErrAutomationTargetClosed means the pull request closed after the run
	// was reserved; the turn ends as a pr_closed preflight.
	ErrAutomationTargetClosed = errors.New("automation turn target is no longer open")
	// errAutomationTurnStoreMissing is returned when a per-target turn
	// reaches an orchestrator without the turn store configured.
	errAutomationTurnStoreMissing = errors.New("automation turn store is not configured")
)

type automationTurnContextKey struct{}
type automationTurnStateKey struct{}

// WithAutomationTurn marks ctx as running the given per-target turn. The
// worker sets it for a fresh generation's run_agent job, which has no
// options parameter; ContinueSession reads the same options from its
// ContinueSessionOptions.
func WithAutomationTurn(ctx context.Context, opts *AutomationTurnContinueOptions) context.Context {
	if opts == nil {
		return ctx
	}
	return context.WithValue(ctx, automationTurnContextKey{}, opts)
}

// AutomationTurnFromContext returns the per-target turn options carried by
// ctx, if any.
func AutomationTurnFromContext(ctx context.Context) *AutomationTurnContinueOptions {
	opts, _ := ctx.Value(automationTurnContextKey{}).(*AutomationTurnContinueOptions)
	return opts
}

func withAutomationTurnState(ctx context.Context, state *automationTurnState) context.Context {
	return context.WithValue(ctx, automationTurnStateKey{}, state)
}

// automationTurnStateFromContext returns the in-progress turn for ctx. It
// is carried on the context so the attempt-end handlers, which are shared
// with ordinary sessions, find it without signature changes.
func automationTurnStateFromContext(ctx context.Context) *automationTurnState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(automationTurnStateKey{}).(*automationTurnState)
	return state
}

// automationTurnState is the per-attempt state of a per-target turn.
type automationTurnState struct {
	opts       *AutomationTurnContinueOptions
	run        models.AutomationRun
	generation models.AutomationTargetSession
	lockToken  uuid.UUID
	jobID      uuid.UUID

	headSHA           string
	baseBranch        string
	pullRequestNumber int
	repository        string
	pullRequestURL    string
	baselineSHA       string
	baseSHA           string
	isPush            bool
	event             models.AutomationGitHubEvent
	// goal is the automation's trusted goal; eventContext is the trigger's
	// pull-request-derived context, rendered as untrusted data.
	goal         string
	eventContext string

	fingerprint     *string
	dependencyState string
	nativeContext   bool
	// nativeContextLost is set when a native resume failed after the
	// checkpoint restored and the turn re-rendered its prompt for embedded
	// history.
	nativeContextLost bool
	prompt            string

	startedAt      time.Time
	agentStartedAt time.Time
	agentEndedAt   time.Time
	restoreBytes   *int64
	restoreMS      *int

	checkpointKey       string
	checkpointPublished bool
	ended               bool

	// pendingPreflight is a preflight outcome discovered during setup. It
	// is applied by the deferred hook after the sandbox and turn hold are
	// released, so the session is never claimable while this attempt still
	// holds its container.
	pendingPreflight        models.AutomationRunOutcomeReason
	pendingPreflightSummary string
}

// markAgentStarted records when the agent process started, so the turn
// duration covers agent execution only.
func (s *automationTurnState) markAgentStarted() {
	if s != nil {
		s.agentStartedAt = time.Now().UTC()
	}
}

// markAgentEnded records when agent execution, including its retries,
// ended; the end-of-turn snapshot is not agent time.
func (s *automationTurnState) markAgentEnded() {
	if s != nil {
		s.agentEndedAt = time.Now().UTC()
	}
}

// agentDurationMS is the agent execution time, or -1 when the agent never
// ran (a setup failure is not agent time).
func (s *automationTurnState) agentDurationMS() int {
	if s == nil || s.agentStartedAt.IsZero() {
		return -1
	}
	end := s.agentEndedAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	return int(end.Sub(s.agentStartedAt) / time.Millisecond)
}

// deferPreflight records a preflight outcome for the deferred hook.
func (s *automationTurnState) deferPreflight(outcome models.AutomationRunOutcomeReason, summary string) {
	s.pendingPreflight = outcome
	s.pendingPreflightSummary = summary
}

func (s *automationTurnState) mode() models.AutomationRunContinuationMode {
	if s == nil || s.opts == nil {
		return ""
	}
	return s.opts.ContinuationMode
}

func (s *automationTurnState) reconstructed() bool {
	return s.mode() == models.AutomationRunContinuationReconstructed
}

func (s *automationTurnState) continued() bool {
	return s.mode() == models.AutomationRunContinuationContinued
}

var (
	gitSHAPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	gitRefNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// automationRunGitHubSnapshot is the github block of a run's config
// snapshot, read for the delivered head and pull request identity.
type automationRunGitHubSnapshot struct {
	Repository        string `json:"repository"`
	PullRequestNumber int    `json:"pull_request_number"`
	PullRequestURL    string `json:"pull_request_url"`
	HeadSHA           string `json:"head_sha"`
	BaseBranch        string `json:"base_branch"`
}

// beginAutomationTurn loads and validates the attempt's identity: the run
// is executing under this job's lease and attempt, it belongs to the
// session, and its head is a well-formed SHA. The returned context carries
// the state for the attempt-end handlers.
func (o *Orchestrator) beginAutomationTurn(ctx context.Context, session *models.Session, opts *AutomationTurnContinueOptions) (context.Context, *automationTurnState, error) {
	if opts == nil {
		return ctx, nil, nil
	}
	if o.automationTurns == nil {
		return ctx, nil, errAutomationTurnStoreMissing
	}
	lockToken, hasToken := jobctx.LockTokenFromContext(ctx)
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	if !hasToken || !hasJob || lockToken == uuid.Nil || jobID == uuid.Nil {
		return ctx, nil, errors.New("automation turn requires a job lease")
	}
	run, err := o.automationTurns.LoadRun(ctx, session.OrgID, opts.RunID)
	if err != nil {
		return ctx, nil, fmt.Errorf("load automation run: %w", err)
	}
	if run.DispatchState == nil || *run.DispatchState != models.AutomationRunDispatchExecuting {
		return ctx, nil, fmt.Errorf("automation run %s is not executing", run.ID)
	}
	if run.AttemptLockToken == nil || *run.AttemptLockToken != lockToken || run.JobID == nil || *run.JobID != jobID {
		return ctx, nil, fmt.Errorf("%w: run %s is claimed by another attempt", ErrAutomationAttemptLost, run.ID)
	}
	if run.SessionID == nil || *run.SessionID != session.ID || run.ThreadID == nil || run.TurnNumber == nil || run.TargetID == nil || run.TargetGeneration == nil {
		return ctx, nil, fmt.Errorf("automation run %s is not reserved for session %s", run.ID, session.ID)
	}
	generation, err := o.automationTurns.LoadGeneration(ctx, session.OrgID, *run.TargetID, *run.TargetGeneration)
	if err != nil {
		return ctx, nil, fmt.Errorf("load automation generation: %w", err)
	}
	var snapshot struct {
		GitHub automationRunGitHubSnapshot  `json:"github"`
		Event  models.AutomationGitHubEvent `json:"github_event"`
	}
	if len(run.ConfigSnapshot) > 0 {
		if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
			return ctx, nil, fmt.Errorf("parse automation run snapshot: %w", err)
		}
	}
	head := strings.TrimSpace(snapshot.GitHub.HeadSHA)
	if run.ResolvedHeadSHA != nil && strings.TrimSpace(*run.ResolvedHeadSHA) != "" {
		head = strings.TrimSpace(*run.ResolvedHeadSHA)
	}
	if opts.HeadSHA != "" && opts.HeadSHA != head {
		return ctx, nil, fmt.Errorf("automation turn head %s does not match the run's head %s", opts.HeadSHA, head)
	}
	if !gitSHAPattern.MatchString(head) {
		return ctx, nil, fmt.Errorf("automation run %s head %q is not a full lowercase SHA", run.ID, head)
	}
	number := snapshot.GitHub.PullRequestNumber
	if opts.PullRequestNumber > 0 {
		number = opts.PullRequestNumber
	}
	if number <= 0 {
		return ctx, nil, fmt.Errorf("automation run %s has no pull request number", run.ID)
	}
	baseBranch := strings.TrimSpace(snapshot.GitHub.BaseBranch)
	if generation.LastBaseRef != nil && strings.TrimSpace(*generation.LastBaseRef) != "" {
		baseBranch = strings.TrimSpace(*generation.LastBaseRef)
	}
	if baseBranch != "" && !gitRefNamePattern.MatchString(baseBranch) {
		return ctx, nil, fmt.Errorf("automation run %s base branch %q is not a valid ref name", run.ID, baseBranch)
	}
	goal, eventContext := splitGoalSnapshot(run.GoalSnapshot)
	state := &automationTurnState{
		opts:              opts,
		run:               run,
		generation:        generation,
		lockToken:         lockToken,
		jobID:             jobID,
		headSHA:           head,
		baseBranch:        baseBranch,
		pullRequestNumber: number,
		repository:        snapshot.GitHub.Repository,
		pullRequestURL:    snapshot.GitHub.PullRequestURL,
		isPush:            run.GitHubAction != nil && *run.GitHubAction == "synchronize",
		event:             snapshot.Event,
		goal:              goal,
		eventContext:      eventContext,
		dependencyState:   "unknown",
		startedAt:         time.Now().UTC(),
	}
	if run.PreviousHeadSHA != nil && gitSHAPattern.MatchString(*run.PreviousHeadSHA) {
		state.baselineSHA = *run.PreviousHeadSHA
	}
	ctx = withAutomationTurnState(ctx, state)
	// Execution-time lifecycle check (design doc 125, "Executing preflight
	// outcomes"): a pull request that closed after the reservation ends the
	// turn as pr_closed. The state is returned so the caller can apply it.
	target, err := o.automationTurns.LoadTarget(ctx, session.OrgID, *run.TargetID)
	if err != nil {
		return ctx, nil, fmt.Errorf("load automation target: %w", err)
	}
	if !target.LifecycleState.AllowsEvent(state.event) {
		return ctx, state, fmt.Errorf("%w: lifecycle is %s", ErrAutomationTargetClosed, target.LifecycleState)
	}
	return ctx, state, nil
}

// goalSnapshotEventMarker separates the automation's goal from the GitHub
// event context the trigger appended to the run's goal snapshot.
const goalSnapshotEventMarker = "\n\nGitHub event context:\n"

// splitGoalSnapshot separates the trusted goal from the pull-request-derived
// event context so only the goal is rendered as instructions.
func splitGoalSnapshot(goalSnapshot string) (goal, eventContext string) {
	goal, eventContext, found := strings.Cut(goalSnapshot, goalSnapshotEventMarker)
	if !found {
		return strings.TrimSpace(goalSnapshot), ""
	}
	return strings.TrimSpace(goal), strings.TrimSpace(eventContext)
}

// automationTurnWorkingBranch is the display-only working branch of a
// per-target session; the checkout is detached.
func automationTurnWorkingBranch(pullRequestNumber int) string {
	return fmt.Sprintf("pr/%d", pullRequestNumber)
}

// prepareAutomationTurnWorkspace checks the workspace out at the run's
// exact head (design doc 125, "Checkout at the exact head", "Clean tree
// and dependency inputs"): clean the tracked tree without touching ignored
// caches, fetch the head by SHA (falling back to the pull request ref only
// to populate it), record the merge-base with the base branch as base_sha,
// check out detached, verify HEAD, and initialize submodules. An
// unreachable head is ErrAutomationStaleHead; a nested repository or LFS
// content is ErrAutomationUnsupportedWorkspace.
func (o *Orchestrator) prepareAutomationTurnWorkspace(ctx context.Context, sandbox *Sandbox, repoURL, token string, state *automationTurnState, log zerolog.Logger) error {
	if state == nil {
		return nil
	}
	remote := repoURL
	if token != "" && repoURL != "" {
		remote = authenticatedGitURL(repoURL, token)
	}
	head := state.headSHA
	if !gitSHAPattern.MatchString(head) {
		return fmt.Errorf("refusing to check out malformed head %q", head)
	}

	// Clean tree: discard tracked changes and untracked files, keep
	// ignored dependency and build caches. The discarded list is bounded
	// and logged.
	if _, _, err := o.execGit(ctx, sandbox, token, "git reset --hard --quiet"); err != nil {
		return fmt.Errorf("reset workspace: %w", err)
	}
	cleanOut, _, err := o.execGit(ctx, sandbox, token, "git clean -fd")
	if err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if discarded := boundedLines(cleanOut, 50); len(discarded) > 0 {
		log.Info().Strs("discarded", discarded).Msg("automation turn discarded untracked files before checkout")
	}

	// Fetch the head by SHA; fall back to the pull request ref to populate
	// the local objects, then require the SHA to be present either way.
	fetchHead := fmt.Sprintf("git fetch --quiet --no-tags '%s' %s", shellEscapeSingleQuote(remote), head)
	if _, stderr, err := o.execGit(ctx, sandbox, token, fetchHead); err != nil {
		log.Info().Str("stderr", stderr).Msg("fetch by SHA failed; falling back to the pull request ref")
		fetchRef := fmt.Sprintf("git fetch --quiet --no-tags '%s' 'pull/%d/head'", shellEscapeSingleQuote(remote), state.pullRequestNumber)
		if _, stderr, err := o.execGit(ctx, sandbox, token, fetchRef); err != nil {
			log.Info().Str("stderr", stderr).Msg("fetch of the pull request ref failed")
		}
	}
	if _, _, err := o.execGit(ctx, sandbox, token, fmt.Sprintf("git cat-file -e %s^{commit}", head)); err != nil {
		return fmt.Errorf("%w: %s", ErrAutomationStaleHead, head)
	}

	// Base SHA: the merge-base with the base branch, the baseline for a full
	// review when no previous head is usable.
	if state.baseBranch != "" {
		fetchBase := fmt.Sprintf("git fetch --quiet --no-tags '%s' '%s'", shellEscapeSingleQuote(remote), shellEscapeSingleQuote(state.baseBranch))
		if _, stderr, err := o.execGit(ctx, sandbox, token, fetchBase); err != nil {
			log.Warn().Str("stderr", stderr).Str("base_branch", state.baseBranch).Msg("failed to fetch the base branch; base sha unavailable")
		} else if out, _, err := o.execGit(ctx, sandbox, token, fmt.Sprintf("git merge-base FETCH_HEAD %s", head)); err != nil {
			log.Warn().Err(err).Msg("failed to compute the merge-base; base sha unavailable")
		} else if base := strings.TrimSpace(out); gitSHAPattern.MatchString(base) {
			state.baseSHA = base
		}
	}

	if _, stderr, err := o.execGit(ctx, sandbox, token, fmt.Sprintf("git checkout --quiet --detach %s", head)); err != nil {
		return fmt.Errorf("checkout head %s: %w (%s)", head, err, stderr)
	}
	out, _, err := o.execGit(ctx, sandbox, token, "git rev-parse HEAD")
	if err != nil {
		return fmt.Errorf("verify head: %w", err)
	}
	if actual := strings.TrimSpace(out); actual != head {
		return fmt.Errorf("verify head: expected %s, got %s", head, actual)
	}
	if out, _, err := o.execGit(ctx, sandbox, token, "git ls-files --error-unmatch .gitmodules 2>/dev/null && echo present || true"); err == nil && strings.Contains(out, "present") {
		if _, stderr, err := o.execGit(ctx, sandbox, token, "git submodule update --init --recursive --quiet"); err != nil {
			return fmt.Errorf("initialize submodules: %w (%s)", err, stderr)
		}
	}
	// Unsupported workspaces: a nested repository or Git LFS content.
	// A registered submodule's .git is a file; an independent nested
	// repository has a .git directory.
	if out, _, err := o.execGit(ctx, sandbox, token, "find . -mindepth 2 -type d -name .git -not -path './.git/*' -print -quit"); err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf("%w: nested repository at %s", ErrAutomationUnsupportedWorkspace, strings.TrimSpace(out))
	}
	if out, _, err := o.execGit(ctx, sandbox, token, "git ls-files -- '.gitattributes' '**/.gitattributes' | head -20 | xargs -r grep -l 'filter=lfs' 2>/dev/null || true"); err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf("%w: git lfs attributes in %s", ErrAutomationUnsupportedWorkspace, strings.TrimSpace(out))
	}
	return nil
}

// execGit runs a shell command in the sandbox and returns stdout and
// stderr, with the installation token redacted. A non-zero exit is an
// error.
func (o *Orchestrator) execGit(ctx context.Context, sandbox *Sandbox, token, cmd string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return stdout.String(), redactGitToken(stderr.String(), token), err
	}
	if exitCode != 0 {
		return stdout.String(), redactGitToken(stderr.String(), token), fmt.Errorf("exit=%d stderr=%s", exitCode, strings.TrimSpace(redactGitToken(stderr.String(), token)))
	}
	return stdout.String(), redactGitToken(stderr.String(), token), nil
}

func boundedLines(text string, limit int) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= limit {
			break
		}
	}
	return lines
}

// automationDependencyManifests are the files whose contents feed the
// dependency input fingerprint.
var automationDependencyManifests = []string{
	"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
	"go.mod", "go.sum", "pyproject.toml", "requirements*.txt",
	"Gemfile", "Gemfile.lock", "Cargo.toml", "Cargo.lock",
	".tool-versions", ".nvmrc", "mise.toml",
}

const automationDependencyManifestLimit = 64

// automationTurnDependencyFingerprint hashes the dependency inputs present
// at the head: manifest and lockfile blobs (tracked files matching the
// manifest list, top level and nested, bounded), the repo-config sandbox
// dependency declarations, and the sandbox image. It is a heuristic for
// "the tool bootstrap from the last checkpoint still applies".
func (o *Orchestrator) automationTurnDependencyFingerprint(ctx context.Context, sandbox *Sandbox, dependencies map[string]string, image string) (string, error) {
	patterns := make([]string, 0, len(automationDependencyManifests)*2)
	for _, m := range automationDependencyManifests {
		patterns = append(patterns, "'"+m+"'", "'**/"+m+"'")
	}
	listOut, _, err := o.execGit(ctx, sandbox, "", "git ls-files -- "+strings.Join(patterns, " "))
	if err != nil {
		return "", fmt.Errorf("list dependency manifests: %w", err)
	}
	files := boundedLines(listOut, automationDependencyManifestLimit)
	sort.Strings(files)
	entries := make([]string, 0, len(files)+len(dependencies)+1)
	if len(files) > 0 {
		quoted := make([]string, 0, len(files))
		for _, f := range files {
			quoted = append(quoted, "'"+shellEscapeSingleQuote(f)+"'")
		}
		hashOut, _, err := o.execGit(ctx, sandbox, "", "git hash-object -- "+strings.Join(quoted, " "))
		if err != nil {
			return "", fmt.Errorf("hash dependency manifests: %w", err)
		}
		hashes := boundedLines(hashOut, automationDependencyManifestLimit)
		if len(hashes) != len(files) {
			return "", fmt.Errorf("hash dependency manifests: expected %d hashes, got %d", len(files), len(hashes))
		}
		for i, f := range files {
			entries = append(entries, "file:"+f+"="+hashes[i])
		}
	}
	depNames := make([]string, 0, len(dependencies))
	for name := range dependencies {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)
	for _, name := range depNames {
		entries = append(entries, "dep:"+name+"="+dependencies[name])
	}
	entries = append(entries, "image:"+image)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

// automationTurnDelta computes the continuation delta (design doc 125,
// "Delta"): from the baseline when it is reachable, else from base_sha with
// a full review. Output is bounded by the prompt renderer.
type automationTurnDelta struct {
	stat       string
	files      []string
	fullReview bool
}

func (o *Orchestrator) computeAutomationTurnDelta(ctx context.Context, sandbox *Sandbox, state *automationTurnState, log zerolog.Logger) automationTurnDelta {
	from := ""
	full := false
	if state.baselineSHA != "" {
		if _, _, err := o.execGit(ctx, sandbox, "", fmt.Sprintf("git cat-file -e %s^{commit}", state.baselineSHA)); err == nil {
			from = state.baselineSHA
		} else {
			log.Info().Str("baseline", state.baselineSHA).Msg("baseline head is unreachable; reviewing from the base sha")
		}
	}
	if from == "" {
		full = true
		from = state.baseSHA
	}
	if from == "" {
		return automationTurnDelta{fullReview: true}
	}
	delta := automationTurnDelta{fullReview: full}
	if out, _, err := o.execGit(ctx, sandbox, "", fmt.Sprintf("git diff --stat=120 %s..%s", from, state.headSHA)); err != nil {
		log.Warn().Err(err).Msg("failed to compute the delta stat")
	} else {
		delta.stat = out
	}
	if out, _, err := o.execGit(ctx, sandbox, "", fmt.Sprintf("git diff --name-only %s..%s", from, state.headSHA)); err != nil {
		log.Warn().Err(err).Msg("failed to list changed files")
	} else {
		delta.files = boundedLines(out, prompts.AutomationTurnChangedFilesLimit+1)
	}
	return delta
}

// renderAutomationTurnPrompt renders the turn's visible user message and
// stores it on the run's message row so the transcript shows what the
// agent saw.
func (o *Orchestrator) renderAutomationTurnPrompt(ctx context.Context, sandbox *Sandbox, session *models.Session, state *automationTurnState, log zerolog.Logger) (string, error) {
	delta := o.computeAutomationTurnDelta(ctx, sandbox, state, log)
	data := prompts.AutomationTurnPromptData{
		Goal:            state.goal,
		EventContext:    state.eventContext,
		TurnNumber:      derefInt(state.run.TurnNumber),
		Mode:            string(state.mode()),
		BaselineSHA:     state.baselineSHA,
		HeadSHA:         state.headSHA,
		BaseSHA:         state.baseSHA,
		BaseBranch:      state.baseBranch,
		NativeContext:   state.continued() && !state.nativeContextLost && session.AgentSessionID != nil && *session.AgentSessionID != "",
		DependencyState: state.dependencyState,
		FullReview:      delta.fullReview,
		DiffStat:        delta.stat,
		ChangedFiles:    delta.files,
	}
	if state.run.ContinuationReason != nil {
		data.ContinuationReason = string(*state.run.ContinuationReason)
	}
	if state.generation.CheckpointHeadSHA != nil && state.baselineSHA == *state.generation.CheckpointHeadSHA &&
		state.generation.CheckpointReviewComplete != nil && !*state.generation.CheckpointReviewComplete {
		data.InterruptedCheckpoint = true
	}
	if state.mode() != models.AutomationRunContinuationFresh && state.run.TargetID != nil && state.run.TargetGeneration != nil {
		summaries, err := o.automationTurns.ListCompletedTurnSummaries(ctx, session.OrgID, *state.run.TargetID, *state.run.TargetGeneration, 5)
		if err != nil {
			log.Warn().Err(err).Msg("failed to load earlier review summaries")
		}
		for _, s := range summaries {
			if data.NativeContext && s.HeadSHA != "" && s.HeadSHA == state.baselineSHA && !data.InterruptedCheckpoint {
				// With native context the baseline turn's findings are
				// already in the agent's memory; embed only the reviews
				// after it. Without native context every review is data.
				continue
			}
			data.Summaries = append(data.Summaries, prompts.AutomationTurnSummary{TurnNumber: s.TurnNumber, HeadSHA: s.HeadSHA, Summary: s.Summary})
		}
	}
	if !state.isPush && state.generation.LastReviewedHeadSHA != nil && *state.generation.LastReviewedHeadSHA == state.headSHA {
		action := ""
		if state.run.GitHubAction != nil {
			action = *state.run.GitHubAction
		}
		data.EventText = fmt.Sprintf("Event %q on %s (%s) at the already-reviewed head %s.", action, state.repository, state.pullRequestURL, state.headSHA)
	}
	prompt := prompts.AutomationTurnPrompt(data)
	updated, err := o.automationTurns.UpdateTurnPrompt(ctx, session.OrgID, state.run.ID, prompt)
	if err != nil {
		return "", err
	}
	if !updated {
		log.Warn().Str("run_id", state.run.ID.String()).Msg("automation turn message was not found; the transcript keeps the placeholder prompt")
	}
	state.prompt = prompt
	return prompt, nil
}

// automationTurnDependencyState decides whether the tool bootstrap from the
// checkpoint still applies and returns the prompt's environment note.
func (s *automationTurnState) applyDependencyFingerprint(fingerprint string) (skipBootstrap bool) {
	s.fingerprint = &fingerprint
	coherent := s.continued() && s.generation.CheckpointSnapshotKey != nil && s.generation.CheckpointDependencyFingerprint != nil
	if coherent && *s.generation.CheckpointDependencyFingerprint == fingerprint {
		s.dependencyState = "dependency inputs are unchanged since the last checkpoint; the tool bootstrap was not re-run"
		return true
	}
	if s.continued() {
		s.dependencyState = "dependency inputs changed since the last checkpoint; the environment may be cold and the tool bootstrap was re-run"
		return false
	}
	s.dependencyState = "reconstructed workspace; the environment is cold and the tool bootstrap was re-run"
	return false
}

// publishAutomationCheckpoint publishes a checkpoint of the owned session
// together with its provenance. reviewComplete is true only for the
// turn-complete checkpoint of a successful review.
func (o *Orchestrator) publishAutomationCheckpoint(ctx context.Context, state *automationTurnState, session *models.Session, agentSessionID, snapshotKey string, kind models.CheckpointKind, sizeBytes int64, checkpointedAt time.Time, stopReason models.RuntimeStopReason, reviewComplete bool) (bool, error) {
	if state == nil || snapshotKey == "" {
		return false, nil
	}
	published, err := o.automationTurns.PublishCheckpointWithProvenance(ctx, session.OrgID, session.ID, state.run.ID, state.lockToken, agentSessionID, snapshotKey, kind, checkpointCapabilityForAgent(session.AgentType), sizeBytes, checkpointedAt, stopReason, models.CheckpointProvenance{
		GenerationID:          state.generation.ID,
		HeadSHA:               state.headSHA,
		DependencyFingerprint: state.fingerprint,
		ReviewComplete:        reviewComplete,
	})
	if err != nil {
		return false, err
	}
	// The bootstrap checkpoint carries provenance but is not end-of-attempt
	// evidence: the marker reports only a checkpoint taken at the attempt's
	// end.
	if published && kind != models.CheckpointKindBootstrap {
		state.checkpointKey = snapshotKey
		state.checkpointPublished = true
	}
	return published, nil
}

// endAutomationAttempt writes the attempt's result marker in the same
// transaction as its session status write (design doc 125, "Result
// marker"). write performs the status write on the transaction-bound store.
// The attempt fence rejecting the marker rolls the status write back and
// returns ErrAutomationAttemptLost: a worker that lost its lease must not
// end the turn.
func (o *Orchestrator) endAutomationAttempt(ctx context.Context, state *automationTurnState, session *models.Session, outcome models.AutomationRunResultOutcome, agentSessionID string, write func(sessions SessionStore) error) error {
	if state == nil {
		return errors.New("end automation attempt: no turn state")
	}
	if state.ended {
		return nil
	}
	marker := &models.AutomationRunResult{
		RunID:                 state.run.ID,
		OrgID:                 session.OrgID,
		Attempt:               state.run.Attempt,
		AttemptLockToken:      state.lockToken,
		ThreadID:              *state.run.ThreadID,
		TurnNumber:            *state.run.TurnNumber,
		Outcome:               outcome,
		ReviewComplete:        outcome == models.AutomationRunResultTurnCompleted,
		CheckpointPublished:   state.checkpointPublished,
		NativeContext:         state.nativeContext,
		DependencyFingerprint: state.fingerprint,
	}
	if state.checkpointPublished {
		key := state.checkpointKey
		head := state.headSHA
		marker.CheckpointKey = &key
		marker.CheckpointHeadSHA = &head
	}
	if agentSessionID != "" {
		id := agentSessionID
		marker.AgentSessionID = &id
	}
	duration := state.agentDurationMS()
	// The marker is written first, so this transaction takes the run and
	// job locks before the session lock. A recovery takes target, job, run,
	// then session; writing the session first would invert that and let the
	// two deadlock, which would roll back a successful turn's result.
	err := o.automationTurns.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions SessionStore) error {
		written, err := o.automationTurns.WriteResult(ctx, tx, session.OrgID, state.jobID, marker)
		if err != nil {
			return fmt.Errorf("write automation run result: %w", err)
		}
		if !written {
			return ErrAutomationAttemptLost
		}
		if err := write(sessions); err != nil {
			return err
		}
		if duration >= 0 {
			if _, err := o.automationTurns.RecordTurnDuration(ctx, tx, session.OrgID, state.run.ID, state.lockToken, duration); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.ended = true
	return nil
}

// runPendingPreflight applies a preflight outcome recorded during setup.
// It runs from a deferred hook registered before the sandbox is created,
// so the sandbox and turn hold are already released when the run ends and
// the session becomes claimable. A completion failure is returned so the
// job fails and retries instead of leaving the run executing behind a
// successful job.
func (o *Orchestrator) runPendingPreflight(ctx context.Context, state *automationTurnState, session *models.Session, log zerolog.Logger) error {
	if state == nil || state.pendingPreflight == "" || state.ended {
		return nil
	}
	outcome := state.pendingPreflight
	done, err := o.automationTurns.CompletePreflight(context.WithoutCancel(ctx), session.OrgID, state.run.ID, state.lockToken, outcome, state.pendingPreflightSummary)
	if err != nil {
		return fmt.Errorf("complete automation turn preflight %s: %w", outcome, err)
	}
	state.pendingPreflight = ""
	if !done {
		log.Warn().Str("run_id", state.run.ID.String()).Str("outcome", string(outcome)).Msg("automation turn preflight was not applied: the attempt lost its lease")
		return nil
	}
	state.ended = true
	log.Info().Str("run_id", state.run.ID.String()).Str("outcome", string(outcome)).Msg("automation turn ended before the agent started")
	return nil
}

// releaseInheritedContainer destroys a container an earlier attempt of
// the same session left recorded (a crash mid-turn), so a preflight that
// ends the run before any sandbox is created never releases the session
// while a stale container still holds it. The destroy is authorized
// under the attempt's lock: the run and job rows stay locked from the
// authorization through the destroy and the CAS clear, so a takeover
// (which locks the same job row) waits, and a worker whose lease is gone
// is refused before it touches anything. A container recorded on another
// node is only cleared when that node is known dead; otherwise the
// attempt yields to the node that owns it. The release follows the turn
// hold's own protocol (FinalizeContainerDestroy, then Destroy): the CAS
// clear refuses a container a preview or another runtime holds, and it
// commits before the destroy so a reader that arrives after the commit
// finds no container to attach to instead of a dying one. A destroy
// failure after the commit leaves an unreferenced container for the
// sandbox reaper, never a recorded id pointing at a dead one.
func (o *Orchestrator) releaseInheritedContainer(ctx context.Context, state *automationTurnState, session *models.Session, log zerolog.Logger) error {
	if session.ContainerID == nil || *session.ContainerID == "" {
		return nil
	}
	recorded := *session.ContainerID
	recordedNode := ""
	if session.WorkerNodeID != nil {
		recordedNode = *session.WorkerNodeID
	}
	onDeadNode := false
	if recordedNode != "" && recordedNode != o.nodeID {
		deadNode, ok := jobctx.DeadTargetNodeFromContext(ctx)
		if !ok || deadNode != recordedNode {
			return fmt.Errorf("%w: inherited sandbox %s is recorded on node %s", ErrSandboxOnDifferentNode, recorded, recordedNode)
		}
		onDeadNode = true
	}
	err := o.automationTurns.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions SessionStore) error {
		owned, err := o.automationTurns.LockAttempt(ctx, tx, session.OrgID, state.run.ID, state.lockToken)
		if err != nil {
			return err
		}
		if !owned {
			return ErrAutomationAttemptLost
		}
		cleared, err := sessions.ClearContainerID(ctx, session.OrgID, session.ID, recorded)
		if err != nil {
			return fmt.Errorf("clear inherited sandbox: %w", err)
		}
		if !cleared {
			return fmt.Errorf("inherited sandbox %s is held by another owner", recorded)
		}
		return nil
	})
	if err != nil {
		return err
	}
	session.ContainerID = nil
	session.WorkerNodeID = nil
	if !onDeadNode {
		if err := o.provider.Destroy(context.WithoutCancel(ctx), &Sandbox{ID: recorded, Provider: o.provider.Name()}); err != nil {
			log.Warn().Err(err).Str("container_id", recorded).Msg("failed to destroy the inherited sandbox after clearing it; the sandbox reaper owns it")
		}
	}
	log.Info().Str("container_id", recorded).Bool("dead_node", onDeadNode).Msg("released the container an earlier attempt left behind")
	return nil
}

// endClosedTargetTurn applies the pr_closed preflight discovered before
// anything was allocated for this attempt, after releasing any container
// an earlier attempt left behind.
func (o *Orchestrator) endClosedTargetTurn(ctx context.Context, state *automationTurnState, session *models.Session, log zerolog.Logger) error {
	if err := o.releaseInheritedContainer(ctx, state, session, log); err != nil {
		return err
	}
	state.deferPreflight(models.AutomationRunOutcomePRClosed, "the pull request is no longer open")
	return o.runPendingPreflight(ctx, state, session, log)
}

// fallbackToEmbeddedHistory re-renders the turn's prompt when the native
// resume failed after the checkpoint restored: the agent starts without
// its memory, so native context is reported absent, the baseline moves
// to the last completed review (persisted on the run), and every earlier
// review is embedded as bounded data.
func (o *Orchestrator) fallbackToEmbeddedHistory(ctx context.Context, sandbox *Sandbox, session *models.Session, state *automationTurnState, log zerolog.Logger) (string, error) {
	var baseline *string
	if state.generation.LastReviewedHeadSHA != nil && gitSHAPattern.MatchString(*state.generation.LastReviewedHeadSHA) {
		b := *state.generation.LastReviewedHeadSHA
		baseline = &b
	}
	recorded, err := o.automationTurns.RecordTurnBaseline(ctx, session.OrgID, state.run.ID, state.lockToken, baseline)
	if err != nil {
		return "", err
	}
	if !recorded {
		return "", fmt.Errorf("%w: baseline was not recorded", ErrAutomationAttemptLost)
	}
	state.nativeContextLost = true
	state.run.PreviousHeadSHA = baseline
	state.baselineSHA = ""
	if baseline != nil {
		state.baselineSHA = *baseline
	}
	log.Warn().Str("run_id", state.run.ID.String()).Msg("native resume failed after the checkpoint restored; continuing with embedded history from the last completed review")
	return o.renderAutomationTurnPrompt(ctx, sandbox, session, state, log)
}

// fallbackToReconstruction turns a continued turn whose checkpoint could
// not be restored into a reconstructed turn in the same session (readiness
// "rebuild" with restore_failed). The run records the fallback so the
// marker and the run row agree.
func (o *Orchestrator) fallbackToReconstruction(ctx context.Context, state *automationTurnState, session *models.Session, cause error, log zerolog.Logger) error {
	// Native context is gone with the checkpoint: the baseline is the last
	// completed review, or none.
	var baseline *string
	if state.generation.LastReviewedHeadSHA != nil && gitSHAPattern.MatchString(*state.generation.LastReviewedHeadSHA) {
		b := *state.generation.LastReviewedHeadSHA
		baseline = &b
	}
	recorded, err := o.automationTurns.RecordContinuationFallback(ctx, session.OrgID, state.run.ID, state.lockToken, models.AutomationRunContinuationReasonRestoreFailed, baseline)
	if err != nil {
		return err
	}
	if !recorded {
		return fmt.Errorf("%w: continuation fallback was not recorded", ErrAutomationAttemptLost)
	}
	reason := models.AutomationRunContinuationReasonRestoreFailed
	mode := models.AutomationRunContinuationReconstructed
	state.run.ContinuationReason = &reason
	state.run.ContinuationMode = &mode
	state.run.PreviousHeadSHA = baseline
	state.opts.ContinuationMode = mode
	state.baselineSHA = ""
	if baseline != nil {
		state.baselineSHA = *baseline
	}
	state.restoreBytes = nil
	log.Warn().Err(cause).Str("run_id", state.run.ID.String()).Msg("checkpoint restore failed; rebuilding the workspace in the same session")
	return nil
}

// endInterruptedAutomationTurn restores the pre-turn status of an
// interrupted (drained) attempt without a marker, in one statement that
// locks and validates the attempt's run and job rows, so a paused worker
// whose job was reclaimed cannot reset the next attempt's session. Returns
// ErrAutomationAttemptLost when the fence rejects it.
func (o *Orchestrator) endInterruptedAutomationTurn(ctx context.Context, state *automationTurnState, session *models.Session, fallbackStatus models.SessionStatus) error {
	updated, err := o.automationTurns.EndInterruptedAttempt(ctx, session.OrgID, state.run.ID, session.ID, state.lockToken, fallbackStatus)
	if err != nil {
		return err
	}
	if !updated {
		return ErrAutomationAttemptLost
	}
	return nil
}

// automationTurnResultDiff disables session diff collection for per-target
// turns: publish_policy is none and the checkout is detached, so a session
// diff is meaningless.
func automationTurnResultDiff(state *automationTurnState, result *models.SessionResult) {
	if state == nil || result == nil {
		return
	}
	result.Diff = nil
	result.DiffBaseCommitSHA = nil
	result.DiffHeadCommitSHA = nil
	result.DiffWorkspaceDirty = false
	result.DiffCollectedAt = nil
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// errAutomationRepositoryUnavailable wraps a failed repository or
// installation lookup for a per-target turn.
var errAutomationRepositoryUnavailable = errors.New("automation turn repository is unavailable")

// prepareAutomationTurnRestoredWorkspace prepares a checkpoint-restored
// workspace for the turn: resolves the repository and its installation
// token, checks out the run's head, and applies the repository's sandbox
// dependencies unless the dependency fingerprint proves the checkpoint's
// bootstrap still applies.
func (o *Orchestrator) prepareAutomationTurnRestoredWorkspace(ctx context.Context, session *models.Session, sandbox *Sandbox, sandboxCfg SandboxConfig, state *automationTurnState, log zerolog.Logger) error {
	if session.RepositoryID == nil {
		return fmt.Errorf("%w: session has no repository", errAutomationRepositoryUnavailable)
	}
	repo, err := o.repositories.GetByID(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		return fmt.Errorf("%w: %v", errAutomationRepositoryUnavailable, err)
	}
	token, err := o.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return fmt.Errorf("%w: installation token: %v", errAutomationRepositoryUnavailable, err)
	}
	if err := o.prepareAutomationTurnWorkspace(ctx, sandbox, repo.CloneURL, token, state, log); err != nil {
		return err
	}
	return o.prepareAutomationTurnRepository(ctx, sandbox, sandboxCfg, state, log)
}

// prepareAutomationTurnRepository fingerprints the dependency inputs at the
// checked-out head, decides whether the checkpoint's tool bootstrap still
// applies, applies the repository's sandbox dependencies and bootstrap
// commands accordingly, and records the workspace facts on the run.
func (o *Orchestrator) prepareAutomationTurnRepository(ctx context.Context, sandbox *Sandbox, sandboxCfg SandboxConfig, state *automationTurnState, log zerolog.Logger) error {
	cfg := readSandboxRepoConfig(ctx, o.provider, sandbox, sandboxCfg.WorkDir, log)
	fingerprint, err := o.automationTurnDependencyFingerprint(ctx, sandbox, cfg.Dependencies, sandboxCfg.Image)
	if err != nil {
		log.Warn().Err(err).Msg("failed to fingerprint dependency inputs; treating the environment as cold")
		state.fingerprint = nil
		state.dependencyState = "dependency inputs could not be fingerprinted; the environment may be cold and the tool bootstrap was re-run"
	}
	skip := false
	if err == nil {
		skip = state.applyDependencyFingerprint(fingerprint)
	}
	if _, err := prepareSandboxRepository(ctx, o.provider, sandbox, sandboxCfg.WorkDir, log, skip); err != nil {
		return err
	}
	readyMS := int(time.Since(state.startedAt) / time.Millisecond)
	state.restoreMS = &readyMS
	recorded, err := o.automationTurns.RecordTurnWorkspace(ctx, state.run.OrgID, state.run.ID, state.lockToken, models.AutomationTurnWorkspace{
		BaseSHA:              state.baseSHA,
		WorkerNodeID:         o.nodeID,
		RestoreSnapshotBytes: state.restoreBytes,
		RestoreDurationMS:    state.restoreMS,
	})
	if err != nil {
		return err
	}
	if !recorded {
		return fmt.Errorf("%w: workspace facts were not recorded", ErrAutomationAttemptLost)
	}
	return nil
}

// failAutomationTurnSetup handles a setup failure of a continued or
// reconstructed turn: a preflight outcome is recorded for the deferred
// hook (applied once the sandbox is released) and the job finishes; an
// unsupported workspace retires the generation and fails the attempt;
// anything else fails the attempt.
func (o *Orchestrator) failAutomationTurnSetup(ctx context.Context, session *models.Session, opts *ContinueSessionOptions, sandbox *Sandbox, state *automationTurnState, cause error, log zerolog.Logger) error {
	if outcome, summary, ok := automationPreflightOutcome(cause); ok {
		log.Info().Err(cause).Str("outcome", string(outcome)).Msg("automation turn cannot start; ending after cleanup")
		state.deferPreflight(outcome, summary)
		return nil
	}
	if errors.Is(cause, ErrAutomationUnsupportedWorkspace) {
		if err := o.automationTurns.RetireGeneration(context.WithoutCancel(ctx), session.OrgID, state.generation.ID, models.AutomationTargetRetiredUnsupportedWorkspace); err != nil {
			log.Warn().Err(err).Msg("failed to retire the generation for an unsupported workspace")
		}
	}
	return o.failContinueSessionError(ctx, session, opts, cause, log)
}

// failAutomationTurnRunSetup is failAutomationTurnSetup for a fresh turn's
// run_agent path.
func (o *Orchestrator) failAutomationTurnRunSetup(ctx context.Context, run *models.Session, sandbox *Sandbox, state *automationTurnState, cause error, log zerolog.Logger) error {
	if outcome, summary, ok := automationPreflightOutcome(cause); ok {
		log.Info().Err(cause).Str("outcome", string(outcome)).Msg("automation turn cannot start; ending after cleanup")
		state.deferPreflight(outcome, summary)
		return nil
	}
	if errors.Is(cause, ErrAutomationUnsupportedWorkspace) {
		if err := o.automationTurns.RetireGeneration(context.WithoutCancel(ctx), run.OrgID, state.generation.ID, models.AutomationTargetRetiredUnsupportedWorkspace); err != nil {
			log.Warn().Err(err).Msg("failed to retire the generation for an unsupported workspace")
		}
	}
	o.failRun(ctx, run, cause.Error())
	return cause
}

// automationPreflightOutcome maps a preparation failure to its executing
// preflight outcome, if it has one.
func automationPreflightOutcome(err error) (models.AutomationRunOutcomeReason, string, bool) {
	switch {
	case errors.Is(err, ErrAutomationStaleHead):
		return models.AutomationRunOutcomeStaleHead, "the pull request head is no longer reachable", true
	case errors.Is(err, errAutomationRepositoryUnavailable):
		return models.AutomationRunOutcomeRepositoryUnavailable, err.Error(), true
	case errors.Is(err, ErrAutomationTargetClosed):
		return models.AutomationRunOutcomePRClosed, "the pull request is no longer open", true
	default:
		return "", "", false
	}
}

// endCancelledAutomationTurn ends a cancelled attempt: the checkpoint, when
// one was taken, is published with provenance marking the review
// incomplete, and the session returns to idle with the turn counted so the
// next turn is numbered after it, in the same transaction as the cancelled
// marker.
func (o *Orchestrator) endCancelledAutomationTurn(ctx context.Context, state *automationTurnState, session *models.Session, agentSessionID, snapshotKey string, snapshotSize int64, turnNumber int, activityExecution *activityPhaseExecution, log zerolog.Logger) {
	if snapshotKey != "" {
		if _, err := o.publishAutomationCheckpoint(ctx, state, session, agentSessionID, snapshotKey, models.CheckpointKindGracefulStop, snapshotSize, time.Now().UTC(), models.RuntimeStopReasonUserCancel, false); err != nil {
			log.Warn().Err(err).Msg("failed to publish cancelled automation turn checkpoint with provenance")
		}
	}
	installedKey := ""
	if state.checkpointPublished {
		installedKey = state.checkpointKey
	}
	if err := o.endAutomationAttempt(ctx, state, session, models.AutomationRunResultCancelled, agentSessionID, func(sessions SessionStore) error {
		return sessions.UpdateTurnComplete(ctx, session.OrgID, session.ID, turnNumber, nil, agentSessionID, installedKey)
	}); err != nil {
		log.Error().Err(err).Msg("failed to end cancelled automation turn")
		return
	}
	if session.PrimaryThreadID != nil && *session.PrimaryThreadID != uuid.Nil {
		o.completeAutomationThreadTurn(ctx, state, session.OrgID, *session.PrimaryThreadID, turnNumber, agentSessionID, log)
	}
	completeActivityPhaseDetached(activityExecution, models.ActivityPhaseStatusCancelled, models.ActivityPhaseBoundaryCancelled, log)
	log.Info().Int("turn", turnNumber).Msg("cancelled automation turn ended")
}

// releaseAutomationTurnHold releases an owned session's turn hold under the
// attempt fence. owned is false when the attempt is no longer ours, and the
// caller must then leave the hold and the container to the turn that owns
// them.
func (o *Orchestrator) releaseAutomationTurnHold(ctx context.Context, state *automationTurnState, orgID, sessionID uuid.UUID) (owned bool, destroyNow bool, containerID string, err error) {
	return o.automationTurns.ReleaseTurnHold(ctx, orgID, sessionID, state.run.ID, state.lockToken)
}

// failAutomationThreadTurn drives an owned session's primary thread
// terminal under the attempt fence, so a worker that lost its attempt
// cannot fail a thread a later turn is running on.
func (o *Orchestrator) failAutomationThreadTurn(ctx context.Context, state *automationTurnState, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult, log zerolog.Logger) {
	written, err := o.automationTurns.FailThreadTurn(ctx, orgID, state.run.ID, state.lockToken, threadID, status, result)
	if err != nil {
		log.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to drive the automation turn's primary thread terminal")
		return
	}
	if !written {
		log.Warn().Str("thread_id", threadID.String()).Msg("automation turn thread failure was fenced out: the attempt is no longer ours")
	}
}

// completeAutomationThreadTurn returns a per-target turn's primary thread
// to idle under the attempt fence. An unfenced write would let a worker
// that paused past its lease overwrite the status, turn number, and
// provider session id of a turn another run has since claimed; a
// fenced-out write is not an error, because the completion that took the
// attempt away already left the thread as its own turn needs it.
func (o *Orchestrator) completeAutomationThreadTurn(ctx context.Context, state *automationTurnState, orgID, threadID uuid.UUID, turnNumber int, agentSessionID string, log zerolog.Logger) {
	if o.automationTurns == nil || state == nil {
		return
	}
	written, err := o.automationTurns.CompleteThreadTurn(ctx, orgID, state.run.ID, state.lockToken, threadID, turnNumber, agentSessionID)
	if err != nil {
		log.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to return the automation turn's primary thread to idle")
		return
	}
	if !written {
		log.Warn().Str("thread_id", threadID.String()).Msg("automation turn thread release was fenced out: the attempt is no longer ours")
	}
}

// automationTurnSnapshotKey names one publication of an owned session's
// checkpoint: the attempt that took it and the time it was taken.
func automationTurnSnapshotKey(orgID, sessionID, runID uuid.UUID, attempt int, at time.Time) string {
	return fmt.Sprintf("snapshots/%s/%s/turns/%s/%d-%d/workspace.tar.zst", orgID, sessionID, runID, attempt, at.UnixNano())
}
