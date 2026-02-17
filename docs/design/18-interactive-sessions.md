# Design: Interactive Agent Sessions

This document describes how 143.dev supports interactive, human-in-the-loop agent sessions alongside the existing batch execution mode. Interactive sessions let agents ask clarifying questions mid-run, let engineers guide agent exploration in real time, and enable pair-programming workflows where human and agent work on the same branch simultaneously.

## Overview

The existing system is **batch mode only**: issue comes in, agent runs autonomously, PR comes out. But the most effective AI coding workflows today are often interactive. This design adds three new execution modes that catch uncertainty _during_ the run rather than _after_:

| Mode | Description | Who drives | When to use |
|------|-------------|------------|-------------|
| **Batch** (existing) | Agent runs autonomously end-to-end | Agent | Simple/clear issues, high-confidence fixes |
| **Guided** (new) | Agent runs but can pause to ask clarifying questions | Agent drives, human assists | Ambiguous issues, multiple root causes, medium confidence |
| **Investigate** (new) | Engineer and agent explore together in real time | Human drives, agent executes | Complex debugging, unclear root cause, "look at X not Y" |
| **Pair** (new) | Engineer and agent work on the same branch simultaneously | Shared control | Architectural decisions, boilerplate + design split |

The key insight: low-confidence runs currently get flagged for human review _after they finish_ (or fail). The higher-leverage pattern is catching uncertainty _during_ the run — before the agent wastes tokens going down the wrong path.

## Architecture

Interactive sessions require **bidirectional real-time communication** between the agent sandbox and the human. The existing SSE log streaming is one-directional (server → client). Interactive sessions add a WebSocket channel for two-way messaging.

```
                                                  ┌──────────────┐
                                                  │   Frontend   │
                                                  │ (WebSocket)  │
                                                  └──────┬───────┘
                                                         │
                                                    WebSocket
                                                    (bidirectional)
                                                         │
                                              ┌──────────▼──────────┐
                                              │   Session Manager   │
                                              │   (Go server)       │
                                              │                     │
                                              │  - Routes messages  │
                                              │  - Manages state    │
                                              │  - Enforces timeout │
                                              └──────────┬──────────┘
                                                         │
                                                    stdin/stdout
                                                    (via Exec)
                                                         │
                                              ┌──────────▼──────────┐
                                              │      Sandbox        │
                                              │   (agent process)   │
                                              └─────────────────────┘
```

### Why WebSocket (not SSE)

| | SSE (existing) | WebSocket (interactive) |
|---|---|---|
| Direction | Server → client only | Bidirectional |
| Human input | Not possible | Human can send messages to agent |
| Agent questions | Not possible | Agent can pause and ask |
| Reconnection | Built-in auto-reconnect | Manual reconnect (handled by client library) |
| Complexity | Low | Medium |

**Decision**: Keep SSE for batch run log streaming (it works well, is simpler, and reconnects automatically). Add WebSocket for interactive sessions that need bidirectional communication. Both can coexist — a session has an SSE log stream _and_ a WebSocket message channel.

## Data Model

### `interactive_sessions`

Each interactive session tracks the real-time collaboration between a human and an agent.

```sql
CREATE TABLE interactive_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_run_id    uuid NOT NULL REFERENCES agent_runs(id),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    user_id         uuid NOT NULL REFERENCES users(id),       -- the human participant
    mode            text NOT NULL,                             -- 'guided', 'investigate', 'pair'
    status          text NOT NULL DEFAULT 'active',            -- 'active', 'waiting_for_human', 'waiting_for_agent', 'completed', 'abandoned', 'timed_out'
    started_at      timestamptz NOT NULL DEFAULT now(),
    last_activity   timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz,
    idle_timeout    interval NOT NULL DEFAULT '30 minutes',    -- session times out after inactivity
    metadata        jsonb NOT NULL DEFAULT '{}',               -- mode-specific config
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_interactive_sessions_run ON interactive_sessions (agent_run_id);
CREATE INDEX idx_interactive_sessions_user ON interactive_sessions (user_id, status);
CREATE INDEX idx_interactive_sessions_active ON interactive_sessions (org_id, status) WHERE status IN ('active', 'waiting_for_human', 'waiting_for_agent');
```

### `session_messages`

All messages exchanged during an interactive session — agent questions, human responses, status updates.

```sql
CREATE TABLE session_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      uuid NOT NULL REFERENCES interactive_sessions(id),
    sender          text NOT NULL,         -- 'agent', 'human', 'system'
    message_type    text NOT NULL,         -- 'question', 'answer', 'directive', 'status', 'checkpoint'
    content         text NOT NULL,         -- the message text
    metadata        jsonb,                 -- structured data (options for questions, file refs, etc.)
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_messages_session ON session_messages (session_id, created_at);
```

