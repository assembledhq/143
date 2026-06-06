# Design: Hierarchical Agent Tools CLI

> **Status:** Implemented | **Last reviewed:** 2026-06-05

## Summary

`143-tools` should move its agent-facing integration command surface from flat
tool names to hierarchical commands. Instead of:

```bash
143-tools sentry_list_errors --severity high --limit 10
143-tools linear_get_task --task_id ENG-123
143-tools log_query --provider victorialogs --query 'level:error' --since 1h
```

agents should use:

```bash
143-tools sentry list_errors --severity high --limit 10
143-tools linear get_task --task_id ENG-123
143-tools logs query --provider victorialogs --query 'level:error' --since 1h
```

Flat integration commands should be removed immediately rather than kept as
aliases. The expected callers are coding agents in 143-managed sandboxes, and
the orchestrator controls the injected prompt text, so the migration can be
coordinated in one product release without a long compatibility period.

The main design goal is not the extra space in the command line. The goal is to
make the agent-facing context smaller and more navigable as integrations grow:
top-level help and generated prompt context should teach discovery, then agents
can ask for provider-specific help only when needed.

## Problem

The current CLI exposes each integration tool as one flat command, such as
`sentry_list_errors`, `linear_update_task`, `notion_search_documents`,
`circleci_get_recent_test_failures`, and `slack_get_thread`.

This has two scaling problems:

- The top-level command list grows linearly with every provider and action.
- The generated sandbox skills document currently lists every command with
  examples, which consumes prompt context and makes similar commands harder for
  agents to distinguish.

Grouping by prefix in help output helps humans, but it does not change the
mental model: every action is still a top-level command. As more providers,
agent workflow tools, diagnostics tools, and session tools are added, the flat
surface will become noisy.

## Goals

- Replace flat agent-facing integration commands with hierarchical commands.
- Remove flat command aliases immediately.
- Keep the underlying `ToolRegistry` dispatch and MCP compatibility stable
  unless a separate MCP migration is explicitly designed.
- Make `143-tools --help` compact enough to include in prompt context.
- Add provider/category-level help, such as `143-tools sentry --help`.
- Make generated sandbox skills docs summarize namespaces and discovery
  commands instead of enumerating every flag for every tool.
- Make CLI errors maximally helpful for LLM callers: errors should explain what
  went wrong, show the correct command shape, and point to the exact help command
  the agent should run next.
- Update public docs, prompt templates, tests, and implementation notes in the
  same migration.

## Non-Goals

- Changing provider API behavior, returned JSON shapes, or credential handling.
- Changing MCP wire tool names in this migration.
- Building provider-agnostic semantic commands such as
  `143-tools errors list --provider sentry`.
- Adding a long deprecation period for old flat names.
- Changing sandbox infrastructure helper subcommands used by git, gh, or the
  orchestrator.

## Command Model

The new agent-facing syntax is:

```bash
143-tools <namespace> <action> [--flag value ...]
143-tools <namespace> --help
143-tools <namespace> <action> --help
143-tools --help
```

Rules:

- `<namespace>` is either a configured provider name (`sentry`, `linear`,
  `notion`, `github`, `circleci`, `slack`) or a 143-owned category (`logs`,
  `issue`, `pr`, `project`).
- `<action>` is the old command suffix after the provider prefix, or a shorter
  category action for 143-owned tools.
- Flags, required fields, defaults, validation, and stdout result format remain
  unchanged.
- Array flags remain comma-separated, such as `--states triage,in_progress`.
- Boolean flags continue to accept `true` or `false`.
- Unknown flat command names fail with an error that points to the new shape.

For example:

```text
error: unknown command "sentry_list_errors"

143-tools now uses hierarchical commands. Try:
  143-tools sentry list_errors ...

Run '143-tools --help' to list namespaces.
```

Do not expose flat aliases such as `143-tools sentry_list_errors`. Tests should
assert that these old names fail.

## Command Inventory

### Error Tracking

