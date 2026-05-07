# Design Documents

## Implementation Status Standard

Every design document must include a status block immediately after the title heading. Use this exact format:

```markdown
# Design: <Title>

> **Status:** <STATUS> | **Last reviewed:** <YYYY-MM-DD>
```

### Valid statuses

| Status | Meaning | Directory |
|--------|---------|-----------|
| `Implemented` | Feature is fully built and live. Design doc serves as historical reference. | `implemented/` |
| `Partially Implemented` | Core parts are built and active work is ongoing. Doc should note what's done vs outstanding. | top level |
| `Backlog` | Partially built but no active work expected for a while. The shipped portion is in production; the gaps are parked. Doc should note what's done vs outstanding. | `backlog/` |
| `Not Started` | Design is approved but no implementation exists yet. | `future/` |

The top level is reserved for **living architecture overviews** (`overall.md`, `03-frontend.md`, etc.) and a small number of features under active iteration. If a doc has been `Partially Implemented` for a while with no active work, move it to `backlog/`.

### Rules

- When you finish implementing a feature described by a design doc, update its status to `Implemented` and move the file into `implemented/`.
- When you begin work on a feature, move it from `future/` (or `backlog/`) to the top level and set status to `Partially Implemented`.
- When work on a `Partially Implemented` doc has been paused for more than ~a month, move it to `backlog/` and set status to `Backlog`.
- When creating a new design doc, start with status `Not Started` and place it in `future/`.
- The `Last reviewed` date should reflect when someone last verified the status is accurate. Update it whenever you change the status.
