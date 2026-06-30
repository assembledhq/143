# Design: Live Agent Command Handles

> **Status:** Not Started | **Last reviewed:** 2026-06-30

> **Related docs:** [overall.md](../overall.md), [60-agent-runtime-timeouts-and-checkpointed-shutdown.md](60-agent-runtime-timeouts-and-checkpointed-shutdown.md), [68-sandbox-agent-tabs-and-threads.md](68-sandbox-agent-tabs-and-threads.md)

## Summary

143 should stop treating interactive coding-agent execution as a fire-and-forget
`ExecStream(...)` call plus out-of-band cancellation hacks.

The long-term runtime primitive should be a **live command handle** owned by the
sandbox provider. That handle should represent one running agent process and
should support:

1. streaming stdout and stderr
2. optionally keeping stdin open
3. optionally allocating a TTY
4. waiting for process exit
5. delivering agent-specific graceful interrupts like `Ctrl+C` or `Esc`
6. force-closing the underlying transport if the process wedges

This moves cancellation and input delivery into the provider/runtime layer,
which is where it belongs. Adapters should describe what kind of runtime they
need. They should not be responsible for shell wrappers, pidfiles, ttyfiles, or
inline PTY supervisors.

## Problem

Today the interactive agent path is built around:

- `SandboxProvider.ExecStream(...)` for output streaming
- `CancelRegistry` separately tracking how to stop the process
- in-sandbox pidfile / ttyfile conventions
- shell wrappers to make the real process stoppable
- an inline Python PTY shim for agents that want keyboard interrupts like `Esc`

That shape works, but it is the wrong abstraction boundary.

### What is wrong with the current shape

1. `ExecStream(...)` is too weak.
   - It gives us output, but not a live stdin handle.
   - It does not give us a first-class running-process object.
   - It forces cancellation to be modeled out-of-band.

2. Cancellation has leaked into shell glue.
   - pidfiles and ttyfiles are implementation artifacts, not product concepts.
   - wrapper behavior is now coupled to adapter command construction.

3. TTY-only agents are awkward.
   - `Esc` is not a signal.
   - Without a retained stdin/TTY handle, we are forced into wrapper-side PTY
     allocation.

4. Error semantics degrade under wrappers.
   - PTY-backed wrappers naturally merge stderr into visible output.
   - This pushes transport quirks upward into adapter parsing and result shaping.

5. The current runtime model will not scale cleanly to thread-scoped execution.
   - Multi-tab/thread sessions need one runtime object per thread.
   - Per-thread runtime budgets, cancellation, status, and recovery are cleaner
     if the process is a first-class handle instead of “a shell command we once
     asked Docker to run.”

## Goals

This design should satisfy the following:

1. **Provider-owned runtime control.** A running agent command is a real object
   in the provider layer, not an incidental side effect of `ExecStream(...)`.
2. **Native graceful interruption.** Agents that want `Ctrl+C`, `Esc`, or a
   future custom input should receive it through the runtime handle.
3. **No adapter-side process supervision glue.** Adapters should describe
   runtime requirements, not hand-roll wrappers.
4. **Incremental adoption.** One-shot utilities like `git diff`, `tar`, and
   prompt-file writes can continue to use `Exec(...)` / `ExecStream(...)`.
5. **Thread-ready runtime model.** The same abstraction should make sense when a
   session has multiple concurrent agent threads.
6. **Observable lifecycle.** The runtime object should have a stable identity,
   clear exit status, and explicit cancellation path.
7. **Provider extensibility.** Docker should implement this first, but the
   abstraction should make sense for future providers too.

## Non-Goals

This design does not require:

- deleting `Exec(...)` or `ExecStream(...)` for all existing call sites
- changing prompt construction or agent-specific output parsing
- unifying every agent onto one transport mode immediately
- solving snapshot/restore fidelity by itself

## Design Principles

### 1. Separate one-shot command execution from interactive runtime execution

Most sandbox commands are utilities:

- `git diff`
- `cat`
- `tar`
- auth bootstrap helpers

Those should keep a simple one-shot API.

Interactive coding-agent turns are different:

- they run for a long time
- they stream output continuously
- they may need follow-up cancellation
- some want live keyboard input

Those should have a different API.

### 2. Model cancellation semantically, not as shell mechanics

The adapter should declare what graceful stop means:

- `ctrl_c`
- `escape`
- future methods if needed

The provider/runtime handle should decide how to deliver that:

- write `0x03` to a TTY-backed stdin
- write `0x1b` to stdin
- send `SIGINT` to a tracked child or process group
- reject unsupported methods explicitly

### 3. Keep transport quirks below the adapter boundary

If Docker needs a TTY, stdin attachment, or a small helper process to supervise
the real child, that should be internal to the provider/runtime implementation.
The adapter should not know or care.

## Proposed Runtime Abstraction

### Keep the existing `SandboxProvider` for one-shot operations

We should keep:

- `Exec(...)`
- `ExecStream(...)`
- `ReadFile(...)`
- `WriteFile(...)`
- `Snapshot(...)`
- `Restore(...)`