For every configured error tracker provider, expose:

| Old command | New command |
|---|---|
| `143-tools sentry_list_errors` | `143-tools sentry list_errors` |
| `143-tools sentry_get_error` | `143-tools sentry get_error` |
| `143-tools sentry_get_error_trend` | `143-tools sentry get_error_trend` |
| `143-tools sentry_find_related_errors` | `143-tools sentry find_related_errors` |

The namespace is provider-derived, so a future `bugsnag` error tracker would
use:

```bash
143-tools bugsnag list_errors
143-tools bugsnag get_error --error_id 12345
```

### Task Managers

For every configured task manager provider, expose:

| Old command | New command |
|---|---|
| `143-tools linear_list_tasks` | `143-tools linear list_tasks` |
| `143-tools linear_get_task` | `143-tools linear get_task` |
| `143-tools linear_find_related_tasks` | `143-tools linear find_related_tasks` |
| `143-tools linear_update_task` | `143-tools linear update_task` |
| `143-tools linear_create_task` | `143-tools linear create_task` |

### Document Stores

For every configured document store provider, expose:

| Old command | New command |
|---|---|
| `143-tools notion_search_documents` | `143-tools notion search_documents` |
| `143-tools notion_get_document` | `143-tools notion get_document` |

### Code Review Sources

For every configured code review source, expose:

| Old command | New command |
|---|---|
| `143-tools github_list_recent_prs` | `143-tools github list_recent_prs` |
| `143-tools github_get_pr_reviews` | `143-tools github get_pr_reviews` |

### CI Test Insights

For every configured CI test insights provider, expose:

| Old command | New command |
|---|---|
| `143-tools circleci_list_flaky_tests` | `143-tools circleci list_flaky_tests` |
| `143-tools circleci_get_job_test_results` | `143-tools circleci get_job_test_results` |
| `143-tools circleci_get_recent_test_failures` | `143-tools circleci get_recent_test_failures` |

### Message Sources

For every configured message source, expose:

| Old command | New command |
|---|---|
| `143-tools slack_search_messages` | `143-tools slack search_messages` |
| `143-tools slack_get_thread` | `143-tools slack get_thread` |

### Logs

Logs are already provider-agnostic and use `--provider` to choose the backing
log provider. Move them under a plural `logs` namespace:

| Old command | New command |
|---|---|
| `143-tools log_query` | `143-tools logs query` |
| `143-tools log_context` | `143-tools logs context` |
| `143-tools log_fields` | `143-tools logs fields` |
| `143-tools log_stats` | `143-tools logs stats` |

Keep `--provider` as a flag because one org may have multiple log providers and
the provider is not the primary command category.

### 143 Workflow Tools

143-owned workflow tools should use short, product-owned namespaces:

| Old command | New command |
|---|---|
| `143-tools issue_create` | `143-tools issue create` |
| `143-tools create_pr` | `143-tools pr create` |
| `143-tools project_propose` | `143-tools project propose` |

Future 143-owned tools should follow the same pattern:

```bash
143-tools session list_tabs
143-tools session create_tab
143-tools session message_tab
143-tools eval run
143-tools repo inspect
```

Prefer nouns for namespaces and verbs or verb phrases for actions.

### Sandbox Infrastructure Commands

These existing commands are not agent-facing integration tools and should stay
top-level because external programs call them directly:

```bash
143-tools git-credential
143-tools auth-token --action push
143-tools git-bootstrap --workdir /path/to/repo
```

They should remain hidden from generated agent skills docs and the public
agent-tools reference unless that reference explicitly documents internal
sandbox mechanics. Their behavior should not be changed by this migration.

## Help Contract

### Top-Level Help

`143-tools --help` should list namespaces, not every action and flag.

Example:

