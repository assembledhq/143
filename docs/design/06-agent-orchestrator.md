# Design: Agent Orchestrator

This document describes how 143.dev launches, manages, and monitors coding agent runs inside isolated sandboxes.

## Overview

When an issue is selected for an agent run (either manually or auto-triggered), the orchestrator:

1. Prepares the execution context (issue details, codebase, instructions)
2. Launches a sandboxed container
3. Runs the coding agent inside the container
4. Streams logs back to the UI in real time
5. Collects the result (code diff) when the agent completes
6. Hands off to the validation pipeline

## Agent Adapter Interface

143.dev supports multiple coding agents. Each agent type implements a common adapter:

```go
type AgentAdapter interface {
    // Name returns the agent identifier (e.g., "claude_code", "codex").
    Name() string

    // PreparePrompt constructs the prompt/instructions for the agent.
    PreparePrompt(ctx context.Context, input *AgentInput) (*AgentPrompt, error)

    // Execute runs the agent inside the provided sandbox and streams output.
    Execute(ctx context.Context, sandbox *Sandbox, prompt *AgentPrompt, logCh chan<- LogEntry) (*AgentResult, error)
}

type AgentInput struct {
    Issue              *models.Issue
    RepoURL            string
    RepoBranch         string
    OrgSettings        *models.OrgSettings
    TokenMode          string // "low" or "high"
    ComplexityEstimate *models.ComplexityEstimate
}

type AgentPrompt struct {
    SystemPrompt string
    UserPrompt   string
    MaxTokens    int
    Files        []string // relevant files to focus on
}

type AgentResult struct {
    Diff                string
    Summary             string
    TokenUsage          TokenUsage
    ExitCode            int
    Error               string
    ConfidenceScore     float64  // 0.0-1.0, self-assessed by the agent
    ConfidenceReasoning string
    RiskFactors         []string
}
```

### Supported Adapters

#### Claude Code Adapter

Runs Claude Code CLI inside the sandbox:

```bash
claude-code --prompt "$PROMPT" --output-format diff --max-tokens $MAX_TOKENS
```

- **Low token mode**: `max_tokens = 50_000`, focus prompt on targeted fix
- **High token mode**: `max_tokens = 200_000`, broader exploration allowed

The prompt includes:
- Issue title and description (sanitized — see [20-security-architecture.md](20-security-architecture.md) for prompt injection defense)
- Stack trace (if from Sentry)
- Customer impact context
- Relevant file paths (from Sentry frames or Linear issue links)
- Instructions to produce a minimal, focused fix with tests
- Explicit instructions to treat issue content as data, not instructions (prompt injection defense)
- Repo-specific conventions from `.143/learned-conventions.md` if present in the repo (see [11-review-feedback-loop.md](11-review-feedback-loop.md))
- For revision runs: the reviewer feedback (sanitized) and previous diff (see [11-review-feedback-loop.md](11-review-feedback-loop.md))

#### Codex Adapter

Runs OpenAI Codex CLI inside the sandbox. Similar prompt structure and execution model.

#### Gemini CLI Adapter

Runs Google Gemini CLI inside the sandbox. Same adapter pattern — the orchestrator is agent-agnostic and delegates all model-specific behavior to the adapter.

#### Custom Adapter

Organizations can register custom agent commands. The orchestrator runs the command in the sandbox and captures stdout as the diff.

## Sandbox Provider Interface

The sandbox layer is **pluggable**. The orchestrator doesn't know or care whether sandboxes are Docker containers, microVMs, or cloud-hosted environments. All sandbox operations go through the `SandboxProvider` interface:

```go
// SandboxProvider abstracts sandbox lifecycle management.
// The default implementation uses Docker with gVisor. Alternative providers
// (E2B, microsandbox, K8s Agent Sandbox) can be swapped in via configuration.
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
    // Must be safe to call multiple times and must not fail silently.
    Destroy(ctx context.Context, sb *Sandbox) error
}

type SandboxConfig struct {
    Image         string        // base image with agent CLI tools pre-installed
    CPULimit      float64       // CPU cores (default: 2)
    MemoryLimitMB int           // memory in MB (default: 4096)
    Timeout       time.Duration // max execution time (default: 5 min)
    NetworkPolicy string        // "restricted" — allow only LLM API endpoints
    WorkDir       string        // /workspace
}

type Sandbox struct {
    ID       string            // unique sandbox identifier (container ID, VM ID, etc.)
    Provider string            // which provider created this sandbox
    WorkDir  string            // path to the workspace inside the sandbox
    Metadata map[string]string // provider-specific metadata
}
```

