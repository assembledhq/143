# Session Transcript Log Windowing

> **Status:** Implemented | **Last reviewed:** 2026-05-23

Long session detail pages must avoid transferring or rendering the entire conversation when the user opens the page, especially on mobile browsers with tighter memory budgets.

Thread-scoped session detail opens from the existing latest message window API. The frontend derives the loaded turn numbers from those messages and requests `/threads/{tid}/logs?turn_numbers=...` so the backend returns only log rows that can render alongside the loaded messages. When the user loads older messages, the log query key changes to include the expanded loaded-turn set and fetches the matching older logs.

The backend preserves the existing session/thread/org validation before querying logs, then applies the `turn_number = ANY(...)` filter in the store. An empty turn filter intentionally falls back to the full thread log query so legacy sessions with logs but no persisted messages remain inspectable.

Regression coverage:

- API client test verifies thread-log requests send deduplicated, sorted loaded turn numbers.
- Session thread handler/service/store tests verify the normalized turn filter is propagated and applied under org scope.