### Changes to `agent_runs`

Add columns to support interactive execution:

```sql
ALTER TABLE agent_runs ADD COLUMN execution_mode text NOT NULL DEFAULT 'batch';
-- 'batch' (existing), 'guided', 'investigate', 'pair'

ALTER TABLE agent_runs ADD COLUMN session_id uuid REFERENCES interactive_sessions(id);
```

## Mode 1: Guided Execution

The agent runs autonomously but can **pause mid-execution to ask clarifying questions**. This is the highest-leverage interactive mode — it catches the agent's uncertainty before it wastes tokens going down the wrong path.

### When Guided Mode Activates

Guided mode is used when:

1. **Explicitly requested**: Engineer clicks "Fix with guidance" instead of "Fix this"
2. **Auto-escalated**: During a batch run, the agent detects ambiguity and the system switches to guided mode (if the org has enabled auto-escalation)
3. **Complexity-triggered**: The complexity estimator flags an issue as having multiple possible root causes

### Agent Question Protocol

The agent adapter interface gains a new method for interactive communication:

```go
type InteractiveAgentAdapter interface {
    AgentAdapter // embeds the existing interface

    // ExecuteInteractive runs the agent with the ability to pause for human input.
    // The questionCh is used by the agent to send questions to the human.
    // The answerCh is used by the human to send answers back to the agent.
    ExecuteInteractive(ctx context.Context, sandbox *Sandbox, prompt *AgentPrompt,
        logCh chan<- LogEntry,
        questionCh chan<- AgentQuestion,
        answerCh <-chan HumanAnswer,
    ) (*AgentResult, error)
}

type AgentQuestion struct {
    ID          string            // unique question identifier
    Text        string            // the question text
    QuestionType string           // 'multiple_choice', 'free_text', 'confirmation', 'file_selection'
    Options     []QuestionOption  // for multiple_choice questions
    Context     string            // what the agent was doing when it hit uncertainty
    Urgency     string            // 'blocking' (agent pauses) or 'informational' (agent continues)
    Metadata    map[string]interface{}
}

type QuestionOption struct {
    ID          string
    Label       string
    Description string
}

type HumanAnswer struct {
    QuestionID  string
    Text        string            // free-text answer or selected option ID
    SelectedIDs []string          // for multi-select questions
}
```

### Implementation: Claude Code Adapter (Guided)

The Claude Code CLI supports interactive mode via stdin/stdout. The adapter wraps this to bridge the question/answer channels:

```go
func (a *ClaudeCodeAdapter) ExecuteInteractive(ctx context.Context, sandbox *Sandbox,
    prompt *AgentPrompt, logCh chan<- LogEntry,
    questionCh chan<- AgentQuestion, answerCh <-chan HumanAnswer,
) (*AgentResult, error) {

    // 1. Start agent process with interactive flag
    cmd := fmt.Sprintf("claude --prompt %q --interactive --output-format json", prompt.UserPrompt)
    stdin, stdout, stderr := sandbox.ExecInteractive(ctx, cmd)

    // 2. Scan stdout for structured output
    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        line := scanner.Text()
        msg := parseAgentOutput(line)

        switch msg.Type {
        case "log":
            logCh <- msg.ToLogEntry()

        case "question":
            // Agent is asking a question — forward to human
            q := AgentQuestion{
                ID:           msg.QuestionID,
                Text:         msg.Text,
                QuestionType: msg.QuestionType,
                Options:      msg.Options,
                Context:      msg.Context,
                Urgency:      "blocking",
            }
            questionCh <- q

            // Wait for human answer
            answer := <-answerCh
            // Forward answer to agent stdin
            answerJSON, _ := json.Marshal(answer)
            fmt.Fprintf(stdin, "%s\n", answerJSON)

        case "result":
            return msg.ToAgentResult(), nil
        }
    }
    return nil, fmt.Errorf("agent process exited unexpectedly")
}
```

### Auto-Escalation from Batch to Guided

During a batch run, the agent's prompt includes instructions to signal uncertainty via structured output. If the orchestrator detects an uncertainty signal, it can auto-escalate to guided mode:

