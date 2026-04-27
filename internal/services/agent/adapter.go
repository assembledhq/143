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
}

// AgentInput contains everything the agent needs to understand and fix an issue.
type AgentInput struct {
	Issue              *models.Issue
	LinkedIssues       []models.SessionIssueSnapshotEntry
	Manual             bool
	UserMessage        string
	RepoURL            string
	RepoBranch         string
	References         []models.SessionInputReference
	Commands           []models.SessionInputCommand
	ReasoningEffort    models.ReasoningEffort
	OrgSettings        json.RawMessage
	TokenMode          string // "low" or "high"
	ComplexityEstimate *ComplexityEstimate
	ContextDocs        []string // content of CLAUDE.md, AGENTS.md, etc.
	RevisionContext    *RevisionContext
	PMContext          *PMTaskContext        // PM guidance for coding agents
	PMContextJSON      string                // serialized PM context for PM agent runs
	IntegrationSkills  string                // auto-generated CLI skills doc for integration tools
	ContextLimits      *models.ContextLimits // org-specific token limits (nil = use defaults)
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
	// ReviewContext is set when the user triggered a session-native review
	// turn (e.g. via the Review button). Adapters that implement
	// ReviewCapableAdapter branch on this to invoke the agent's native
	// review surface (Claude Code's /review skill, etc.) instead of
	// running the next conversational turn.
	ReviewContext *SessionReviewContext `json:"review_context,omitempty"`
}

// SessionReviewContext describes a single user-initiated review turn. It is
// session-scoped — reviews can run before a PR exists — and intentionally
// distinct from PullRequestRepairContext so the two flows don't drift.
type SessionReviewContext struct {
	Mode           models.SessionReviewMode `json:"mode"`
	PreviousDiff   string                   `json:"previous_diff,omitempty"`
	RequestSummary string                   `json:"request_summary,omitempty"`
}

// ReviewCapableAdapter is implemented by adapters whose underlying agent has
// a curated, native review surface (e.g. Claude Code's /review and
// /security-review skills). Adapters without a native review surface MUST
// NOT implement this interface — the Review button will hide for sessions
// using those agents, by design (see doc 63: "no fallback prompt-based
// review for agents without native support").
type ReviewCapableAdapter interface {
	// ReviewModes returns the review modes this adapter supports natively,
	// in display order. Returning nil or an empty slice has the same effect
	// as not implementing the interface.
	ReviewModes() []models.SessionReviewMode
}

// AdapterReviewModes returns the review modes a given adapter supports, or
// nil if the adapter does not implement ReviewCapableAdapter. Centralized so
// callers (HTTP handlers, the session review service) don't have to repeat
// the type assertion and nil-check pattern.
func AdapterReviewModes(adapter AgentAdapter) []models.SessionReviewMode {
	if adapter == nil {
		return nil
	}
	capable, ok := adapter.(ReviewCapableAdapter)
	if !ok {
		return nil
	}
	modes := capable.ReviewModes()
	if len(modes) == 0 {
		return nil
	}
	return modes
}

// AdapterSupportsReviewMode reports whether the adapter natively supports
// the given review mode.
func AdapterSupportsReviewMode(adapter AgentAdapter, mode models.SessionReviewMode) bool {
	for _, m := range AdapterReviewModes(adapter) {
		if m == mode {
			return true
		}
	}
	return false
}

