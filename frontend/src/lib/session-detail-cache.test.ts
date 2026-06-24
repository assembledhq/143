import { describe, expect, it } from "vitest";

import {
  isProvisionalSessionDetail,
  markProvisionalSessionDetail,
  provisionalSessionDetailFromListItem,
} from "./session-detail-cache";
import type { Session, SessionDetail } from "./types";

describe("session detail cache helpers", () => {
  it("marks a session detail as provisional without changing its fields", () => {
    const session = { id: "session-1", title: "Fix bug" } as SessionDetail;

    const marked = markProvisionalSessionDetail(session);

    expect(marked).toMatchObject(session);
    expect(isProvisionalSessionDetail(marked)).toBe(true);
    expect(isProvisionalSessionDetail(session)).toBe(false);
  });

  it("treats nullish session details as non-provisional", () => {
    expect(isProvisionalSessionDetail(undefined)).toBe(false);
    expect(isProvisionalSessionDetail(null)).toBe(false);
  });

  it("creates a provisional detail response from a list item with existing threads", () => {
    const session = {
      id: "session-1",
      title: "Fix bug",
      threads: [{ id: "thread-1" }],
    } as unknown as Session;

    const response = provisionalSessionDetailFromListItem(session);

    expect(response.data).toMatchObject({
      id: "session-1",
      title: "Fix bug",
      threads: [{ id: "thread-1" }],
    });
    expect(isProvisionalSessionDetail(response.data)).toBe(true);
  });

  it("defaults missing threads to an empty array", () => {
    const session = { id: "session-1", title: "Fix bug" } as Session;

    expect(provisionalSessionDetailFromListItem(session).data.threads).toEqual([]);
  });
});