```go
func (o *Orchestrator) RunAgent(ctx context.Context, run *models.AgentRun) error {
    // ... existing setup ...

    if run.ExecutionMode == "batch" && settings.AutoEscalateToGuided {
        // Wrap the log channel to detect uncertainty signals
        wrappedLogCh := make(chan LogEntry, 100)
        go func() {
            for entry := range wrappedLogCh {
                logCh <- entry
                if entry.Level == "uncertainty" && entry.Metadata["confidence"].(float64) < settings.GuidedEscalationThreshold {
                    // Escalate to guided mode
                    o.escalateToGuided(ctx, run, entry)
                }
            }
        }()
    }

    // ... rest of execution ...
}

func (o *Orchestrator) escalateToGuided(ctx context.Context, run *models.AgentRun, trigger LogEntry) {
    // 1. Update run mode
    o.db.UpdateAgentRunMode(ctx, run.ID, "guided")

    // 2. Create interactive session
    session := &models.InteractiveSession{
        AgentRunID: run.ID,
        OrgID:      run.OrgID,
        Mode:       "guided",
        Status:     "waiting_for_human",
    }
    o.db.CreateInteractiveSession(ctx, session)

    // 3. Notify humans
    o.notify.InteractiveSessionNeeded(ctx, run.OrgID, session.ID, NotifyPayload{
        RunID:   run.ID,
        Reason:  "Agent encountered uncertainty during execution",
        Context: trigger.Message,
    })
}
```

### Notification Channels

When the agent asks a question, the system notifies the engineer via:

1. **UI notification**: Real-time badge/toast in the 143.dev dashboard ("Agent needs your input on ENG-1234")
2. **Slack** (if configured): DM or channel message with the question and a deep-link to the session
3. **Email** (optional): For non-urgent questions when the engineer is offline

```go
type NotificationService interface {
    InteractiveSessionNeeded(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, payload NotifyPayload) error
}

type NotifyPayload struct {
    RunID       uuid.UUID
    SessionID   uuid.UUID
    Reason      string
    Context     string
    Question    *AgentQuestion  // nil for investigate/pair invitations
    DeepLink    string          // URL to the session in the UI
}
```

### Question Timeout

If the human doesn't respond within a configurable timeout, the agent either:

- **Continues with its best guess** (if the question was informational)
- **Pauses the run** (if the question was blocking) and notifies the engineer

```go
func (o *Orchestrator) waitForAnswer(ctx context.Context, session *models.InteractiveSession, question AgentQuestion) (*HumanAnswer, error) {
    timeout := session.IdleTimeout
    if question.Urgency == "informational" {
        timeout = 2 * time.Minute // shorter timeout for non-blocking questions
    }

    select {
    case answer := <-o.answerCh:
        return &answer, nil
    case <-time.After(timeout):
        if question.Urgency == "blocking" {
            o.db.UpdateSessionStatus(ctx, session.ID, "timed_out")
            o.db.UpdateAgentRunStatus(ctx, session.AgentRunID, "needs_human_guidance")
            return nil, fmt.Errorf("question timed out after %v", timeout)
        }
        // Informational — let agent continue with its default
        return &HumanAnswer{QuestionID: question.ID, Text: "__timeout_continue__"}, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

## Mode 2: Investigate (Human-Guided Exploration)

An engineer clicks "Investigate this" on an issue and gets a **live session** where the agent explores the codebase while the human steers. The human sends directives ("look at the database layer, not the API handler") and the agent follows them.

### How It Works

```
Engineer clicks "Investigate" on issue ENG-1234
        │
        ▼
  System creates sandbox + clones repo
  System creates interactive session
  Agent starts with initial investigation prompt
        │
        ▼
  ┌─────────────────────────────────────────────┐
  │               Live Session UI               │
  │                                             │
  │  ┌─────────────────────────────────────┐    │
  │  │  Agent Activity (streaming logs)    │    │
  │  │  > Reading src/api/users.go...      │    │
  │  │  > Found potential null deref at    │    │
  │  │    line 142                         │    │
  │  │  > Checking caller chain...         │    │
  │  └─────────────────────────────────────┘    │
  │                                             │
  │  ┌─────────────────────────────────────┐    │
  │  │  Chat / Directives                  │    │
  │  │                                     │    │
  │  │  Human: "Look at the database layer │    │
  │  │          instead, I think the issue │    │
  │  │          is in the query"           │    │
  │  │                                     │    │
  │  │  Agent: "OK, investigating          │    │
  │  │          db/queries/users.sql..."   │    │
  │  │                                     │    │
  │  │  [___________________________] Send │    │
  │  └─────────────────────────────────────┘    │
  │                                             │
  │  [Generate Fix]  [Save Findings]  [End]     │
  └─────────────────────────────────────────────┘
