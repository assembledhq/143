// Package agent defines the interfaces and types for running coding agents
// inside sandboxes to fix issues.
package agent

import (
	"context"
	"encoding/json"
	"io"
	"time"

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
	RepoURL            string
	RepoBranch         string
	OrgSettings        json.RawMessage
	TokenMode          string // "low" or "high"
	ComplexityEstimate *ComplexityEstimate
	ContextDocs        []string // content of CLAUDE.md, AGENTS.md, etc.
	RevisionContext    *RevisionContext
	PMContext          *PMTaskContext // PM guidance for coding agents
	PMContextJSON      string         // serialized PM context for PM agent runs
	IntegrationSkills  string         // auto-generated CLI skills doc for integration tools
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
	FormattedFeedback string `json:"formatted_feedback"`
	PreviousDiff      string `json:"previous_diff"`
	CommentSummary    string `json:"comment_summary"`
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
	Files           []string // relevant files to focus on
	Continuation    bool     // true when resuming an existing interactive session
	ResumeSessionID string   // agent's session ID for --resume/--continue (set on subsequent turns)
	UserMessage     string   // follow-up message from the user (set on subsequent turns)
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

// SandboxConfig holds the resource limits and settings for creating a sandbox.
type SandboxConfig struct {
	Image         string            // base image with agent CLI tools pre-installed
	CPULimit      float64           // CPU cores (default: 2)
	MemoryLimitMB int               // memory in MB (default: 4096)
	Timeout       time.Duration     // max execution time (default: 5 min)
	NetworkPolicy string            // "restricted" — allow only LLM API endpoints
	WorkDir       string            // /workspace
	Env           map[string]string // environment variables injected into the container (e.g. API keys)
	DiskLimitGB   int               // max container rootfs size in GB (default: 10); requires overlay2+xfs backing store
}

// DefaultSandboxConfig returns a SandboxConfig populated with sensible defaults.
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		Image:         "143-sandbox:latest",
		CPULimit:      2,
		MemoryLimitMB: 4096,
		DiskLimitGB:   10,
		Timeout:       5 * time.Minute,
		NetworkPolicy: "restricted",
		WorkDir:       "/workspace",
	}
}

// Sandbox represents a running isolated environment for agent execution.
type Sandbox struct {
	ID       string            // unique sandbox identifier (container ID, VM ID, etc.)
	Provider string            // which provider created this sandbox
	WorkDir  string            // path to the workspace inside the sandbox
	Metadata map[string]string // provider-specific metadata
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