### Provider: Docker + gVisor (Default)

The default sandbox provider uses Docker containers with **gVisor** (`runsc`) as the container runtime. gVisor intercepts all syscalls in user space, providing kernel-level isolation without the overhead of a full VM.

**Why gVisor over plain Docker:**
- Plain Docker containers share the host kernel — a kernel exploit in agent-generated code could escape the sandbox
- gVisor implements its own user-space kernel (Sentry) that intercepts every syscall, drastically reducing the attack surface
- Near-zero cold start overhead compared to Docker (milliseconds, not seconds)
- Drop-in replacement — same Docker images, same Docker API, just change the runtime flag
- Used in production by Google Cloud Run and GKE Sandbox

```go
// internal/services/agent/providers/docker.go

type DockerProvider struct {
    client  *docker.Client
    runtime string // "runsc" (gVisor, default) or "runc" (standard Docker)
    network string // pre-created Docker network with egress restrictions
}

func (d *DockerProvider) Create(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
    container, _ := d.client.ContainerCreate(ctx, &container.Config{
        Image:      cfg.Image,
        WorkingDir: cfg.WorkDir,
        User:       "sandbox",  // non-root
    }, &container.HostConfig{
        Runtime: d.runtime, // "runsc" for gVisor
        Resources: container.Resources{
            NanoCPUs:  int64(cfg.CPULimit * 1e9),
            Memory:    int64(cfg.MemoryLimitMB) * 1024 * 1024,
            PidsLimit: int64Ptr(256),       // prevent fork bombs
        },
        NetworkMode: container.NetworkMode(d.network),
        CapDrop:     []string{"ALL"},        // drop all Linux capabilities
        SecurityOpt: []string{"no-new-privileges:true"},
        ReadonlyRootfs: true,                // read-only root, agent writes to /workspace and /tmp
        Tmpfs: map[string]string{
            "/tmp": "rw,noexec,nosuid,size=1g",
        },
    }, nil, nil, "")

    d.client.ContainerStart(ctx, container.ID, container.StartOptions{})

    return &Sandbox{
        ID:       container.ID,
        Provider: "docker",
        WorkDir:  cfg.WorkDir,
    }, nil
}
```