```

### Investigation Agent Prompt

The agent runs with a different prompt than batch mode. Instead of "fix this issue", the prompt is "investigate this issue and report findings while following human guidance":

```go
func (a *ClaudeCodeAdapter) PrepareInvestigationPrompt(ctx context.Context, input *AgentInput) (*AgentPrompt, error) {
    prompt := &AgentPrompt{
        SystemPrompt: `You are investigating a production issue interactively with an engineer.

Your job is to:
1. Explore the codebase methodically, reporting what you find
2. Follow the engineer's directives when they redirect your investigation
3. Identify the root cause and explain your reasoning
4. When you have enough context, offer to generate a fix

Communication protocol:
- Stream your findings as you discover them
- When you receive a directive from the engineer, acknowledge it and adjust your investigation
- If you find multiple possible causes, present them and ask which to pursue
- Do NOT generate a fix until the engineer says to proceed

Output structured investigation findings as you go.`,
        UserPrompt: fmt.Sprintf("Investigate this issue:\n\n%s\n\n%s", input.Issue.Title, input.Issue.Description),
    }
    return prompt, nil
}
```

### Directives

Human directives during an investigation session are sent as `session_messages` with `message_type = 'directive'`. The session manager injects them into the agent's stdin:

```go
func (sm *SessionManager) HandleHumanDirective(ctx context.Context, sessionID uuid.UUID, directive string) error {
    session, _ := sm.db.GetInteractiveSession(ctx, sessionID)

    // 1. Store the message
    sm.db.CreateSessionMessage(ctx, &models.SessionMessage{
        SessionID:   sessionID,
        Sender:      "human",
        MessageType: "directive",
        Content:     directive,
    })

    // 2. Forward to the agent process via stdin
    sm.agentStdin[sessionID].Write([]byte(fmt.Sprintf(`{"type":"directive","content":%q}\n`, directive)))

    // 3. Update session activity timestamp
    sm.db.UpdateSessionActivity(ctx, sessionID)

    return nil
}
```

### Transitioning from Investigate to Fix

When the investigation is complete, the engineer can click "Generate Fix". This transitions the session:

1. The agent's investigation findings are captured as context
2. A new agent run is created in batch mode with the investigation findings injected into the prompt
3. The sandbox is reused (repo is already cloned, agent already has context)

```go
func (sm *SessionManager) TransitionToFix(ctx context.Context, sessionID uuid.UUID) (*models.AgentRun, error) {
    session, _ := sm.db.GetInteractiveSession(ctx, sessionID)
    messages, _ := sm.db.GetSessionMessages(ctx, sessionID)

    // Compile investigation findings
    findings := compileFindings(messages)

    // Create a new agent run with investigation context
    run := &models.AgentRun{
        IssueID:       session.AgentRun.IssueID,
        OrgID:         session.OrgID,
        ExecutionMode: "batch",      // fix runs in batch after investigation
        AgentType:     session.AgentRun.AgentType,
    }
    run.ID = sm.db.CreateAgentRun(ctx, run)

    // Inject investigation findings into the prompt
    input := &AgentInput{
        Issue:                 session.AgentRun.Issue,
        InvestigationFindings: findings,
        // ... other fields
    }

    // Reuse the existing sandbox
    sm.orchestrator.RunAgentInSandbox(ctx, run, session.Sandbox, input)

    return run, nil
}
```

## Mode 3: Pair Programming

The most advanced mode. The engineer and agent work on the **same branch simultaneously**. The agent handles boilerplate and routine tasks while the human makes architectural decisions and writes the tricky parts.

### How It Works

```
Engineer clicks "Pair on this" on issue ENG-1234
        │
        ▼
  System creates a working branch: 143/pair/{issue-id}/{slug}
  System creates sandbox + clones repo on that branch
  Engineer clones/checks out the same branch locally
        │
        ▼
  ┌─────────────────────────────────────────────┐
  │         Pair Programming Session UI         │
  │                                             │
  │  ┌───────────────────┬─────────────────┐    │
  │  │  Agent Activity   │  Branch Status  │    │
  │  │                   │                 │    │
  │  │  Working on:      │  Last push:     │    │
  │  │  Adding tests for │  2 min ago      │    │
  │  │  user validation  │  by: engineer   │    │
  │  │                   │                 │    │
  │  │  Files modified:  │  Agent changes: │    │
  │  │  - user_test.go   │  3 files        │    │
  │  │                   │  Human changes: │    │
  │  │                   │  2 files        │    │
  │  └───────────────────┴─────────────────┘    │
  │                                             │
  │  ┌─────────────────────────────────────┐    │
  │  │  Task Assignment Chat               │    │
  │  │                                     │    │
  │  │  Human: "I'll handle the API route  │    │
  │  │          design. You write the       │    │
  │  │          tests and the DB migration" │    │
  │  │                                     │    │
  │  │  Agent: "Got it. Starting with the  │    │
  │  │          migration for the new       │    │
  │  │          sessions table..."          │    │
  │  │                                     │    │
  │  │  [___________________________] Send │    │
  │  └─────────────────────────────────────┘    │
  │                                             │
  │  [Push Agent Changes]  [Sync]  [Finish]     │
  └─────────────────────────────────────────────┘
