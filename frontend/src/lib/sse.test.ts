import { describe, expect, it, vi } from "vitest";
import { addSSEListener, SSE_EVENT } from "./sse";

/** Minimal mock that captures handlers like a real EventSource. */
function createMockEventSource() {
  const listeners: Record<string, EventListener> = {};
  return {
    onmessage: null as ((e: MessageEvent) => void) | null,
    addEventListener(event: string, handler: EventListener) {
      listeners[event] = handler;
    },
    _fire(event: string, data: string) {
      const msg = new MessageEvent(event, { data });
      if (event === "message" && this.onmessage) {
        this.onmessage(msg);
      } else if (listeners[event]) {
        listeners[event](msg);
      }
    },
  };
}

describe("addSSEListener", () => {
  it("handles LOG (message) events by setting onmessage", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.LOG, handler);

    const payload = { id: 1, session_id: "s1", level: "info", message: "hi", metadata: null, turn_number: 1, created_at: "2026-01-01T00:00:00Z" };
    source._fire("message", JSON.stringify(payload));

    expect(handler).toHaveBeenCalledWith(payload);
  });

  it("handles STATUS events via addEventListener", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.STATUS, handler);

    const payload = { id: "s1", status: "running" };
    source._fire("status", JSON.stringify(payload));

    expect(handler).toHaveBeenCalledWith(payload);
  });

  it("handles DONE events via addEventListener", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.DONE, handler);

    const payload = { id: "s1", status: "completed" };
    source._fire("done", JSON.stringify(payload));

    expect(handler).toHaveBeenCalledWith(payload);
  });

  it("handles human input events via addEventListener", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.HUMAN_INPUT_UPDATED, handler);

    const payload = { id: 12, session_id: "s1", level: "human_input", message: "answered", metadata: { status: "answered" }, turn_number: 1, created_at: "2026-01-01T00:00:00Z" };
    source._fire("session_human_input.updated", JSON.stringify(payload));

    expect(handler).toHaveBeenCalledWith(payload);
  });

  it("ignores unparseable JSON for message events", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.LOG, handler);
    source._fire("message", "not-json");

    expect(handler).not.toHaveBeenCalled();
  });

  it("ignores unparseable JSON for named events", () => {
    const source = createMockEventSource();
    const handler = vi.fn();

    addSSEListener(source as unknown as EventSource, SSE_EVENT.STATUS, handler);
    source._fire("status", "{bad json");

    expect(handler).not.toHaveBeenCalled();
  });
});

describe("SSE_EVENT constants", () => {
  it("has correct event names", () => {
    expect(SSE_EVENT.LOG).toBe("message");
    expect(SSE_EVENT.STATUS).toBe("status");
    expect(SSE_EVENT.DONE).toBe("done");
    expect(SSE_EVENT.HUMAN_INPUT_CREATED).toBe("session_human_input.created");
    expect(SSE_EVENT.HUMAN_INPUT_UPDATED).toBe("session_human_input.updated");
    expect(SSE_EVENT.PULL_REQUEST_UPDATED).toBe("pull_request.updated");
    expect(SSE_EVENT.THREAD_INBOX_QUEUED).toBe("thread.inbox.queued");
    expect(SSE_EVENT.THREAD_INBOX_CLEARED).toBe("thread.inbox.cleared");
    expect(SSE_EVENT.THREAD_RUNTIME_UPDATED).toBe("thread.runtime.updated");
    expect(SSE_EVENT.SESSION_WORKSPACE_GENERATION_CHANGED).toBe("session.workspace.generation_changed");
  });
});
