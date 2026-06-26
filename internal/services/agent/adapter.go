// Package agent defines the interfaces and types for running coding agents
// inside sandboxes to fix issues.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// SessionResumeMode declares how an adapter handles continuation turns. The
// orchestrator reads this to decide whether to ship a bare follow-up message
// (and let the agent CLI load its own conversation history) or to embed
// conversation history in the prompt itself.
type SessionResumeMode int

const (
	// ResumeUnsupported: adapter has no headless resume mechanism. Continuation
	// turns rely on the restored sandbox filesystem state. Adapters in this
	// mode may set result.AgentSessionID for observability, but the value is
	// never fed back into the CLI.
	ResumeUnsupported SessionResumeMode = iota

	// ResumeBySessionID: adapter resumes a specific prior session by ID
	// captured from that session's stream output. Adapters in this mode MUST:
	//   1. Set result.AgentSessionID from a stream event during Execute().
	//   2. When prompt.Continuation && prompt.ResumeSessionID != "", construct
	//      a deterministic resume command using that ID.
	//   3. When prompt.Continuation && prompt.ResumeSessionID == "", run a
	//      fresh exec from prompt.SystemPrompt + prompt.UserPrompt. Adapters
	//      MUST NOT fall back to non-deterministic flags like --last,
	//      --continue, or --resume latest, which pick up whatever session
	//      happens to be newest in the local agent storage.
	ResumeBySessionID
)

// AgentAdapter is the contract that all coding agent integrations implement.
// Each adapter knows how to prepare a prompt and execute a specific agent CLI
// (e.g., Claude Code, Codex) inside a sandbox.
type AgentAdapter interface {
	// Name returns the agent identifier (e.g., "claude_code").
	Name() models.AgentType

	// PreparePrompt constructs the prompt and instructions for the agent
	// based on the issue context and org settings.
	PreparePrompt(ctx context.Context, input *AgentInput) (*AgentPrompt, error)

	// Execute runs the agent inside the provided sandbox and streams log
	// entries to logCh. The channel is closed by the caller after Execute returns.
	Execute(ctx context.Context, sandbox *Sandbox, prompt *AgentPrompt, logCh chan<- LogEntry) (*AgentResult, error)

	// ResumeMode declares how this adapter handles continuation turns. See
	// SessionResumeMode for the per-mode contract.
	ResumeMode() SessionResumeMode
}

// PromptStyle controls how the shared prompt builders render an agent task.
type PromptStyle string

const (
	PromptStyleIssueContext PromptStyle = "issue_context"
	PromptStyleRawTask      PromptStyle = "raw_task"
	PromptStyleAnswerOnly   PromptStyle = "answer_only"
)

// AgentInput contains everything the agent needs to understand and fix an issue.
type AgentInput struct {
	Issue              *models.Issue
	LinkedIssues       []models.SessionIssueSnapshotEntry
	Manual             bool
	PromptStyle        PromptStyle
	UserMessage        string
	Attachments        []AgentAttachment
	RepoURL            string
	RepoBranch         string
	References         []models.SessionInputReference
	Commands           []models.SessionInputCommand
	ReasoningEffort    models.ReasoningEffort
	OrgSettings        json.RawMessage
	TokenMode          models.SessionTokenMode
	ComplexityEstimate *ComplexityEstimate
	ContextDocs        []string // content of CLAUDE.md, AGENTS.md, etc.
	RevisionContext    *RevisionContext
	PMContext          *PMTaskContext        // PM guidance for coding agents
	PMContextJSON      string                // serialized PM context for PM agent runs
	IntegrationSkills  string                // auto-generated CLI skills doc for integration tools
	ContextLimits      *models.ContextLimits // org-specific token limits (nil = use defaults)
}

// AgentAttachment describes a user-uploaded attachment after the server has
// attempted to make it available to the sandbox. LocalPath is set when the
// file was copied successfully; Error is set when the attachment should be
// surfaced to the agent as unavailable.
type AgentAttachment struct {
	OriginalURL  string
	LocalPath    string
	ContentType  string
	Error        string
	MessageIndex int
}

// PMTaskContext carries the PM agent's analysis into the coding agent's prompt.
type PMTaskContext struct {
	Approach      string
	Risk          string
	Reasoning     string
	RelatedIssues []string
	RootCause     string
}