```

### Branch Coordination

The pair session uses a shared Git branch. The agent works in the sandbox, the human works locally. Coordination happens via Git:

```go
type PairSession struct {
    SessionID     uuid.UUID
    BranchName    string
    SandboxLastCommit string  // last commit SHA from the agent
    HumanLastCommit   string  // last commit SHA from the human
}

func (ps *PairSessionManager) SyncBranch(ctx context.Context, session *PairSession) error {
    // 1. Agent pushes its changes to the branch
    ps.sandbox.Exec(ctx, session.Sandbox, "git add -A && git commit -m 'agent: work in progress' && git push origin "+session.BranchName)

    // 2. Human pulls and pushes via their local git
    // (happens outside 143.dev — human uses their normal git workflow)

    // 3. Agent pulls human's changes
    ps.sandbox.Exec(ctx, session.Sandbox, "git pull --rebase origin "+session.BranchName)

    return nil
}
```

### Conflict Resolution

When the agent and human edit the same file:

1. **Prevention**: The task assignment chat helps avoid conflicts — "I'll work on X, you work on Y"
2. **Detection**: Before the agent commits, it checks for upstream changes and rebases
3. **Resolution**: If rebase fails, the agent reports the conflict to the human via the session chat and waits for the human to resolve it

```go
func (ps *PairSessionManager) AgentCommitAndPush(ctx context.Context, session *PairSession, commitMsg string) error {
    // 1. Fetch latest from remote
    ps.sandbox.Exec(ctx, session.Sandbox, "git fetch origin "+session.BranchName)

    // 2. Try to rebase onto remote
    exitCode, _ := ps.sandbox.Exec(ctx, session.Sandbox, "git rebase origin/"+session.BranchName)
    if exitCode != 0 {
        // Conflict detected — abort rebase and notify human
        ps.sandbox.Exec(ctx, session.Sandbox, "git rebase --abort")
        ps.sendMessage(ctx, session.SessionID, "agent", "status",
            "I have changes to push but they conflict with your latest commit. Could you pull my changes and resolve the conflict?")
        return fmt.Errorf("rebase conflict — waiting for human resolution")
    }

    // 3. Push
    ps.sandbox.Exec(ctx, session.Sandbox, "git push origin "+session.BranchName)
    return nil
}
```

### Task Division

The pair session prompt instructs the agent to coordinate with the human on task division:

```go
func (a *ClaudeCodeAdapter) PreparePairPrompt(ctx context.Context, input *AgentInput) (*AgentPrompt, error) {
    prompt := &AgentPrompt{
        SystemPrompt: `You are pair programming with an engineer on a shared Git branch.

Rules:
1. Coordinate task division — ask the engineer what they want to handle vs. what you should handle
2. Work on your assigned tasks. Do NOT modify files the engineer is working on unless asked
3. Commit frequently with descriptive messages prefixed with "agent: "
4. When you finish a task, report it and ask for the next assignment
5. If you need to touch a file the engineer is working on, ask first
6. Before pushing, always pull and rebase to avoid conflicts

You have full access to the codebase in the sandbox. The engineer is working on their local machine on the same branch.`,
    }
    return prompt, nil
}
```

## WebSocket Protocol

### Connection

The frontend connects to the session WebSocket:

```
ws://host/api/v1/sessions/{session_id}/ws
```

Authentication via the same session cookie used for HTTP requests.

### Message Format

All WebSocket messages use a common envelope:

```json
{
    "type": "question|answer|directive|status|log|checkpoint|sync",
    "sender": "agent|human|system",
    "id": "msg-uuid",
    "timestamp": "2025-01-15T10:30:00Z",
    "payload": { ... }
}
```

### Message Types

| Type | Sender | Description |
|------|--------|-------------|
| `question` | agent | Agent asks a clarifying question (guided mode) |
| `answer` | human | Human responds to an agent question |
| `directive` | human | Human gives the agent a direction (investigate/pair mode) |
| `status` | agent/system | Status update (agent started task, finished task, etc.) |
| `log` | agent | Streaming log entry (also sent via SSE for backward compat) |
| `checkpoint` | agent | Agent saves investigation findings or progress |
| `sync` | system | Branch sync notification (pair mode) |

### Server-Side WebSocket Handler

```go
func (h *SessionHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
    sessionID := chi.URLParam(r, "sessionID")
    session, err := h.db.GetInteractiveSession(r.Context(), sessionID)
    if err != nil {
        http.Error(w, "session not found", http.StatusNotFound)
        return
    }

    // Upgrade to WebSocket
    conn, err := h.upgrader.Upgrade(w, r, nil)
    if err != nil {
        return
    }
    defer conn.Close()

    // Register this connection with the session manager
    h.sessionManager.RegisterConnection(sessionID, conn)
    defer h.sessionManager.UnregisterConnection(sessionID, conn)

    // Read loop — human messages come in here
    for {
        _, msgBytes, err := conn.ReadMessage()
        if err != nil {
            break
        }

        var msg SessionMessage
        json.Unmarshal(msgBytes, &msg)

        switch msg.Type {
        case "answer":
            h.sessionManager.HandleHumanAnswer(r.Context(), sessionID, msg.Payload)
        case "directive":
            h.sessionManager.HandleHumanDirective(r.Context(), sessionID, msg.Payload.Content)
        }
    }
}
```

## Session Manager

The `SessionManager` is the core coordinator. It manages the lifecycle of interactive sessions, routes messages between humans and agents, and enforces timeouts.

```go
type SessionManager struct {
    db           *db.DB
    orchestrator *Orchestrator
    notify       NotificationService
    connections  map[uuid.UUID][]*websocket.Conn // session_id -> active WebSocket connections
    agentStdin   map[uuid.UUID]io.Writer         // session_id -> agent process stdin
    mu           sync.RWMutex
}

