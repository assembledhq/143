# Plan: <title>

## Context
<!-- Background on what we're building and why. 2-3 sentences max. -->


## References
<!-- Key files and docs that agents should read before starting. -->
- `docs/design/XX-relevant-doc.md` — <why it's relevant>
- `AGENTS.md` — Project conventions and patterns

## Tasks

### Task 1: <imperative verb + what to do>
- **Agent**: claude-code
- **Domain**: backend
- **Files**: `internal/services/foo.go`, `internal/db/foo.go`
- **Depends on**: (none)
- **Acceptance criteria**:
  - [ ] Unit tests pass (`make test`)
  - [ ] No new lint errors (`make lint`)
  - [ ] <specific behavioral criteria>
- **Notes**: <approach hints, constraints, patterns to follow>

### Task 2: <imperative verb + what to do>
- **Agent**: codex
- **Domain**: frontend
- **Files**: `frontend/src/components/Bar.tsx`, `frontend/src/hooks/useBar.ts`
- **Depends on**: Task 1
- **Acceptance criteria**:
  - [ ] TypeScript compiles (`npm run typecheck`)
  - [ ] Lint passes (`npm run lint`)
  - [ ] Build succeeds (`npm run build`)
  - [ ] <specific behavioral criteria>
- **Notes**: <approach hints, constraints, patterns to follow>

## Constraints
<!-- Global constraints that apply to all tasks -->
- All changes must pass CI
- Follow patterns in AGENTS.md
- Do not modify the database schema without a migration

## Out of Scope
<!-- Things agents should NOT do, even if they seem related -->
-
