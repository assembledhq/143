"use client";

import { useEffect, useState } from "react";
import { addSSEListener, type SSEEventPayloads } from "./sse";
import { pollMs } from "./poll-intervals";

// Connect cadence shared across SSE consumers (eval batch/bootstrap pages, the
// code reviews list, …). Capped exponential backoff (1s → 2s → 4s → 8s → 15s
// ceiling) so a transient Redis outage doesn't burn into the page lifecycle,
// but a sustained outage hands off to the polling backstop in the calling
// component without retrying forever.
const SSE_INITIAL_RECONNECT_DELAY_MS = pollMs(1_000);
const SSE_MAX_RECONNECT_DELAY_MS = 15_000;
const SSE_MAX_RECONNECT_ATTEMPTS = 5;

export interface UseResourceSSEOptions<K extends keyof SSEEventPayloads> {
  /**
   * Fully-qualified SSE URL (typically built by one of the build*StreamURL
   * helpers). Pass null when there's nothing to subscribe to (e.g. no active
   * bootstrap run); the hook stays idle and the previous connection is torn
   * down on transition.
   */
  url: string | null;
  /** SSE event name to listen for, e.g. SSE_EVENT.CODE_REVIEW_UPDATED. */
  event: K;
  /**
   * Fires on every received event. Typically just a queryClient.invalidate.
   * Don't read fields off the payload to drive UI state — Redis pub/sub is
   * at-most-once and unordered, so the canonical record fetched on
   * invalidation is the source of truth.
   */
  onEvent: (payload: SSEEventPayloads[K]) => void;
}

export interface UseResourceSSEResult {
  /**
   * True after EventSource.onopen fires; flips to false on any error so
   * callers can drive a faster polling backstop while Redis recovers.
   * Never resets to true on its own when there's no URL — there's nothing
   * to be healthy or unhealthy about, so the previous value sticks until
   * the next subscription opens.
   */
  healthy: boolean;
}

/**
 * useResourceSSE wires an EventSource for a single org/resource-scoped SSE
 * channel with capped exponential reconnect and stream-health tracking.
 * Shared by the eval batch/bootstrap pages and the code reviews list — see
 * settings/evals/batch/[id]/page.tsx, settings/evals/page.tsx, and
 * code-reviews/page.tsx.
 */
export function useResourceSSE<K extends keyof SSEEventPayloads>({
  url,
  event,
  onEvent,
}: UseResourceSSEOptions<K>): UseResourceSSEResult {
  const [healthy, setHealthy] = useState(true);

  useEffect(() => {
    if (!url) return;

    let eventSource: EventSource | null = null;
    let cancelled = false;
    let reconnectAttempts = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    function connect() {
      if (cancelled) return;
      eventSource = new EventSource(url!, { withCredentials: true });

      eventSource.onopen = () => {
        reconnectAttempts = 0;
        setHealthy(true);
      };

      addSSEListener(eventSource, event, onEvent);

      eventSource.onerror = () => {
        eventSource?.close();
        setHealthy(false);
        if (cancelled || reconnectAttempts >= SSE_MAX_RECONNECT_ATTEMPTS) {
          return;
        }
        const delay = Math.min(
          SSE_INITIAL_RECONNECT_DELAY_MS * 2 ** reconnectAttempts,
          SSE_MAX_RECONNECT_DELAY_MS,
        );
        reconnectAttempts += 1;
        reconnectTimer = setTimeout(connect, delay);
      };
    }

    connect();

    return () => {
      cancelled = true;
      eventSource?.close();
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
    };
  }, [url, event, onEvent]);

  return { healthy };
}