// ReviewModeProvider returns a function that maps an agent type to the
// review modes its adapter supports natively. Empty/nil result means the
// agent has no native review surface. Used to wire the session review
// service to the orchestrator's adapter map without coupling the
// sessionreview package to the full agent package.
func ReviewModeProvider(adapters map[models.AgentType]AgentAdapter) func(models.AgentType) []models.SessionReviewMode {
	return func(agentType models.AgentType) []models.SessionReviewMode {
		adapter, ok := adapters[agentType]
		if !ok {
			return nil
		}
		return AdapterReviewModes(adapter)
	}
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

// MarshalRevisionContextWithoutReview returns the JSON encoding of ctx with
// ReviewContext stripped out. Returns (nil, nil) when ctx is nil or only
// carried review context — a nil byte slice cleanly clears the persisted
// row. Returns the re-encoded payload otherwise so other fields
// (RepairAction, RepairContext, FormattedFeedback, ...) are preserved.
//
// Used by the orchestrator to consume a one-shot review directive without
// disturbing PR-repair revision state that may be set on the same row.
func MarshalRevisionContextWithoutReview(ctx *RevisionContext) ([]byte, error) {
	if ctx == nil {
		return nil, nil
	}
	stripped := *ctx
	stripped.ReviewContext = nil
	if isRevisionContextEmpty(&stripped) {
		return nil, nil
	}
	return json.Marshal(stripped)
}

func isRevisionContextEmpty(ctx *RevisionContext) bool {
	if ctx == nil {
		return true
	}
	return ctx.FormattedFeedback == "" &&
		ctx.PreviousDiff == "" &&
		ctx.CommentSummary == "" &&
		ctx.RepairAction == "" &&
		ctx.RepairContext == nil &&
		ctx.ReviewContext == nil
}

func FormatRevisionContextForContinuation(ctx *RevisionContext) string {
	if ctx == nil {
		return ""
	}

	// Reviews are routed through adapter-native review surfaces by Execute
	// when ReviewCapableAdapter is implemented. The orchestrator only reaches
	// FormatRevisionContextForContinuation here for the prompt-text fallback,
	// so emitting any review framing here would double-prompt or — worse —
	// silently produce a hand-rolled review on agents the design explicitly
	// chose not to fake (see doc 63 § "What we are explicitly *not* building").
	// Adapters that handle reviews natively read ReviewContext directly off
	// AgentPrompt.RevisionContext.

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
	Continuation    bool     // true when resuming an existing interactive session
	ResumeSessionID string   // agent's session ID for --resume/--continue (set on subsequent turns)
	UserMessage     string   // follow-up message from the user (set on subsequent turns)
	// RevisionContext carries revision/repair/review metadata into Execute.
	// PreparePrompt is bypassed on continuation turns, so adapters that need
	// to branch on review or repair context (e.g. to invoke Claude Code's
	// /review skill) read it from here. Nil for ordinary turns.
	RevisionContext *RevisionContext
}

// IsReview reports whether this prompt represents a session-native review
// turn that adapters should route to the agent's curated review surface.
func (p *AgentPrompt) IsReview() bool {
	return p != nil && p.RevisionContext != nil && p.RevisionContext.ReviewContext != nil
}

// AgentResult is the outcome of an agent execution.
type AgentResult struct {
	Diff                string
	Summary             string
	TokenUsage          TokenUsage
	ExitCode            int
	Error               string
	ConfidenceScore     float64 // 0.0-1.0, self-assessed by the agent
	ConfidenceReasoning string
	RiskFactors         []string
	AgentSessionID      string // agent's internal session ID, used for --resume on subsequent turns
}

// TokenUsage tracks LLM token consumption and cost for an agent run.
type TokenUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// LogEntry represents a single log line emitted during agent execution.
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"` // info, debug, error, tool_use, output, question
	Message   string                 `json:"message"`
	ThreadID  *uuid.UUID             `json:"thread_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
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

// DefaultSandboxTimeout is the default maximum wall-clock duration for an
// agent execution inside a sandbox. Org admins can override this per-org via
// OrgSettings.MaxSessionDurationSeconds.
const DefaultSandboxTimeout = 25 * time.Minute

// HandlerCleanupBuffer is the slack added on top of the resolved session
// timeout at the worker handler boundary, giving the orchestrator time to
// destroy the container, persist a terminal status, and enqueue follow-up
// jobs after a session hits its wall-clock limit.
const HandlerCleanupBuffer = 2 * time.Minute

// SandboxConfig holds the resource limits and settings for creating a sandbox.
type SandboxConfig struct {
	Image         string            // base image with agent CLI tools pre-installed
	CPULimit      float64           // CPU cores (default: 2)
	MemoryLimitMB int               // memory in MB (default: 4096)
	Timeout       time.Duration     // max execution time (default: DefaultSandboxTimeout)
	NetworkPolicy string            // "restricted" — allow only LLM API endpoints
	WorkDir       string            // path to the repo checkout inside the sandbox (e.g. /home/sandbox/<repo>)
	HomeDir       string            // sandbox user's home dir (e.g. /home/sandbox); HOME env var points here
	Env           map[string]string // environment variables injected into the container (e.g. API keys)
	DiskLimitGB   int               // max container rootfs size in GB (default: 10); requires overlay2+xfs backing store

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
	memoryLimitMB := 4096
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
	Metadata map[string]string // provider-specific metadata

	// Tracing identifiers copied from SandboxConfig at Create time. Providers
	// attach these to every lifecycle log line so operators can find all
	// sandbox logs for a given session in Grafana.
	SessionID string
	OrgID     string
	Purpose   string
}

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
