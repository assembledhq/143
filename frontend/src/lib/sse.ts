import type {
  SessionDetail,
  SessionLog,
  SessionWorkspaceGenerationChangedEvent,
  ThreadInboxEvent,
  ThreadRuntimeEvent,
} from "./types";
import { normalizeAPIResponse } from "./api-normalize";
import { captureError } from "./errors";

// EventSource cannot send custom headers like X-Active-Org-ID. The backend
// auth middleware then resolves the active org from the session's
// last_org_id hint, which deliberately is NOT updated when the client uses
// X-Active-Org-ID (so two tabs on different orgs don't trample each other).
// For multi-org users that hint commonly differs from the org they're
// actively viewing, so we have to pass the active org via query string and
// let the backend membership-check it. Used by all SSE endpoints.
export function buildSessionLogsStreamURL(
  apiBase: string,
  sessionId: string,
  activeOrgId: string | null,
  lastEventId?: string,
): string {
  const searchParams = new URLSearchParams();
  if (activeOrgId) {
    searchParams.set("org_id", activeOrgId);
  }
  if (lastEventId) {
    searchParams.set("last_event_id", lastEventId);
  }
  const qs = searchParams.toString();
  return `${apiBase}/api/v1/sessions/${sessionId}/logs/stream${qs ? `?${qs}` : ""}`;
}

/** Full-jitter reconnects that never give up on an active resource stream. */
export function resourceSSEReconnectDelay(attempt: number, random = Math.random): number {
  if (attempt >= 5) {
    return 30_000 + Math.floor(random() * 90_001);
  }
  const cap = Math.min(15_000, 1_000 * 2 ** Math.max(0, attempt));
  return Math.floor(random() * (cap + 1));
}

// Same X-Active-Org-ID workaround as buildSessionLogsStreamURL — see comment
// above for the underlying reason.

// Org-scoped SSE that wakes whenever a code review row is created or changes
// status/decision. Replaces the manual "Refresh" button on the code reviews
// page. Same X-Active-Org-ID workaround as buildSessionLogsStreamURL.

// Per-batch SSE that wakes whenever an eval batch or one of its runs flips
// state. Replaces the prior 5s React Query poll on /evals/batch/{id}.

// Per-bootstrap-run SSE that wakes whenever a bootstrap run flips state.
// Replaces the prior 3s React Query poll on /evals/bootstrap/candidates.

/**
 * SSE event types emitted by the session log stream.
 * Must stay in sync with the backend sse.EventType constants.
 */
export const SSE_EVENT = {
  /** Default (unnamed) event carrying a SessionLog entry. */
  LOG: "message",
  /** Sent when the session status changes, carries a Session object. */
  STATUS: "status",
  /** Sent when the session reaches a terminal status, carries a Session object. */
  DONE: "done",
  /** Sent when an agent creates a durable human-input request. */
  HUMAN_INPUT_CREATED: "session_human_input.created",
  /** Sent when a durable human-input request is answered or cancelled. */
  HUMAN_INPUT_UPDATED: "session_human_input.updated",
  /** Sent when a pull request health snapshot is updated. */
  /** Sent when a code review row is created or changes status/decision. */
  /** Sent when an eval batch or one of its runs changes state. */
  /** Sent when an eval bootstrap run changes state. */
  /** Sent when a thread has queued inbox input waiting for runtime delivery. */
  THREAD_INBOX_QUEUED: "thread.inbox.queued",
  /** Sent when a thread drains queued inbox input. */
  THREAD_INBOX_CLEARED: "thread.inbox.cleared",
  /** Sent when a thread runtime-visible state changes. */
  THREAD_RUNTIME_UPDATED: "thread.runtime.updated",
  /** Sent when a session workspace generation advances. */
  SESSION_WORKSPACE_GENERATION_CHANGED: "session.workspace.generation_changed",
} as const;

export type SSEEventType = (typeof SSE_EVENT)[keyof typeof SSE_EVENT];

/** Typed payloads for each SSE event type. */
export interface SSEEventPayloads {
  [SSE_EVENT.LOG]: SessionLog;
  [SSE_EVENT.STATUS]: SessionDetail;
  [SSE_EVENT.DONE]: SessionDetail;
  [SSE_EVENT.HUMAN_INPUT_CREATED]: SessionLog;
  [SSE_EVENT.HUMAN_INPUT_UPDATED]: SessionLog;
  [SSE_EVENT.THREAD_INBOX_QUEUED]: ThreadInboxEvent;
  [SSE_EVENT.THREAD_INBOX_CLEARED]: ThreadInboxEvent;
  [SSE_EVENT.THREAD_RUNTIME_UPDATED]: ThreadRuntimeEvent;
  [SSE_EVENT.SESSION_WORKSPACE_GENERATION_CHANGED]: SessionWorkspaceGenerationChangedEvent;
}

/**
 * Type-safe event listener adder for session SSE streams.
 *
 * `onCursor` is invoked on receipt of the frame, before parsing/handling, so a
 * corrupt payload still advances the resume cursor past an undeliverable event
 * rather than reconnecting into an infinite replay of it.
 */
export function addSSEListener<K extends keyof SSEEventPayloads>(
  source: EventSource,
  event: K,
  handler: (data: SSEEventPayloads[K]) => void,
  onCursor?: (lastEventId: string) => void,
): void {
  if (event === SSE_EVENT.LOG) {
    source.onmessage = (e: MessageEvent) => {
      try {
        if (e.lastEventId) onCursor?.(e.lastEventId);
        handler(normalizeAPIResponse(JSON.parse(e.data)) as SSEEventPayloads[K]);
      } catch (err) {
        captureError(err, { feature: "sse" });
      }
    };
  } else {
    source.addEventListener(event, ((e: MessageEvent) => {
      try {
        if (e.lastEventId) onCursor?.(e.lastEventId);
        handler(normalizeAPIResponse(JSON.parse(e.data)) as SSEEventPayloads[K]);
      } catch (err) {
        captureError(err, { feature: "sse" });
      }
    }) as EventListener);
  }
}