```text
Usage:
  143-tools <namespace> <action> [--flag value ...]
  143-tools <namespace> --help
  143-tools <namespace> <action> --help

Namespaces:
  sentry      Error tracking: list_errors, get_error, get_error_trend, find_related_errors
  linear      Tasks: list_tasks, get_task, find_related_tasks, update_task, create_task
  notion      Documents: search_documents, get_document
  github      Code review: list_recent_prs, get_pr_reviews
  circleci    CI test insights: list_flaky_tests, get_job_test_results, get_recent_test_failures
  logs        Logs: query, context, fields, stats
  slack       Messages: search_messages, get_thread
  issue       143 issues: create
  pr          143 pull requests: create
  project     143 projects: propose

Run '143-tools <namespace> --help' for namespace-specific commands.
```

Only configured integrations should appear. If Slack is not configured, do not
list `slack`. If no log provider supports stats, omit `stats` from `logs`.

### Namespace Help

`143-tools sentry --help` should list actions and one-line descriptions:

```text
Usage:
  143-tools sentry <action> [--flag value ...]
  143-tools sentry <action> --help

Actions:
  list_errors           List unresolved errors from sentry.
  get_error             Get full details for a single error from sentry.
  get_error_trend       Get occurrence trend for an error from sentry.
  find_related_errors   Find errors likely to share a root cause.
```

### Action Help

`143-tools sentry list_errors --help` should show the existing detailed flag
table content in CLI-friendly text:

```text
Usage:
  143-tools sentry list_errors [flags]

List unresolved errors from sentry. Returns error summaries with severity,
occurrence counts, and affected users.

Flags:
  --limit      number              Max results to return (default: 25)
  --project    string              Project slug to filter by
  --severity   critical|high|...   Filter by severity level
  --since      string              Only errors seen after this ISO 8601 timestamp
```

The flag help should continue to be generated from `Tool.InputSchema`.

### Error Messages

Use actionable, self-correcting errors because the primary caller is an LLM.
Every CLI parsing error should give the agent enough information to repair the
next command without guessing:

- State the specific problem.
- Show the expected command shape or a likely replacement command.
- Point to the most specific help command available.
- Avoid terse errors such as `unknown command` without usage guidance.

Examples:

- Missing namespace:
  `error: missing namespace. Run '143-tools --help'.`
- Unknown namespace:
  `error: unknown namespace "foo". Run '143-tools --help'.`
- Missing action:
  `error: missing action for namespace "sentry". Run '143-tools sentry --help'.`
- Unknown action:
  `error: unknown action "foo" for namespace "sentry". Run '143-tools sentry --help'.`
- Old flat command:
  `error: "sentry_list_errors" is no longer supported. Use '143-tools sentry list_errors'.`

The old-flat-command error should use best-effort translation when the
namespace/action pair exists. If the provider is unavailable, still explain the
new syntax:

```text
error: "linear_get_task" is no longer supported. 143-tools now uses
hierarchical commands such as '143-tools linear get_task'.
```

## Implementation Plan

### 1. Add command path metadata helpers

Keep `Tool.Name` unchanged for MCP compatibility and existing dispatch:

```go
Tool{Name: "sentry_list_errors", ...}
Tool{Name: "log_query", ...}
Tool{Name: "create_pr", ...}
```

Add CLI-only helpers in `internal/services/mcp/cli.go` or a new
`cli_commands.go`:

```go
type CLICommand struct {
    Namespace   string
    Action      string
    ToolName    string
    Description string
    Schema      ToolSchema
}
```

Build `[]CLICommand` from `tr.ListTools()`:

- Provider-prefixed tools become `<provider> <suffix>`.
- `log_query`, `log_context`, `log_fields`, `log_stats` become
  `logs query`, `logs context`, `logs fields`, `logs stats`.
- `issue_create` becomes `issue create`.
- `create_pr` becomes `pr create`.
- `<projectProvider>_propose` becomes `project propose` for the current 143
  project proposer.

Do not infer command paths by blindly splitting every tool on the first
underscore. Use explicit category matching so `create_pr` does not become
`create pr` and log commands do not become `log query`.

