import type { Session, SessionLog } from "./types";
import { captureError } from "./errors";

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
} as const;

export type SSEEventType = (typeof SSE_EVENT)[keyof typeof SSE_EVENT];

/** Typed payloads for each SSE event type. */
export interface SSEEventPayloads {
  [SSE_EVENT.LOG]: SessionLog;
  [SSE_EVENT.STATUS]: Session;
  [SSE_EVENT.DONE]: Session;
}

/** Type-safe event listener adder for session SSE streams. */
export function addSSEListener<K extends keyof SSEEventPayloads>(
  source: EventSource,
  event: K,
  handler: (data: SSEEventPayloads[K]) => void,
): void {
  if (event === SSE_EVENT.LOG) {
    source.onmessage = (e: MessageEvent) => {
      try {
        handler(JSON.parse(e.data));
      } catch (err) {
        captureError(err, { feature: "sse" });
      }
    };
  } else {
    source.addEventListener(event, ((e: MessageEvent) => {
      try {
        handler(JSON.parse(e.data));
      } catch (err) {
        captureError(err, { feature: "sse" });
      }
    }) as EventListener);
  }
}