// RevisionContext holds feedback that triggered a revision run.
type RevisionContext struct {
	FormattedFeedback string                             `json:"formatted_feedback"`
	PreviousDiff      string                             `json:"previous_diff"`
	CommentSummary    string                             `json:"comment_summary"`
	RepairAction      models.PullRequestRepairActionType `json:"repair_action,omitempty"`
	RepairContext     *PullRequestRepairContext          `json:"repair_context,omitempty"`
}

type PullRequestRepairContext struct {
	PullRequestNumber int                          `json:"pull_request_number"`
	Repository        string                       `json:"repository"`
	HeadSHA           string                       `json:"head_sha"`
	BaseSHA           string                       `json:"base_sha"`
	MergeState        models.PullRequestMergeState `json:"merge_state"`
	HasConflicts      bool                         `json:"has_conflicts"`
	FailingChecks     []PullRequestFailingCheck    `json:"failing_checks,omitempty"`
}

type PullRequestFailingCheck struct {
	Name        string                          `json:"name"`
	Category    models.PullRequestCheckCategory `json:"category"`
	Summary     string                          `json:"summary,omitempty"`
	DetailsURL  string                          `json:"details_url,omitempty"`
	LogExcerpt  string                          `json:"log_excerpt,omitempty"`
	Annotations []string                        `json:"annotations,omitempty"`
}

func ParseRevisionContext(raw json.RawMessage) (*RevisionContext, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var parsed RevisionContext
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal revision context: %w", err)
	}
	return &parsed, nil
}

// ResolveConflictsGuidance returns the shared instructions injected into both
// the system prompt (new-session path) and the continuation user message
// (resume path) when the repair action is resolve_conflicts. The text warns
// against the most common failure mode we have observed: agents reporting
// `git diff` / `git status` numbers taken mid-merge as if they were the PR's
// net delta, which inflates the apparent change set by the size of the base
// branch's incoming history.
func ResolveConflictsGuidance(baseSHA, headSHA string) string {
	var b strings.Builder
	b.WriteString("### Conflict resolution guidance\n")
	b.WriteString("- Bring the base branch into the head branch (merge or rebase). Resolve only the conflicting hunks; do not silently drop this PR's changes and do not revert changes that came in from the base branch.\n")
	b.WriteString("- While a merge or rebase is in progress, plain `git diff` and `git status` reflect the merge index, not this PR's net delta. Numbers taken from them mid-merge will look enormous (every incoming base-branch change shows up as ours).\n")
	if baseSHA != "" {
		b.WriteString(fmt.Sprintf("- After the merge/rebase commit lands, verify the net delta with `git diff %s...HEAD`. That is the only diff worth reporting back; it should be roughly this PR's original changes plus minimal conflict resolution.\n", baseSHA))
	} else {
		b.WriteString("- After the merge/rebase commit lands, verify the net delta with `git diff <base_sha>...HEAD` against the base SHA above. That is the only diff worth reporting back; it should be roughly this PR's original changes plus minimal conflict resolution.\n")
	}
	b.WriteString("- If the verified net delta is dramatically larger than the original PR (many extra files or thousands of extra lines), stop and investigate before pushing — you have likely captured incoming base-branch history into this branch incorrectly.\n")
	b.WriteString("- Push only after the working tree is clean and the verified net delta looks right.")
	return b.String()
}

