# Session Transcript Log Windowing

> **Status:** Implemented | **Last reviewed:** 2026-06-10

Long session detail pages must avoid transferring or rendering the entire conversation when the user opens the page, especially on mobile browsers with tighter memory budgets.

Thread-scoped session detail opens from a bounded message window API. The default first window is `position=latest`, but saved reading positions use `position=around&anchor_message_id=...` so long sessions can reopen directly around the saved message instead of paging backward from the live edge. Anchored windows carry both older and newer cursors: scrolling upward fetches `before=<oldest_loaded_id>`, scrolling downward fetches `after=<newest_loaded_id>`, and `Jump to latest` bypasses stepwise newer pagination with a fresh latest-window request.

The frontend derives the loaded turn numbers from the currently loaded message range and requests `/threads/{tid}/logs?turn_numbers=...` so the backend returns only log rows that can render alongside those messages. When older or newer messages are added to the loaded range, the log query key changes to include the expanded loaded-turn set and fetches the matching logs.

The backend preserves the existing session/thread/org validation before querying logs, then applies the `turn_number = ANY(...)` filter in the store. An empty turn filter intentionally falls back to the full thread log query so legacy sessions with logs but no persisted messages remain inspectable.

Regression coverage:

- API client test verifies thread-log requests send deduplicated, sorted loaded turn numbers.
- Session thread handler/service/store tests verify the normalized turn filter is propagated and applied under org scope.
- Session thread handler/store tests verify latest, older, newer, and anchor-centered message windows remain org/thread scoped.