These remain useful for bootstrap and utility commands.

### Add a new interactive runtime capability

Add a provider capability dedicated to long-lived interactive commands.

Illustrative shape:

```go
type InteractiveCommandSpec struct {
    Cmd        string
    WorkingDir string

    // Whether the command needs a TTY-backed transport.
    TTY bool

    // Whether stdin must remain writable after start.
    OpenStdin bool

    // Whether stdout/stderr should remain logically separated when possible.
    SplitOutput bool
}

type InteractiveCommandHandle interface {
    // Stable provider-level identifier for logs/debugging.
    ID() string

    // Stream lifecycle.
    Stdout() io.Reader
    Stderr() io.Reader

    // Optional runtime control.
    WriteInput(ctx context.Context, data []byte) error
    CloseInput(ctx context.Context) error

    // Graceful and forceful shutdown.
    Interrupt(ctx context.Context, spec agent.CancellationSpec) error
    Kill(ctx context.Context) error

    // Wait returns the exit code when the process finishes.
    Wait(ctx context.Context) (int, error)

    // Close releases provider-side resources such as hijacked connections.
    Close() error
}

type InteractiveSandboxProvider interface {
    StartInteractiveCommand(ctx context.Context, sb *Sandbox, spec InteractiveCommandSpec) (InteractiveCommandHandle, error)
}
```

The exact names may change, but the core idea should not:

- agent turns start through a **live handle**
- the handle owns transport and lifecycle
- cancellation is a method on the handle

### Why a separate capability instead of mutating `ExecStream(...)`

Because the semantics are different.

`ExecStream(...)` is:

- one call
- one callback
- one exit code

Interactive runtime needs:

- a start phase
- retained control over stdin/TTY
- a wait phase
- resource cleanup
- interrupt semantics

Overloading `ExecStream(...)` to do both would make the interface vague and
push too much state into hidden provider internals.

## Adapter Runtime Requirements

On top of `CancellationSpec`, adapters should declare an execution profile.

Illustrative shape:

```go
type AgentRuntimeProfile struct {
    Cancellation agent.CancellationSpec

    // Whether the CLI requires a TTY to honor its documented cancel behavior.
    RequiresTTY bool

    // Whether stdin must remain writable while the command runs.
    RequiresOpenStdin bool

    // Whether stderr must remain separated for diagnostics.
    PreferSplitOutput bool
}
```

Examples:

- `Pi`
  - `Cancellation: escape`
  - `RequiresTTY: true`
  - `RequiresOpenStdin: true`
- `Codex`, `Claude Code`, `Gemini`
  - likely `Cancellation: ctrl_c`
  - may begin with `RequiresTTY: false`
  - can still move to TTY later if that becomes the cleanest transport
- `Amp`
  - keep default `ctrl_c` until its stop contract is better defined

## Docker Implementation Strategy

### End state

Docker should implement `StartInteractiveCommand(...)` directly.

That implementation should own:

- `ContainerExecCreate(...)`
- `ContainerExecAttach(...)`
- optional stdin attachment
- optional TTY allocation
- a long-lived hijacked connection
- output demux / stream fanout
- interruption behavior
- wait / cleanup

### Important note: Docker may still need an internal helper

The public abstraction should be a live command handle. That does **not**
guarantee the Docker implementation can always avoid an internal shim.

Two cases matter:

1. **TTY/input-driven interruption**
   - `Esc` can be delivered by writing `0x1b` to stdin when the command is
     started with open stdin and a TTY.

2. **Signal-driven interruption without TTY**
   - Docker does not expose a perfect “send signal to this exec process” API.
   - If we insist on true signal delivery for some commands, Docker may still
     need an internal runtime helper or tracked child process group.

That is acceptable. The design goal is **not** “zero helpers at any layer.” The
goal is “no adapter-side shell/python glue, and no product logic depending on
filesystem sidecars like pidfiles.”

If Docker needs a small static helper in the sandbox image, that is a valid
implementation detail. It is still much cleaner than inline shell/Python
wrappers because:

- it becomes provider-owned
- it is testable in isolation
- the adapter layer stops knowing about it

## Orchestrator and Cancel Path Changes

### Today

The orchestrator:

1. creates a sandbox
2. calls adapter execution
3. separately registers sandbox/provider with `CancelRegistry`
4. `CancelRegistry` later reconstructs how to stop the process

### Proposed

The adapter execution path should:

1. start a live command handle through the provider
2. immediately register that handle with `CancelRegistry`
3. stream output from the handle
4. wait on the handle
5. deregister and close the handle

`CancelRegistry` should evolve from:

- `sandbox + provider + cancellation spec`

to:

- `interactive command handle + cancellation spec`

Illustrative shape:

```go
type cancelEntry struct {
    handle    InteractiveCommandHandle
    ctxCancel context.CancelFunc
    cancel    CancellationSpec
    ...
}
```

Then `RequestStop(...)` becomes:

1. `handle.Interrupt(spec)`
2. if that fails, `ctxCancel()`
3. if the grace window expires, `handle.Kill(...)` or `ctxCancel()`

