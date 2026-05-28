import { describe, expect, it } from "vitest";
import { normalizeAPIResponse } from "./api-normalize";

describe("normalizeAPIResponse", () => {
  it("normalizes session log timestamp aliases throughout API payloads", () => {
    const payload = {
      data: [
        {
          id: 1,
          session_id: "session-1",
          level: "info",
          message: "working",
          metadata: null,
          turn_number: 1,
          timestamp: "2026-01-01T00:00:01Z",
        },
      ],
      meta: {},
    };

    expect(normalizeAPIResponse(payload)).toEqual({
      data: [
        {
          ...payload.data[0],
          created_at: "2026-01-01T00:00:01Z",
        },
      ],
      meta: {},
    });
  });

  it("fills nested session timeline timestamps from the parent entry", () => {
    const payload = {
      data: [
        {
          kind: "message",
          created_at: "2026-01-01T00:00:01Z",
          message: {
            id: 1,
            session_id: "session-1",
            org_id: "org-1",
            turn_number: 1,
            role: "user",
            content: "hello",
          },
        },
        {
          kind: "tool_group",
          created_at: "2026-01-01T00:00:02Z",
          tool_use: {
            id: 2,
            session_id: "session-1",
            level: "tool_use",
            message: "using tool",
            metadata: null,
            turn_number: 1,
          },
          tool_result: {
            id: 3,
            session_id: "session-1",
            level: "output",
            message: "result",
            metadata: { type: "tool_result" },
            turn_number: 1,
          },
        },
      ],
      meta: {},
    };

    const normalized = normalizeAPIResponse(payload) as {
      data: Array<{
        message?: { created_at?: string };
        tool_use?: { created_at?: string };
        tool_result?: { created_at?: string };
      }>;
    };

    expect(normalized.data[0]?.message?.created_at).toBe("2026-01-01T00:00:01Z");
    expect(normalized.data[1]?.tool_use?.created_at).toBe("2026-01-01T00:00:02Z");
    expect(normalized.data[1]?.tool_result?.created_at).toBe("2026-01-01T00:00:02Z");
  });

  it("does not map unrelated timestamp fields to created_at", () => {
    const payload = {
      data: {
        id: "event-1",
        timestamp: "2026-01-01T00:00:01Z",
        message: "not a session log",
      },
    };

    expect(normalizeAPIResponse(payload)).toEqual(payload);
  });
});