### 2. Change `RunCLI` parsing

Update `RunCLI` so the first positional argument is namespace and the second is
action.

Behavior:

- `143-tools --help` prints top-level namespace help.
- `143-tools sentry --help` prints namespace help.
- `143-tools sentry list_errors --help` prints action help.
- `143-tools sentry list_errors --severity high` dispatches to
  `tr.CallTool(ctx, "sentry_list_errors", argsJSON)`.
- `143-tools sentry_list_errors ...` returns the explicit old-command error and
  non-zero exit.

All existing flag parsing, schema coercion, required-field checking, and stdout
printing should remain shared.

### 3. Update generated skills docs

Change `GenerateSkillsDoc` so it no longer emits every tool with a code block.
The generated prompt should be closer to:

```markdown
# Integration Tools

Use `143-tools` to query connected services.

## Quick Reference

```bash
143-tools <namespace> <action> [--flag value ...]
143-tools <namespace> --help
143-tools --help
```

Configured namespaces:

- `sentry`: errors (`list_errors`, `get_error`, `get_error_trend`, `find_related_errors`)
- `linear`: tasks (`list_tasks`, `get_task`, `find_related_tasks`, `update_task`, `create_task`)
- `logs`: production logs (`query`, `context`, `fields`, `stats`)
- `pr`: pull requests (`create`)

Examples:

```bash
143-tools sentry list_errors --severity critical --limit 20
143-tools linear get_task --task_id ENG-123
143-tools logs query --provider victorialogs --query 'service:api AND level:error' --since 1h --limit 100
143-tools pr create --draft false
```

Run `143-tools <namespace> --help` before using unfamiliar commands.
```

Keep examples bounded. Prefer one high-value example per category, not one per
action. The skills doc should remain useful even when many integrations are
configured.

Update `internal/services/mcp/skills_test.go` to assert:

- new syntax appears
- old flat names do not appear
- configured-only namespaces appear
- unconfigured namespaces do not appear
- the doc stays under the token/word budget

### 4. Update public docs

Update `docs/public/reference/agent-tools.mdx`:

- Change the CLI contract from `143-tools <tool_name>` to
  `143-tools <namespace> <action>`.
- Replace every flat command heading with hierarchical command headings.
- Update every example.
- Add a top-level "Namespaces" section that mirrors `143-tools --help`.
- Document that flat command names were removed and are not supported.
- Keep flag tables unchanged except for command names.
- Keep `143-tools --help` as the runtime source of truth.
- Keep log-provider guidance, especially bounded time filters for log queries.

Update any docs index summaries only if they quote the old command shape.

### 5. Update prompt templates and static instructions

Search for old command names and command-shape examples:

```bash
rg '143-tools [a-z0-9]+_[a-z0-9_]+|143-tools <tool_name>|sentry_|linear_|notion_|github_|circleci_|slack_|log_query|create_pr|issue_create|project_propose' internal/prompts docs cmd internal -g '!frontend/node_modules'
```

Update at least:

- `internal/prompts/templates/pm_bootstrap.template`
- `internal/prompts/templates/pm_context_refresh.template`
- `internal/prompts/templates/pm_system_prompt.template`
- `internal/services/mcp/AGENTS.md`
- `cmd/tools/AGENTS.md`
- `cmd/tools/main.go` package comment
- `docs/public/reference/agent-tools.mdx`
- Any tests that snapshot or assert generated prompt content.

The user-provided AGENTS guidance in downstream sandboxes should also be
updated when this repo generates it. After migration, new sandbox prompts should
never mention old flat integration commands.

### 6. Update tests

This is a CLI behavior change, so write failing tests before implementation.

Required Go tests:

- `RunCLI` top-level help lists namespaces, not flat command names.
- `RunCLI` namespace help works, such as `sentry --help`.
- `RunCLI` action help works, such as `sentry list_errors --help`.
- `RunCLI` dispatches hierarchical provider commands.
- `RunCLI` dispatches `logs query`.
- `RunCLI` dispatches `pr create`.
- `RunCLI` rejects old flat commands with a useful migration error.
- `RunCLI` reports missing namespace, missing action, unknown namespace, and
  unknown action clearly.
