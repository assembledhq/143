# 53 - Session Composer Mentions

> **Status:** Implemented | **Last reviewed:** 2026-04-22
>
> **Depends on:** [../overall.md](../overall.md), [../03-frontend.md](../03-frontend.md), [../future/53-session-composer-mentions.md](../future/53-session-composer-mentions.md)

## What shipped

Manual session creation at `/sessions/new` now supports repository-aware `@` mentions for files and directories.

The shipped design follows the core proposal:

- The textarea keeps visible `@path` tokens.
- The frontend also tracks canonical `references[]` entries for each selected mention.
- Manual session creation submits both `message` and `references`.
- The initial `session_messages` row persists those references in structured form.
- The agent orchestrator reads manual-session references from issue raw data and threads them into adapter input.
- Agent adapters append a normalized "referenced context" section so downstream agents receive durable meaning, not just raw text.

## Backend shape

- `session_messages.references` is stored as JSONB and scanned into `models.SessionInputReferences`.
- `models.SessionInputReference` supports `file`, `directory`, `app`, and `plugin` kinds so the API shape is future-proof even though v1 only resolves file-system references.
- `POST /api/v1/sessions/manual` validates reference kinds and stores them on both the initial message and manual issue raw data.
- `GET /api/v1/session-composer/files` returns ranked file/directory matches for a repository, using GitHub tree listing through the PR service and repository/org access checks.

## Frontend shape

- The manual session composer maintains `message`, `references`, and caret-driven active mention state.
- Typing `@` opens a picker anchored directly above the composer so results stay visually attached to the input, arrow keys move the selection, `Enter` inserts the mention, and selected references render as removable chips below the textarea.
- Reference state is reconciled back against the message text so deleting a token removes the structured reference.
- Changing repositories clears stale references from both chips and message text to avoid cross-repo ambiguity.

## Follow-ups intentionally left out

- App/plugin mention resolution in the picker
- symbol or line-range references
- generic binary attachment translation for all agent types
- remote MCP resource browsing from the composer
