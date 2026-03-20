# Design Documents

## Implementation Status Standard

Every design document must include a status block immediately after the title heading. Use this exact format:

```markdown
# Design: <Title>

> **Status:** <STATUS> | **Last reviewed:** <YYYY-MM-DD>
```

### Valid statuses

| Status | Meaning |
|--------|---------|
| `Implemented` | Feature is fully built and live. Design doc serves as historical reference. |
| `Partially Implemented` | Core parts are built but significant gaps remain. Doc should note what's done vs outstanding. |
| `Not Started` | Design is approved but no implementation exists yet. These docs live in `docs/design/future/`. |

### Rules

- When you finish implementing a feature described by a design doc, update its status to `Implemented` and set the review date.
- When you begin work on a feature, move it from `future/` back to `docs/design/` and set status to `Partially Implemented`.
- When creating a new design doc, start with status `Not Started` and place it in `docs/design/future/`.
- The `Last reviewed` date should reflect when someone last verified the status is accurate.

## Directory layout

```
docs/design/
├── AGENTS.md              # This file — conventions for design docs
├── overall.md             # High-level system overview
├── 01-database-schema.md  # Implemented designs
├── 02-api-server.md
├── ...
└── future/                # Not-yet-started designs
    ├── 14-codebase-context.md
    └── ...
```