- `GenerateSkillsDoc` uses hierarchical commands and omits old flat command
  names.
- `GenerateSkillsDoc` stays compact with a registry containing many providers.

Existing tests currently use `testing` directly in some places. New and changed
tests should follow the repo's standard pattern: table-driven where useful,
`t.Parallel()`, and `require` from `stretchr/testify`.

### 7. Keep MCP stable

Do not rename `Tool.Name` values in `ToolRegistry` as part of this migration.
MCP clients can continue seeing flat tool names through `tools/list`, while
`143-tools` presents hierarchy at the CLI layer.

This avoids coupling a sandbox-agent CLI cleanup to a protocol/tooling migration
for IDE integrations. If MCP should eventually expose grouped names, write a
separate design because MCP tool names are naturally flat and clients may cache
them.

### 8. Remove old command references

After implementation, run:

```bash
rg '143-tools [a-z0-9]+_[a-z0-9_]+|143-tools <tool_name>|sentry_list_errors|linear_get_task|log_query|create_pr|issue_create|project_propose'
```

Expected remaining references:

- Migration tests that assert old commands fail.
- This design doc, as historical migration context.
- MCP-specific docs/tests if they intentionally discuss flat `Tool.Name`
  values.
- Sandbox infrastructure `auth-token`, `git-credential`, and `git-bootstrap`
  references, because those are not integration tools.

All agent-facing prompt docs and public command examples should use only
hierarchical syntax.

## Database Schema

No schema changes.

The migration changes CLI parsing, generated prompt text, docs, and tests. It
does not add tables, columns, indexes, constraints, triggers, or enum-like model
values.

## API Contract

No HTTP API changes.

`143-tools` still reads credentials and session capability environment variables
the same way. It still calls the existing integration layer and emits the same
stdout JSON result shapes. Error text changes for CLI parsing failures only.

## Rollout Plan

1. Land the implementation and prompt/docs updates together.
2. Build the sandbox image with the new `143-tools` binary.
3. Deploy the orchestrator/API changes that generate hierarchical skills docs.
4. Verify a fresh sandbox prompt contains only hierarchical commands.
5. Run smoke commands in a sandbox with representative integrations:

   ```bash
   143-tools --help
   143-tools sentry --help
   143-tools sentry list_errors --severity high --limit 5
   143-tools linear get_task --task_id ENG-123
   143-tools logs query --provider victorialogs --query 'service:api' --since 15m --limit 5
   143-tools pr create --help
   ```

6. Verify old commands fail:

   ```bash
   143-tools sentry_list_errors --limit 5
   143-tools log_query --query 'service:api' --since 15m
   143-tools create_pr --help
   ```

Because traffic is low and callers are controlled coding agents, do not run a
compatibility window. If a live run has an older prompt but a newer binary, the
old-command error should point it to the new shape.

## Verification Checklist

Backend/Go verification:

```bash
go vet ./...
go build ./...
go test ./...
```

Documentation verification:

```bash
rg '143-tools [a-z0-9]+_[a-z0-9_]+|143-tools <tool_name>' internal/prompts docs cmd internal/services/mcp
```

Manual CLI verification:

```bash
143-tools --help
143-tools <configured-provider> --help
143-tools <configured-provider> <action> --help
```

## Open Questions

- Should namespace/action names prefer shorter verbs in future, such as
  `143-tools sentry list` instead of `list_errors`? This migration keeps action
  names close to existing tool names to reduce agent confusion.
- Should `logs` eventually move from `--provider victorialogs` to
  `143-tools victorialogs logs query`? Not for this migration; log query shape
  is intentionally provider-agnostic.
- Should public docs document the sandbox infrastructure commands? Not as part
  of this migration; they are not normal agent tools.