This is the correct ownership model.

## Adapter Execution Refactor

We should avoid every adapter manually managing handle startup and stream loops.

Introduce a shared helper for interactive agents, analogous to what
`runStreamingAgent(...)` does today, but handle-based.

Illustrative responsibilities:

1. accept `AgentRuntimeProfile`
2. start the interactive command handle
3. register it for cancellation
4. stream stdout/stderr into parser callbacks
5. wait for exit
6. close resources

This lets adapters keep owning:

- prompt construction
- CLI command construction
- output parsing
- agent-specific metadata shaping

while the runtime helper owns:

- transport
- lifecycle
- cancellation registration

## Migration Plan

### Phase 0: design and invariants

Before implementation, agree on:

- the interactive provider interface
- the handle lifecycle rules
- what `Interrupt(...)` and `Kill(...)` mean
- whether Docker will initially use TTY+stdin, a helper binary, or a hybrid

### Phase 1: additive provider interface

Add:

- `InteractiveCommandSpec`
- `InteractiveCommandHandle`
- `InteractiveSandboxProvider`

Do **not** remove `Exec(...)` or `ExecStream(...)`.

### Phase 2: Docker implementation

Implement `StartInteractiveCommand(...)` in Docker.

Requirements:

- supports stdout/stderr streaming
- supports ctx cancellation unblocking reads
- supports open stdin when requested
- supports TTY allocation when requested
- supports `Interrupt(...)`
- supports `Wait(...)` and `Close()`

### Phase 3: new runtime helper in agent execution path

Add a shared helper under `internal/services/agent` or
`internal/services/agent/adapters` that runs an agent turn from a live handle.

Switch `Pi` first:

- it benefits the most because `Esc` is the main reason wrappers exist today

### Phase 4: move Ctrl+C agents

Move:

- `Codex`
- `Claude Code`
- `Gemini`
- `Amp`

off wrapper-based `ExecStream(...)` and onto the new handle flow.

At this point:

- pidfiles become implementation detail only, if still needed internally
- adapter-side wrappers should disappear

### Phase 5: simplify cancellation registry

Once all interactive agents use live handles:

- remove sandbox-home pidfile/ttyfile tracking from product logic
- remove adapter-side interrupt wrappers
- shrink `CancelRegistry` to runtime-handle supervision only

### Phase 6: thread-ready execution

Use the same handle abstraction for per-thread runtime control in
multi-tab sessions.

That future work will need:

- one handle per thread
- one cancel entry per thread
- thread-scoped runtime budgets and recovery

This design is intentionally chosen to make that straightforward.

## Files and Modules Likely To Change

### Core runtime abstractions

- `internal/services/agent/adapter.go`
  - add interactive runtime interfaces and request/handle types

### Provider implementation

- `internal/services/agent/providers/docker.go`
  - implement `StartInteractiveCommand(...)`
  - add a Docker-backed handle type
  - move interruption semantics into the handle/provider

### Cancellation and orchestration

- `internal/services/agent/cancel.go`
  - register handles instead of reconstructing process control from sandbox state
- `internal/services/agent/cancellation.go`
  - keep semantic interruption modeling
  - remove filesystem-side assumptions from the public control path
- `internal/services/agent/orchestrator.go`
  - register/deregister live handles

### Adapter execution

- `internal/services/agent/adapters/stream_parser.go`
  - likely split “runtime transport” from “line parsing”
- `internal/services/agent/adapters/*.go`
  - remove wrapper usage
  - declare runtime profile

### Tests

- `internal/services/agent/providers/docker_test.go`
  - add contract tests for handle lifecycle and interrupts
- `internal/services/agent/cancel_test.go`
  - move toward handle-based cancellation tests
- adapter tests
  - switch from wrapper-string assertions to runtime-profile / transport tests

## Testing Strategy

We should add tests at three layers.

### 1. Provider contract tests

For Docker:

- start command and read stdout
- start command with stderr output and preserve diagnostics when possible
- start TTY command and write stdin
- deliver `Esc` and verify command exits
- deliver `Ctrl+C` and verify graceful stop
- cancel ctx and verify blocked reads unblock

### 2. Cancel registry tests

- register handle, interrupt, and verify `Interrupt(...)` is called
- interrupt failure falls back correctly
- grace timeout escalates to force-stop / ctx cancel

### 3. Adapter integration tests

- `Pi` non-zero exit still surfaces actionable failure text
- Ctrl+C agents still preserve their existing streaming and summary behavior
- no adapter asserts wrapper-specific shell fragments anymore

## Recommendation

Yes: the most sensible long-term direction is the **live command handle**
approach.

In practical terms, that means:

1. do **not** keep polishing wrapper scripts
2. do **not** overload `ExecStream(...)` with hidden interactive state
3. add a separate provider capability for interactive agent turns
4. migrate cancellation to the returned handle
5. treat any remaining helper process as a provider-internal detail, not an
   adapter concern

That is the cleanest architecture, the best fit for future multi-thread agent
execution, and the only path that makes `Esc`/`Ctrl+C` semantics feel native
instead of accidental.
