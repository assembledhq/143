# 57 — Coding Agent Settings UI (C2)

## Summary

The coding-agent settings page should move to a single direction:

- keep the main page summary-first
- keep the full coding-agent catalog visible
- treat subscriptions and API keys as separate credential sources that can coexist
- use a compact table when data exists
- use a strong empty state when no credentials exist yet
- move detailed add/remove/reconnect/edit flows into modals

This avoids turning the page into a large form while still making it obvious that 143 supports multiple coding agents and multiple credential sources per agent.

## Product Model

For a selected agent, the UI should represent credentials like this:

- an agent can have zero or more subscriptions
- an agent can also have an API key configured
- both may exist at the same time
- the page must show routing precedence explicitly

Example:

- `Claude Code`: 3 subscriptions + Anthropic API key fallback
- `Codex`: 2 ChatGPT subscriptions + OpenAI API key fallback
- `Gemini CLI`: Google API key only

The UI should never imply that the user must choose exactly one "credential mode."

## Main Page Structure

The main page should have three visible layers.

### 1. Agent catalog

Purpose:

- show the breadth of the product immediately
- let the user switch focus between supported coding agents

Content:

- cards or chips for `Codex`, `Claude Code`, `Gemini CLI`, `Amp`, and `Pi`
- short provider/support line under the row
- selected state is obvious

Wireframe:

```text
┌────────────────────────────────────────────────────────────┐
│ Available coding agents                                   │
│ [Codex] [Claude Code] [Gemini CLI] [Amp] [Pi]             │
│ OpenAI, Anthropic, Google, Sourcegraph, multi-provider    │
└────────────────────────────────────────────────────────────┘
```

### 2. Selected-agent summary

Purpose:

- show what is configured for the currently selected agent
- summarize health and routing without exposing all forms inline

Content:

- selected agent name
- configured credential sources
- routing order
- health summary
- top-level actions

Wireframe:

```text
┌────────────────────────────────────────────────────────────┐
│ Selected agent: Claude Code                               │
│ Credential sources: subscriptions + API key               │
│ Execution route: subscriptions first, API key fallback    │
│ Health: 2 active · 1 needs attention                      │
│                                                            │
│ [Manage subscriptions] [Manage API key]                   │
└────────────────────────────────────────────────────────────┘
```

### 3. Credential-source panel

Purpose:

- show either a compact data view or an onboarding empty state

Behavior:

- if subscriptions exist: show table summary
- if none exist: show empty state
- API key state is always shown as a summary row, not a large inline form

## Empty State

The empty state should be the onboarding surface for the selected agent.

Requirements:

- explain what the credential source enables
- make the recommended next action obvious
- show that API key fallback is optional, not mutually exclusive

Wireframe:

```text
┌────────────────────────────────────────────────────────────┐
│ No Claude Code subscriptions connected yet                │
│ Use Claude subscriptions for Anthropic-backed runs.       │
│ Labels are generated for you automatically.               │
│ Optional: add an API key too as a fallback source.        │
│                                                            │
│ [Add Claude subscription]   [Add API key fallback]        │
└────────────────────────────────────────────────────────────┘
```

## Table State

When subscriptions exist, the page should show a compact read-only table instead of stacked interactive rows.

Recommended columns:

- `Name`
- `Status`
- `Type`
- `Last used`
- `Added by` or `Created`

The table on the main page is a summary, not the full management surface.

Wireframe:

```text
┌────────────────────────────────────────────────────────────┐
│ Claude subscriptions                                      │
│                                                            │
│ Name            Status            Type         Last used   │
│ Alice Smith     Active            Max          2h ago      │
│ Alice Smith 2   Active            Max          5h ago      │
│ Bob Team        Needs attention   Pro          yesterday   │
│                                                            │
│ API key fallback: Configured                              │
│                                                            │
│ [Manage subscriptions] [Manage API key]                   │
└────────────────────────────────────────────────────────────┘
```

## Modal Flows

The main page should not contain the full editing surface. Use focused modals.

### Add credential modal

Purpose:

- start from agent choice when launched globally
- start directly from the selected agent when launched contextually

Wireframe:

```text
┌──────────────────────────────────────────────────────┐
│ Add coding agent credential                          │
│                                                      │
│ [Codex]        ChatGPT subscriptions + OpenAI key    │
│ [Claude Code]  Claude subscriptions + Anthropic key  │
│ [Gemini CLI]   Google Gemini API key                 │
│ [Amp]          Sourcegraph Amp                       │
│ [Pi]           Multi-provider routing                │
│                                                      │
│                         [Cancel]                     │
└──────────────────────────────────────────────────────┘
```