**gVisor installation**: On Linux hosts, install `runsc` and register it as a Docker runtime. See [gvisor.dev/docs/user_guide/install](https://gvisor.dev/docs/user_guide/install/). In production, gVisor is **required** — the server refuses to start without it unless `SANDBOX_REQUIRE_GVISOR=false` is explicitly set. On dev machines (macOS), the provider falls back to `runc` with a logged warning. See [20-security-architecture.md](20-security-architecture.md) for full sandbox hardening details.

#### Network Policy

A pre-created Docker network restricts sandbox egress:

- **Allowed**: LLM provider APIs (api.anthropic.com, api.openai.com, generativelanguage.googleapis.com), package registries (npm, PyPI, crates.io)
- **Blocked**: Everything else — no access to the host network, internal services, or arbitrary internet

Implemented via iptables rules on the Docker bridge network. The orchestrator creates this network on startup.

#### Resource Limits

| Resource | Default | Max |
|----------|---------|-----|
| CPU | 2 cores | 4 cores |
| Memory | 4 GB | 8 GB |
| Timeout | 5 min | 30 min |
| Disk | 10 GB | 20 GB |

Configurable per org in settings. The orchestrator enforces an absolute max to prevent resource abuse.

### Provider: E2B (Optional)

For teams that want stronger isolation (Firecracker microVMs) without managing infrastructure, E2B can be used as an alternative sandbox provider. Each sandbox is a full Linux VM with its own kernel.

```go
// internal/services/agent/providers/e2b.go

type E2BProvider struct {
    apiKey     string
    templateID string // custom E2B template with agent CLIs pre-installed
}

func (e *E2BProvider) Create(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
    // POST https://api.e2b.dev/sandboxes
    // Creates a Firecracker microVM with the specified template
    // ...
}
```

Requires `E2B_API_KEY` and `E2B_TEMPLATE_ID` environment variables. See [e2b.dev](https://e2b.dev) for setup.

**Trade-offs vs. Docker+gVisor:**
- Stronger isolation (separate kernel per VM vs. shared kernel with syscall interception)
- Managed infrastructure (no gVisor/Docker setup needed)
- Adds a cloud dependency (conflicts with the self-hosting principle if used as the only provider)
- ~$0.05/hr per sandbox vs. free for Docker

### Provider: Custom

Organizations can implement `SandboxProvider` for their own infrastructure — Kubernetes pods, Fly Machines, microsandbox, or any other isolation backend.

### Provider Selection

The provider is selected via the `SANDBOX_PROVIDER` environment variable:

| Value | Provider | Requirements |
|-------|----------|-------------|
| `docker` (default) | Docker + gVisor | Docker daemon, gVisor `runsc` runtime (falls back to `runc` if unavailable) |
| `e2b` | E2B cloud | `E2B_API_KEY`, `E2B_TEMPLATE_ID` |

The orchestrator initializes the configured provider at startup. All providers must pass a health check (create and destroy a test sandbox) before the worker begins processing jobs.

### Sandbox Lifecycle

```
┌─────────┐    ┌──────────┐    ┌─────────┐    ┌──────────┐    ┌──────────┐
│  Create  │───▶│  Clone   │───▶│  Run    │───▶│ Collect  │───▶│ Destroy  │
│ Sandbox  │    │  Repo    │    │ Agent   │    │  Result  │    │ Sandbox  │
└─────────┘    └──────────┘    └─────────┘    └──────────┘    └──────────┘
```

1. **Create**: Call `provider.Create()` with resource limits. Provider-agnostic.
2. **Clone**: Call `provider.CloneRepo()` to clone the target repository into the workspace (using a GitHub App installation token).
3. **Run**: Execute the agent adapter's `Execute` method. The adapter calls `provider.Exec()` to run commands inside the sandbox. Stream logs via the `logCh` channel.
4. **Collect**: Call `provider.Exec()` to run `git diff` inside the sandbox and collect the result.
5. **Destroy**: Call `provider.Destroy()`. Always runs, even on failure.

## Log Streaming

During execution, the agent adapter sends log entries to a channel:

```go
type LogEntry struct {
    Timestamp time.Time
    Level     string // info, debug, error, tool_use, output
    Message   string
    Metadata  map[string]interface{} // tool calls, file paths, etc.
}
```

The orchestrator:

1. Persists each log entry to the `agent_run_logs` table
2. Broadcasts each entry to any connected SSE clients (the frontend)

This enables real-time log viewing in the UI.

## Orchestrator Workflow

```go
func (o *Orchestrator) RunAgent(ctx context.Context, run *models.AgentRun) error {
    // 1. Update status to "running"
    o.db.UpdateAgentRunStatus(ctx, run.ID, "running")

    // 2. Fetch complexity estimate
    issue, _ := o.db.GetIssue(ctx, run.IssueID)
    estimate, _ := o.db.GetComplexityEstimate(ctx, issue.ID)
    settings, _ := o.db.GetOrgSettings(ctx, run.OrgID)

    // 3. Check aggressiveness — skip if issue is too complex for current settings
    if !run.SkipComplexityCheck {
        if !o.isWithinAggressiveness(estimate, settings) {
            o.db.UpdateAgentRun(ctx, run.ID, "skipped", "too_complex_for_current_settings", nil)
            return nil
        }
    }

    // 4. Check for active agent config experiments and assign variant
    //    See 15-run-debugging.md for full experiment design.
    if experiment, _ := o.db.GetActiveExperiment(ctx, run.OrgID); experiment != nil {
        variant := o.assignVariant(experiment, issue.ID)
        run.ExperimentID = &experiment.ID
        run.ExperimentVariant = variant.Name
        o.db.UpdateAgentRunExperiment(ctx, run.ID, experiment.ID, variant.Name)
    }

    // 5. Get the adapter for the admin's configured agent type
    adapter := o.adapters[run.AgentType]

    // 6. Prepare the prompt (includes complexity context and experiment overrides)
    input := &AgentInput{
        Issue: issue,
        ComplexityEstimate: estimate,
        // ... other fields
    }
    prompt, _ := adapter.PreparePrompt(ctx, input)

    // 7. Create sandbox (via pluggable provider — Docker+gVisor, E2B, etc.)
    sandbox, _ := o.provider.Create(ctx, o.sandboxConfig)
    defer o.provider.Destroy(ctx, sandbox)

    // 8. Clone repo
    o.provider.CloneRepo(ctx, sandbox, run.RepoURL, run.RepoBranch, token)

    // 9. Execute agent with log streaming
    logCh := make(chan LogEntry, 100)
    go o.streamLogs(ctx, run.ID, logCh)

    result, err := adapter.Execute(ctx, sandbox, prompt, logCh)

    // 10. Store result (including confidence score)
    if err != nil {
        o.db.UpdateAgentRun(ctx, run.ID, "failed", err.Error(), nil)
        o.jobs.Enqueue(ctx, "classify_failure", map[string]interface{}{"agent_run_id": run.ID})
        return err
    }
    o.db.UpdateAgentRun(ctx, run.ID, "completed", "", result)

    // 11. Check confidence score against thresholds
    thresholds := settings.ConfidenceThresholds
    if result.ConfidenceScore < thresholds.HumanReview {
        // Low confidence — pause for human guidance
        o.db.UpdateAgentRunStatus(ctx, run.ID, "needs_human_guidance")
        o.notify.AdminAlert(ctx, run.OrgID, "low_confidence_run", run.ID)
        return nil
    }

    // 12. Enqueue validation job
    o.jobs.Enqueue(ctx, "validate", map[string]interface{}{"agent_run_id": run.ID})
    return nil
}

// isWithinAggressiveness checks if an issue's complexity is within the org's aggressiveness setting.
func (o *Orchestrator) isWithinAggressiveness(estimate *models.ComplexityEstimate, settings *models.OrgSettings) bool {
    maxTierByLevel := map[int]int{
        1: 2, // conservative: tier 1-2
        2: 3, // moderate: tier 1-3
        3: 4, // aggressive: tier 1-4
        4: 5, // maximum: tier 1-5
    }
    maxTier := maxTierByLevel[settings.ExecutionAggressiveness]
    return estimate.Tier <= maxTier
}
```

## Concurrency Control

- Max concurrent agent runs per org (default: 3, configurable)
- The orchestrator checks concurrency before starting a new run
- If at capacity, the run stays in `pending` status until a slot opens
- A background goroutine periodically checks for pending runs with available capacity

## Error Handling & Retries

- If the container crashes or times out, the run is marked `failed` with the error.
- Admins can manually retry from the UI (creates a new agent run, does not modify the failed one).
- Auto-retry is not enabled by default to avoid wasting tokens, but can be enabled in org settings for up to 1 retry.

### Confidence Gating

After each run, the orchestrator checks the agent's confidence score:

- **Above auto-proceed threshold** (default 0.8): proceed to validation
- **Between thresholds** (default 0.5-0.8): proceed but flag the PR for mandatory human review
- **Below human-review threshold** (default 0.5): pause the run, mark as `needs_human_guidance`, notify admin
- Admin can then approve (continue to validation), retry with different settings, or dismiss

### Interactive Execution Modes

In addition to the default batch mode, the orchestrator supports interactive sessions where the agent and a human collaborate in real time. See [18-interactive-sessions.md](18-interactive-sessions.md) for the full design.

| Mode | Description | Sandbox Lifetime |
|------|-------------|-----------------|
| **Batch** | Agent runs autonomously end-to-end | Short (5-30 min) |
| **Guided** | Agent can pause to ask clarifying questions mid-run | Medium (up to 2 hours) |
| **Investigate** | Human steers agent exploration in real time | Long (up to 4 hours) |
| **Pair** | Human and agent work on same branch simultaneously | Long (up to 8 hours) |

When `auto_escalate_to_guided` is enabled in org settings, the orchestrator monitors batch runs for uncertainty signals. If the agent's mid-run confidence drops below the escalation threshold, the run is automatically upgraded to guided mode — a `SessionManager` is created, the human is notified via Slack/UI, and the agent can ask questions via a WebSocket channel.

The `AgentAdapter` interface is extended with `InteractiveAgentAdapter` for agents that support mid-run questions and human directives. Agents that don't implement this interface fall back to batch-only execution.

## Token Usage Tracking

Each agent run records token usage in `agent_runs.token_usage`:

```json
{
  "input_tokens": 12500,
  "output_tokens": 3200,
  "total_cost_usd": 0.47
}
```

The dashboard aggregates this for cost monitoring. Orgs can set a monthly token budget ceiling; the orchestrator pauses auto-triggered runs if the budget is exceeded.
