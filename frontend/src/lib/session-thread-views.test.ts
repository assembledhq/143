import { beforeEach, describe, expect, it } from "vitest";

import { readStoredViewedThreadIds, writeStoredViewedThreadIds } from "./session-thread-views";

describe("session-thread-views", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("returns an empty set when no stored value exists", () => {
    const storage = window.localStorage;

    const viewedThreadIds = readStoredViewedThreadIds(storage, "session-1");

    expect([...viewedThreadIds]).toEqual([]);
  });

  it("round-trips viewed thread ids for a session", () => {
    const storage = window.localStorage;

    writeStoredViewedThreadIds(storage, "session-1", ["thread-2", "thread-1", "thread-2"]);

    expect(storage.getItem("session-viewed-threads:session-1")).toBe(JSON.stringify(["thread-1", "thread-2"]));
    expect([...readStoredViewedThreadIds(storage, "session-1")]).toEqual(["thread-1", "thread-2"]);
  });

  it("ignores malformed stored data", () => {
    const storage = window.localStorage;
    storage.setItem("session-viewed-threads:session-1", JSON.stringify({ thread: "thread-1" }));

    const viewedThreadIds = readStoredViewedThreadIds(storage, "session-1");

    expect([...viewedThreadIds]).toEqual([]);
  });
});