func (sm *SessionManager) CreateSession(ctx context.Context, req CreateSessionRequest) (*models.InteractiveSession, error) {
    // 1. Create the agent run
    run := &models.AgentRun{
        IssueID:       req.IssueID,
        OrgID:         req.OrgID,
        ExecutionMode: req.Mode,
        AgentType:     req.AgentType,
        Status:        "running",
    }
    sm.db.CreateAgentRun(ctx, run)

    // 2. Create the session
    session := &models.InteractiveSession{
        AgentRunID:  run.ID,
        OrgID:       req.OrgID,
        UserID:      req.UserID,
        Mode:        req.Mode,
        Status:      "active",
        IdleTimeout: req.IdleTimeout,
    }
    sm.db.CreateInteractiveSession(ctx, session)

    // 3. Update the run with the session link
    sm.db.UpdateAgentRunSession(ctx, run.ID, session.ID)

    // 4. Start the agent in a sandbox
    go sm.runInteractiveAgent(ctx, run, session)

    return session, nil
}

// Idle timeout watcher — runs as a background goroutine per session
func (sm *SessionManager) watchIdleTimeout(ctx context.Context, sessionID uuid.UUID) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            session, _ := sm.db.GetInteractiveSession(ctx, sessionID)
            if session.Status == "completed" || session.Status == "abandoned" {
                return
            }
            if time.Since(session.LastActivity) > session.IdleTimeout {
                sm.db.UpdateSessionStatus(ctx, sessionID, "timed_out")
                sm.broadcast(sessionID, SessionMessage{
                    Type:    "status",
                    Sender:  "system",
                    Payload: map[string]string{"message": "Session timed out due to inactivity"},
                })
                // Cleanup sandbox
                sm.orchestrator.CleanupSession(ctx, session)
                return
            }
        case <-ctx.Done():
            return
        }
    }
}
```

## Sandbox Lifecycle for Interactive Sessions

Interactive sessions use **long-lived sandboxes** unlike batch runs which create-and-destroy quickly. This has implications for resource management.

### Extended Timeouts

| | Batch | Guided | Investigate | Pair |
|---|---|---|---|---|
| Default timeout | 5 min | 30 min | 1 hour | 4 hours |
| Max timeout | 30 min | 2 hours | 4 hours | 8 hours |
| Idle timeout | N/A | 30 min | 30 min | 1 hour |

### Resource Management

Long-lived sandboxes tie up compute resources. The system enforces limits:

```go
type InteractiveSessionLimits struct {
    MaxConcurrentSessions    int           // per org (default: 2)
    MaxSessionDuration       time.Duration // absolute max (default: 8 hours)
    IdleTimeoutDefault       time.Duration // default idle timeout (default: 30 min)
    SandboxCPU               float64       // CPU cores (default: 2)
    SandboxMemoryMB          int           // memory (default: 4096)
}
```

When an org is at its concurrent session limit, new session requests are queued or rejected with a message.

### Sandbox Persistence

For pair mode, the sandbox needs to persist across agent restarts (e.g., if the agent finishes a task and waits for the next assignment). The sandbox stays alive as long as the session is active.

```go
func (sm *SessionManager) runInteractiveAgent(ctx context.Context, run *models.AgentRun, session *models.InteractiveSession) {
    // Create sandbox with extended timeout
    config := sm.sandboxConfig
    config.Timeout = session.IdleTimeout * 2 // sandbox outlives session timeout

    sandbox, _ := sm.orchestrator.provider.Create(ctx, config)
    defer sm.orchestrator.provider.Destroy(ctx, sandbox)

    // Clone repo
    sm.orchestrator.provider.CloneRepo(ctx, sandbox, run.RepoURL, run.RepoBranch, token)

    // For pair mode, checkout or create the pair branch
    if session.Mode == "pair" {
        branchName := fmt.Sprintf("143/pair/%s/%s", run.IssueID[:8], slugify(run.Issue.Title))
        sandbox.Exec(ctx, "git checkout -b "+branchName)
        sandbox.Exec(ctx, "git push -u origin "+branchName)
        sm.broadcast(session.ID, SessionMessage{
            Type:    "status",
            Sender:  "system",
            Payload: map[string]string{
                "message": fmt.Sprintf("Pair branch created: %s", branchName),
                "branch":  branchName,
            },
        })
    }

    // Run the agent in interactive mode
    adapter := sm.orchestrator.adapters[run.AgentType]
    if interactive, ok := adapter.(InteractiveAgentAdapter); ok {
        questionCh := make(chan AgentQuestion, 10)
        answerCh := make(chan HumanAnswer, 10)

        // Bridge questions/answers to WebSocket
        go sm.bridgeQuestions(ctx, session.ID, questionCh)
        go sm.bridgeAnswers(ctx, session.ID, answerCh)

        logCh := make(chan LogEntry, 100)
        go sm.streamLogs(ctx, run.ID, session.ID, logCh)

        result, err := interactive.ExecuteInteractive(ctx, sandbox, prompt, logCh, questionCh, answerCh)
        // ... handle result
    }
}
```

## Notification Integration

Interactive sessions integrate with the existing notification channels (UI, Slack) to alert engineers when their input is needed.

### Slack Integration

```go
func (n *SlackNotifier) InteractiveSessionNeeded(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, payload NotifyPayload) error {
    channel := n.getConfiguredChannel(orgID)

    blocks := []slack.Block{
        slack.NewSectionBlock(
            slack.NewTextBlockObject("mrkdwn",
                fmt.Sprintf(":robot_face: *Agent needs your input*\n\n%s\n\n<%s|Open Session>",
                    payload.Reason, payload.DeepLink),
                false, false),
            nil, nil,
        ),
    }

    if payload.Question != nil {
        // Include the question inline for quick response
        blocks = append(blocks, slack.NewSectionBlock(
            slack.NewTextBlockObject("mrkdwn",
                fmt.Sprintf("*Question:* %s", payload.Question.Text),
                false, false),
            nil, nil,
        ))

        // Add option buttons if it's multiple choice
        if payload.Question.QuestionType == "multiple_choice" {
            var buttons []slack.BlockElement
            for _, opt := range payload.Question.Options {
                buttons = append(buttons, slack.NewButtonBlockElement(
                    fmt.Sprintf("answer_%s_%s", sessionID, opt.ID),
                    opt.Label,
                    slack.NewTextBlockObject("plain_text", opt.Label, false, false),
                ))
            }
            blocks = append(blocks, slack.NewActionBlock("answer_actions", buttons...))
        }
    }

    return n.client.PostMessage(channel, slack.MsgOptionBlocks(blocks...))
}
```

This enables engineers to answer agent questions directly from Slack without opening the 143.dev UI.

## API Endpoints

New routes for interactive sessions:

```
/api/v1/
├── /sessions
│   ├── POST   /                    # create interactive session
│   ├── GET    /                    # list active sessions
│   ├── GET    /:id                 # get session details
│   ├── GET    /:id/ws              # WebSocket connection
│   ├── GET    /:id/messages        # get message history
│   ├── POST   /:id/messages        # send message (REST fallback for non-WebSocket clients)
│   ├── POST   /:id/end             # end session gracefully
│   ├── POST   /:id/generate-fix    # transition investigate session to fix generation
│   └── POST   /:id/sync            # trigger branch sync (pair mode)
```

## Frontend Components

New UI components for interactive sessions:

```
frontend/src/
├── app/
│   ├── sessions/
│   │   ├── page.tsx                  # active sessions list
│   │   └── [id]/
│   │       └── page.tsx              # live session view
├── components/
│   ├── sessions/
│   │   ├── session-chat.tsx          # bidirectional message feed
│   │   ├── session-log-viewer.tsx    # streaming logs (reuses run-log-viewer)
│   │   ├── session-question.tsx      # agent question display with answer input
│   │   ├── session-status-bar.tsx    # session mode, duration, status
│   │   ├── session-branch-status.tsx # pair mode branch sync indicator
│   │   └── session-actions.tsx       # end session, generate fix, sync buttons
├── hooks/
│   ├── use-websocket.ts              # WebSocket hook for interactive sessions
│   └── use-session.ts                # TanStack Query hooks for session data
```

### WebSocket Hook

```tsx
function useSessionWebSocket(sessionId: string) {
    const [messages, setMessages] = useState<SessionMessage[]>([]);
    const [status, setStatus] = useState<'connecting' | 'connected' | 'disconnected'>('connecting');
    const wsRef = useRef<WebSocket | null>(null);

    useEffect(() => {
        const ws = new WebSocket(`${WS_BASE}/api/v1/sessions/${sessionId}/ws`);

        ws.onopen = () => setStatus('connected');
        ws.onclose = () => {
            setStatus('disconnected');
            // Auto-reconnect after 2 seconds
            setTimeout(() => {
                // reconnect logic
            }, 2000);
        };
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            setMessages(prev => [...prev, msg]);
        };

        wsRef.current = ws;
        return () => ws.close();
    }, [sessionId]);

    const sendMessage = useCallback((type: string, content: string, metadata?: any) => {
        wsRef.current?.send(JSON.stringify({ type, sender: 'human', content, metadata }));
    }, []);

    return { messages, status, sendMessage };
}
```

## Org Settings

Interactive sessions are configurable per org in `organizations.settings`:

```json
{
    "interactive_sessions": {
        "enabled": true,
        "auto_escalate_to_guided": true,
        "guided_escalation_threshold": 0.5,
        "max_concurrent_sessions": 2,
        "default_idle_timeout_minutes": 30,
        "pair_mode_enabled": true,
        "notification_channels": ["ui", "slack"]
    }
}
```

## Job Queue

New job types:

| Job Type | Queue | Trigger | Description |
|----------|-------|---------|-------------|
| `create_interactive_session` | `agent` | Manual (UI action) | Spin up sandbox and start interactive session |
| `session_timeout_check` | `system` | Scheduled (every 1 min) | Check for idle sessions and time them out |
| `generate_fix_from_investigation` | `agent` | "Generate Fix" button in investigate mode | Create a batch agent run from investigation findings |

## Connection with Other Design Docs

**Agent Orchestrator (doc 06)**:
- `AgentAdapter` interface gains `InteractiveAgentAdapter` extension with `ExecuteInteractive` method
- `Orchestrator.RunAgent` supports the new `execution_mode` field and auto-escalation
- Sandbox lifecycle changes: long-lived sandboxes for interactive sessions, idle timeout management

**Database Schema (doc 01)**:
- Two new tables: `interactive_sessions`, `session_messages`
- `agent_runs` gains `execution_mode` and `session_id` columns

**Frontend (doc 03)**:
- New `/sessions` page and session detail view
- WebSocket hook alongside existing SSE hook
- New components: session chat, question display, branch status

**API Server (doc 02)**:
- New `/sessions` route group with WebSocket upgrade endpoint
- `gorilla/websocket` added as dependency
- `SessionManager` service added to project structure

**Smart Routing (doc 12)**:
- Complexity estimation can recommend guided mode for issues with ambiguous root causes
- Issues classified as tier 4-5 may auto-suggest investigate mode

**Codebase Context (doc 14)**:
- Interactive sessions receive the same context injection as batch runs
- Investigation findings can feed back into the file map and architecture docs

**PR & Ship (doc 08)**:
- Pair mode creates branches using the same branch naming convention: `143/pair/{issue-id}/{slug}`
- When a pair session ends and produces a fix, the normal PR creation flow takes over

## Build Order

This feature is built in **Phase 4.5**, after basic agent execution (Phase 4) is working but before validation (Phase 5). The modes are built incrementally:

1. **WebSocket infrastructure** — WebSocket upgrade handler, `SessionManager`, `interactive_sessions` and `session_messages` tables
2. **Guided mode** — agent question/answer protocol, auto-escalation from batch, notification integration
3. **Investigate mode** — investigation prompts, directive handling, "Generate Fix" transition
4. **Pair mode** — branch coordination, sync mechanism, task division protocol
5. **Slack integration** — answer questions directly from Slack
6. **Session UI** — session list page, live session view, all session components
