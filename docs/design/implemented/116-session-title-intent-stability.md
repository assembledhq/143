# Session Title Intent Stability

> **Status:** Implemented

Session titles identify the primary intention of a coding session. They are not
a status display for the work performed most recently.

## Policy

- Initial titles are generated from the original task description.
- Generated titles are reconsidered only after every tenth completed primary-
  thread turn (turns 10, 20, 30, and so on).
- The original user request remains authoritative unless a later user-authored
  primary-thread instruction clearly replaces or substantially redefines the
  primary deliverable.
- Tests, CI repair, review feedback, documentation, refactoring, investigation,
  publishing, and other implementation phases do not constitute a pivot.
- Assistant messages, system-generated messages, and secondary-thread messages
  are excluded from pivot detection.
- Ambiguous or malformed classifier output keeps the existing title.
- Manual and issue-derived titles are sticky and are never changed by the
  background pivot detector.

## Title provenance

`sessions.title_source` records one of:

- `legacy`: a title created before provenance tracking; protected from automatic updates.
- `generated`: initially generated or updated after an explicit pivot.
- `issue`: derived from a linked external issue.
- `manual`: explicitly renamed by a user or agent command.

The generic session title update path records `manual`. System-owned writers
must choose their source explicitly. Title and provenance are updated atomically
and scoped by `org_id`.

Accepted automatic pivots also persist `title_intent` and
`title_pivoted_at_turn`. Later checks use that accepted objective as their
baseline and consider only instructions after the accepted pivot. If a pivot
produces the same display title, the intent state still advances without
changing `last_activity_at`.

## Detection flow

On an eligible primary-thread turn, the title service loads the current title
state and a bounded session transcript. It constructs classifier context from
the original user request and recent, human-authored messages belonging to the
primary thread. The classifier returns either `KEEP` or `PIVOT` followed by one
single-line replacement objective.

Only a valid `PIVOT` result triggers title generation. Invalid output fails
closed. LLM and persistence errors are non-fatal to session execution and are
reported through the worker logger.

The service reads title state through a narrow store query and loads a bounded
title context directly from Postgres rather than materializing the session's
full transcript. Provenance remains outside the public session response.

Users and API clients can explicitly recompute a title from the original
primary-thread request with `POST /api/v1/sessions/{id}/title/regenerate`. This
explicit action may replace a protected manual or issue-derived title.

Decision outcomes are emitted as bounded-cardinality OpenTelemetry metrics and
structured logs. A manual rename within 24 hours of generated-title activity is
recorded as a regret signal for false-positive monitoring.