### Manage subscriptions modal

Purpose:

- full inventory management for the selected agent
- add, retry, remove, and later reorder if needed

Wireframe:

```text
┌──────────────────────────────────────────────────────┐
│ Manage Claude subscriptions                          │
│                                                      │
│ Name            Status            Type      Actions  │
│ Alice Smith     Active            Max       Remove   │
│ Alice Smith 2   Active            Max       Remove   │
│ Bob Team        Needs attention   Pro       Retry    │
│                                                      │
│ [Add subscription]                                   │
│                                                      │
│                          [Close]                     │
└──────────────────────────────────────────────────────┘
```

### Manage API key modal

Purpose:

- manage the fallback key independently from subscriptions

Wireframe:

```text
┌──────────────────────────────────────────────────────┐
│ Claude API key fallback                              │
│                                                      │
│ Status: Configured                                   │
│ Current key: sk-ant-...xyz                           │
│                                                      │
│ [Replace key]                                        │
│ [Remove key]                                         │
│                                                      │
│                          [Close]                     │
└──────────────────────────────────────────────────────┘
```

## UX Rules

The page should follow these rules:

- do not show large inline forms by default
- do not use mutually-exclusive language like `credential mode`
- use `credential sources` or `available credentials`
- always show routing precedence if more than one source exists
- keep the agent catalog visible on the main page
- use empty states for onboarding, not hidden blank tables
- keep destructive actions inside management modals

Copy examples:

- good: `Credential sources: subscriptions + API key`
- good: `Execution route: subscriptions first, API key fallback`
- bad: `Credential mode: subscription`

## Implementation Plan

### Frontend structure

Refactor the current settings card into these components:

- `AgentCatalog`
  - renders all supported coding agents
  - controls selected agent in local UI state
- `AgentCredentialSummaryCard`
  - renders selected-agent summary
  - shows routing precedence and health
- `AgentCredentialEmptyState`
  - shown when selected agent has no subscriptions
- `AgentSubscriptionSummaryTable`
  - read-only compact table
- `ManageSubscriptionsModal`
  - full subscription inventory and actions
- `ManageAgentAPIKeyModal`
  - add/replace/remove fallback API key
- optional `AddAgentCredentialModal`
  - global entry point if the page needs a single “Add credential” CTA

### State model

For each selected agent, derive:

- `subscriptions`
- `activeSubscriptionCount`
- `needsAttentionCount`
- `hasAPIKey`
- `credentialSources`
- `executionRoute`

Example derived values:

- `credentialSources = ["subscriptions", "api_key"]`
- `executionRoute = "subscriptions_first_with_api_key_fallback"`

The UI should compute these from existing backend responses instead of introducing a new persistence model first.

### Data mapping

The current page already has most of the required inputs:

- selected default agent from org settings
- subscription lists for Codex and Claude
- API key presence from `agent_config`
- status values like `active`, `pending_auth`, `invalid`

Use those to build a display model per agent:

```text
AgentDisplayState {
  agentKey
  label
  supportsSubscriptions
  supportsAPIKey
  subscriptionSummary
  apiKeySummary
  executionRouteLabel
  healthLabel
}
```

### Modal behavior

`ManageSubscriptionsModal`

- opens scoped to the selected agent
- owns add/retry/remove flows
- can reuse the existing Codex and Claude auth modals internally
- should refresh subscriptions on success and close nested auth on completion

`ManageAgentAPIKeyModal`

- opens scoped to the selected agent
- reuses existing sensitive-key save behavior
- should not expose the full plaintext existing key

### Recommended rollout

#### Phase 1

- keep existing backend behavior
- replace inline subscription controls with summary + empty state + modal shell
- keep current auth flows underneath

#### Phase 2

- convert inline Codex/Claude management into full modal inventory management
- move API key editing out of the page body into its own modal

#### Phase 3

- if needed, add ordering or routing controls
- only do this if real usage shows the default routing summary is insufficient

## Recommendation

Implement this C2 design.

Why:

- it removes the current wall-of-forms problem
- it keeps the product’s multi-agent value proposition visible
- it correctly represents multiple credential sources per agent
- it keeps advanced management flows available without making them the default page experience