func FormatRevisionContextForContinuation(ctx *RevisionContext) string {
	if ctx == nil {
		return ""
	}

	var b strings.Builder
	if ctx.FormattedFeedback != "" || ctx.CommentSummary != "" || ctx.PreviousDiff != "" {
		b.WriteString("## Revision context\n\n")
		if ctx.FormattedFeedback != "" {
			b.WriteString(ctx.FormattedFeedback)
			b.WriteString("\n\n")
		}
		if ctx.CommentSummary != "" {
			b.WriteString("Summary: ")
			b.WriteString(ctx.CommentSummary)
			b.WriteString("\n\n")
		}
		if ctx.PreviousDiff != "" {
			b.WriteString("Previous diff:\n```diff\n")
			b.WriteString(ctx.PreviousDiff)
			b.WriteString("\n```\n\n")
		}
	}

	if ctx.RepairContext != nil {
		b.WriteString("## Pull request repair context\n\n")
		if ctx.RepairAction != "" {
			b.WriteString("Repair action: `")
			b.WriteString(string(ctx.RepairAction))
			b.WriteString("`\n\n")
		}
		b.WriteString(fmt.Sprintf("PR #%d in `%s`.\n", ctx.RepairContext.PullRequestNumber, ctx.RepairContext.Repository))
		b.WriteString(fmt.Sprintf("Head SHA: `%s`\n", ctx.RepairContext.HeadSHA))
		b.WriteString(fmt.Sprintf("Base SHA: `%s`\n", ctx.RepairContext.BaseSHA))
		b.WriteString(fmt.Sprintf("Merge state: `%s`\n\n", ctx.RepairContext.MergeState))
		if ctx.RepairAction == models.PullRequestRepairActionTypeResolveConflicts {
			b.WriteString(ResolveConflictsGuidance(ctx.RepairContext.BaseSHA, ctx.RepairContext.HeadSHA))
			b.WriteString("\n\n")
		}
		if len(ctx.RepairContext.FailingChecks) > 0 {
			b.WriteString("Failing checks:\n")
			for _, check := range ctx.RepairContext.FailingChecks {
				b.WriteString("- `")
				b.WriteString(check.Name)
				b.WriteString("` (")
				b.WriteString(string(check.Category))
				b.WriteString(")")
				if check.Summary != "" {
					b.WriteString(": ")
					b.WriteString(check.Summary)
				}
				b.WriteString("\n")
				for _, annotation := range check.Annotations {
					b.WriteString("  - annotation: ")
					b.WriteString(annotation)
					b.WriteString("\n")
				}
				if check.LogExcerpt != "" {
					b.WriteString("  - log excerpt: ")
					b.WriteString(check.LogExcerpt)
					b.WriteString("\n")
				}
				if check.DetailsURL != "" {
					b.WriteString("  - details: ")
					b.WriteString(check.DetailsURL)
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
		}
	}

	return strings.TrimSpace(b.String())
}

// ComplexityEstimate holds the triage system's assessment of issue complexity.
type ComplexityEstimate struct {
	Tier       int     `json:"tier"`
	Reasoning  string  `json:"reasoning"`
	Confidence float64 `json:"confidence"`
}

// AgentPrompt is the prepared instruction set passed to the agent CLI.
type AgentPrompt struct {
	SystemPrompt    string
	UserPrompt      string
	MaxTokens       int
	ReasoningEffort models.ReasoningEffort
	Files           []string // relevant files to focus on
	UsageHint       TokenUsageHint
	Continuation    bool   // true when resuming an existing interactive session
	ResumeSessionID string // agent's session ID for --resume/--continue (set on subsequent turns)
	UserMessage     string // follow-up message from the user (set on subsequent turns)
	// RevisionContext carries revision/repair metadata into Execute.
	// PreparePrompt is bypassed on continuation turns, so adapters that need
	// repair-specific context read it from here. Nil for ordinary turns.
	RevisionContext *RevisionContext
	// HumanInputAnswer carries a normalized answer to a provider-native
	// deferred request. Adapters with native resume hooks can translate this
	// back into provider-specific callback payloads instead of treating the
	// answer as plain follow-up text only.
	HumanInputAnswer *HumanInputAnswer
}

// AgentResult is the outcome of an agent execution.
type AgentResult struct {
	Diff               string
	Summary            string
	TokenUsage         TokenUsage
	ExitCode           int
	Error              string
	AgentSessionID     string // agent's internal session ID, used for --resume on subsequent turns
	RequiresHumanInput bool
}

type HumanInputRequest struct {
	ProviderRequestID string
	Kind              models.HumanInputRequestKind
	Title             string
	Body              string
	Context           *string
	BlocksPhase       *string
	Choices           []models.HumanInputChoice
	ResponseSchema    json.RawMessage
	ProviderPayload   json.RawMessage
}

type HumanInputAnswer struct {
	RequestID         uuid.UUID
	ProviderRequestID string
	Kind              models.HumanInputRequestKind
	Status            models.HumanInputRequestStatus
	AnswerText        *string
	SelectedChoiceIDs []string
	AnswerPayload     json.RawMessage
	Choices           []models.HumanInputChoice
}

// TokenUsage tracks LLM token consumption and cost for an agent run.
type TokenUsage struct {
	InputTokens         int               `json:"input_tokens"`
	CachedInputTokens   int               `json:"cached_input_tokens,omitempty"`
	CacheCreationTokens int               `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens        int               `json:"output_tokens"`
	TotalTokens         int               `json:"total_tokens,omitempty"`
	TotalCostUSD        float64           `json:"total_cost_usd,omitempty"`
	Cost                *TokenCost        `json:"cost,omitempty"`
	NativeCost          *TokenCost        `json:"native_cost,omitempty"`
	NativeUsage         *NativeTokenUsage `json:"native_usage,omitempty"`
	Reported            bool              `json:"-"`
}

type TokenCostUnit string

const (
	TokenCostUnitUSD     TokenCostUnit = "usd"
	TokenCostUnitCredits TokenCostUnit = "credits"
)

type TokenCostSource string

const (
	TokenCostSourceDirect      TokenCostSource = "direct"
	TokenCostSourceDerived     TokenCostSource = "derived"
	TokenCostSourceUnavailable TokenCostSource = "unavailable"
)

type TokenBillingMode string

const (
	TokenBillingModeUnknown      TokenBillingMode = "unknown"
	TokenBillingModeAPIKey       TokenBillingMode = "api_key"
	TokenBillingModeSubscription TokenBillingMode = "subscription"
)

type TokenCost struct {
	Amount float64         `json:"amount"`
	Unit   TokenCostUnit   `json:"unit"`
	Source TokenCostSource `json:"source"`
	Detail string          `json:"detail,omitempty"`
}

type NativeTokenUsage struct {
	Reported            bool             `json:"reported"`
	Provider            string           `json:"provider,omitempty"`
	Model               string           `json:"model,omitempty"`
	BillingMode         TokenBillingMode `json:"billing_mode,omitempty"`
	InputTokens         int              `json:"input_tokens,omitempty"`
	CachedInputTokens   int              `json:"cached_input_tokens,omitempty"`
	CacheCreationTokens int              `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens        int              `json:"output_tokens,omitempty"`
	TotalTokens         int              `json:"total_tokens,omitempty"`
}

type TokenUsageHint struct {
	AgentType      models.AgentType
	EffectiveModel string
	BillingMode    TokenBillingMode
}

// LogEntry represents a single log line emitted during agent execution.
type LogEntry struct {
	Timestamp  time.Time              `json:"timestamp"`
	Level      string                 `json:"level"` // info, debug, error, tool_use, output, question
	Message    string                 `json:"message"`
	ThreadID   *uuid.UUID             `json:"thread_id,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	HumanInput *HumanInputRequest     `json:"human_input,omitempty"`
}

// SandboxProvider abstracts sandbox lifecycle management.
// The default implementation uses Docker with gVisor.
type SandboxProvider interface {
	// Name returns the provider identifier (e.g., "docker", "e2b").
	Name() string

	// Create spins up a new sandbox with the given resource limits.
	Create(ctx context.Context, cfg SandboxConfig) (*Sandbox, error)

	// CloneRepo clones a repository into the sandbox's workspace.
	CloneRepo(ctx context.Context, sb *Sandbox, repoURL, branch, token string) error

	// Exec runs a command inside the sandbox and streams output.
	Exec(ctx context.Context, sb *Sandbox, cmd string, stdout, stderr io.Writer) (int, error)

	// ReadFile reads a file from the sandbox filesystem.
	ReadFile(ctx context.Context, sb *Sandbox, path string) ([]byte, error)

	// WriteFile writes a file into the sandbox filesystem.
	WriteFile(ctx context.Context, sb *Sandbox, path string, data []byte) error

	// Destroy tears down the sandbox and cleans up all resources.
	// Must be safe to call multiple times and must not panic.
	Destroy(ctx context.Context, sb *Sandbox) error

	// IsAlive reports whether the sandbox container still exists. Returns
	// (false, nil) when the container is definitively gone (e.g., "no such
	// container"), (true, nil) when it exists (regardless of running state —
	// attaching to a stopped container will fail later with a more specific
	// error), and (false, err) for transient failures the caller can't
	// distinguish from a real absence. Used by callers that want to reuse a
	// recorded container_id but need to guard against zombie rows left by
	// out-of-band container removal.
	IsAlive(ctx context.Context, sb *Sandbox) (bool, error)

	// ConnectionInfo returns provider-specific connection details for local resume.
	ConnectionInfo(ctx context.Context, sb *Sandbox) (*SandboxConnectionInfo, error)

	// Snapshot tars the workspace and agent state directories, returning a
	// reader for the compressed archive. Caller must close the reader.
	Snapshot(ctx context.Context, sb *Sandbox) (io.ReadCloser, error)

	// Restore extracts a snapshot tarball into the sandbox, restoring
	// workspace and agent state from a previous turn.
	Restore(ctx context.Context, sb *Sandbox, reader io.Reader) error

	// ExecStream runs a command inside the sandbox and calls onLine for
	// each newline-delimited chunk of stdout as it arrives, enabling
	// real-time streaming. Returns the command's exit code.
	ExecStream(ctx context.Context, sb *Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error)
}

// ExecStreamOptions controls a streaming sandbox exec without requiring the
// caller to encode environment or working-directory details into shell text.
type ExecStreamOptions struct {
	Cmd        []string
	Env        map[string]string
	WorkingDir string
}

// DefaultSandboxTimeout is the default maximum wall-clock duration for an
// agent execution inside a sandbox. Org admins can override this per-org via
// OrgSettings.MaxSessionDurationSeconds.
const DefaultSandboxTimeout = time.Hour

// HandlerCleanupBuffer is the slack added on top of the resolved session
// timeout at the worker handler boundary, giving the orchestrator time to
// destroy the container, persist a terminal status, and enqueue follow-up
// jobs after a session hits its wall-clock limit.
const HandlerCleanupBuffer = 2 * time.Minute

// SandboxConfig holds the resource limits and settings for creating a sandbox.
type SandboxConfig struct {
	Image          string            // base image with agent CLI tools pre-installed
	CPULimit       float64           // CPU cores (default: 2)
	MemoryLimitMB  int               // memory in MB (default: 3072)
	Timeout        time.Duration     // max execution time (default: DefaultSandboxTimeout)
	NetworkPolicy  string            // "restricted" — allow only LLM API endpoints
	NetworkName    string            // optional Docker network override for this sandbox
	ResolvConfPath string            // optional host resolv.conf override for this sandbox
	EgressMode     string            // "direct" or "static"; persisted in sandbox metadata
	WorkDir        string            // path to the repo checkout inside the sandbox (e.g. /home/sandbox/<repo>)
	HomeDir        string            // sandbox user's home dir (e.g. /home/sandbox); HOME env var points here
	Env            map[string]string // environment variables injected into the container (e.g. API keys)
	DiskLimitGB    int               // max container rootfs size in GB (default: 10); requires overlay2+xfs backing store

	// SessionID and OrgID are tracing identifiers propagated into provider log
	// lines so that every sandbox-lifecycle event (create, exec, clone, stop,
	// destroy) is greppable by session in Grafana. Set by the caller; empty
	// values are omitted from logs. Copied onto the returned Sandbox so
	// follow-up operations keep the same scope.
	SessionID string
	OrgID     string
	// Purpose describes why the sandbox was created (e.g. "agent_run",
	// "pm_bootstrap", "preview"). Included in provider logs to disambiguate
	// sandboxes that aren't attached to a single session (e.g. PM bootstrap).
	Purpose string
	// AuthSocketPath is the host-side path of a per-session AF_UNIX socket
	// the provider should make reachable inside the container. The provider
	// bind-mounts the socket's parent directory (not the file itself) onto
	// SandboxSocketDir so the in-container path stays valid across host-side
	// recreate cycles. Empty means the agent has no GitHub credential helper
	// wired (preview sandboxes, PM bootstrap, anything not triggered as part
	// of an agent_run).
	AuthSocketPath string
}

// DefaultSandboxConfig returns a SandboxConfig populated with sensible defaults.
func DefaultSandboxConfig() SandboxConfig {
	image := "143-sandbox:latest"
	if v := os.Getenv("SANDBOX_IMAGE"); v != "" {
		image = v
	}
	cpuLimit := 2.0
	if v := os.Getenv("SANDBOX_CPU_LIMIT"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 {
			cpuLimit = parsed
		}
	}
	memoryLimitMB := 3072
	if v := os.Getenv("SANDBOX_MEMORY_LIMIT_MB"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			memoryLimitMB = parsed
		}
	}
	diskLimitGB := 10
	if v := os.Getenv("SANDBOX_DISK_LIMIT_GB"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			diskLimitGB = parsed
		}
	}
	return SandboxConfig{
		Image:         image,
		CPULimit:      cpuLimit,
		MemoryLimitMB: memoryLimitMB,
		DiskLimitGB:   diskLimitGB,
		Timeout:       DefaultSandboxTimeout,
		NetworkPolicy: "restricted",
		WorkDir:       "/workspace",
		HomeDir:       "/home/sandbox",
	}
}

// SlugForRepo converts a repo full name ("org/repo") into a filesystem-safe
// slug suitable for use as a directory name (the repo portion only). Returns
// an empty string for inputs that don't contain a "/", resolve to an empty
// slug, or resolve to a traversal component ("." / ".."); callers should
// treat that as "no repo known" and fall back to defaults.
func SlugForRepo(fullName string) string {
	_, slug, ok := strings.Cut(fullName, "/")
	if !ok {
		return ""
	}
	// Replace path separators defensively — GitHub repo names don't contain "/"
	// but the sandbox dir must be a single path component.
	slug = strings.ReplaceAll(slug, "/", "-")
	// Reject traversal components so /home/sandbox/<slug> can't resolve to a
	// parent dir (e.g. "/home/sandbox/.." = "/home"). GitHub's repo-name
	// grammar already excludes these, but this keeps the function safe for
	// any future caller that feeds it less-trusted input.
	if slug == "." || slug == ".." {
		return ""
	}
	return slug
}

// Sandbox represents a running isolated environment for agent execution.
type Sandbox struct {
	ID       string            // unique sandbox identifier (container ID, VM ID, etc.)
	Provider string            // which provider created this sandbox
	WorkDir  string            // path to the repo checkout inside the sandbox
	HomeDir  string            // sandbox user's home dir (HOME env); distinct from WorkDir
	Env      map[string]string `json:"-"` // environment passed to agent CLI execs; never persist secrets
	Metadata map[string]string // provider-specific metadata

	// Tracing identifiers copied from SandboxConfig at Create time. Providers
	// attach these to every lifecycle log line so operators can find all
	// sandbox logs for a given session in Grafana.
	SessionID string
	OrgID     string
	Purpose   string
}

// SandboxMetadataBaseCommitSHA is the well-known sandbox.Metadata key under
// which the orchestrator stashes the immutable base commit captured at session
// start. Adapters read it when collecting the authoritative session diff. The
// constant lives in the agent package (rather than sessiondiff) so callers in
// the agent package can reference it without creating an import cycle —
// sessiondiff already imports agent for SandboxProvider/Sandbox.
const SandboxMetadataBaseCommitSHA = "base_commit_sha"

// SandboxMetadataTargetBranch is the well-known sandbox.Metadata key under
// which the orchestrator stashes the target branch (e.g. "main") for the
// session — the branch the working branch will be PR'd into. sessiondiff.Collect
// uses it to compute a dynamic merge-base diff (`origin/<target>...HEAD`) so
// integrating the target branch back into the working branch (e.g.
// `git pull origin main` or merging main to resolve PR conflicts) does not
// inflate the diff with target-branch changes. Falls back to the immutable
// base_commit_sha when missing or when the merge-base resolution fails.
const SandboxMetadataTargetBranch = "target_branch"

// SandboxMetadataEgressMode records whether a sandbox used direct worker
// egress or the opt-in static egress gateway path.
const SandboxMetadataEgressMode = "egress_mode"

const (
	SandboxEgressModeDirect = "direct"
	SandboxEgressModeStatic = "static"
)

// SandboxMetadataClaudeCodePermissionMode is the sandbox.Metadata key used by
// Claude Code auth setup to record the CLI permission mode for sandboxed runs.
const SandboxMetadataClaudeCodePermissionMode = "claude_code_permission_mode"

// SandboxMetadataClaudeCodeVersion is the sandbox.Metadata key used to cache
// the parsed `claude --version` result for capability checks.
const SandboxMetadataClaudeCodeVersion = "claude_code_version"

const (
	ClaudeCodePermissionModeAuto              = "auto"
	ClaudeCodePermissionModeAcceptEdits       = "acceptEdits"
	ClaudeCodePermissionModeBypassPermissions = "bypassPermissions"
)

// SandboxConnectionInfo holds provider-specific connection details for local resume.
type SandboxConnectionInfo struct {
	Provider     string            // "docker" or "e2b"
	SandboxID    string            // container/VM ID
	ConnectURL   string            // URL for the 143 CLI to connect
	AgentSession string            // agent-specific session ID for --resume
	Environment  map[string]string // env vars needed for the local CLI
}

// sandboxProviderKey is the context key for injecting a SandboxProvider.
// It lives in the agent package so both the orchestrator and adapters can
// share it without circular imports.
type sandboxProviderKey struct{}

// WithSandboxProvider returns a context carrying the given SandboxProvider.
// The orchestrator injects the provider before calling adapter.Execute,
// and adapters retrieve it via SandboxProviderFromContext.
func WithSandboxProvider(ctx context.Context, p SandboxProvider) context.Context {
	return context.WithValue(ctx, sandboxProviderKey{}, p)
}

// SandboxProviderFromContext retrieves the SandboxProvider from the context.
// Returns nil if no provider is set.
func SandboxProviderFromContext(ctx context.Context) SandboxProvider {
	p, _ := ctx.Value(sandboxProviderKey{}).(SandboxProvider)
	return p
}
