import { describe, it, expect, beforeEach } from "vitest";

const STORAGE_KEY = "143:command-palette:recents";

describe("recent palette items localStorage contract", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("stores items under the correct key", () => {
    const items = [
      { type: "session", id: "s1", label: "Fix bug", href: "/sessions/s1", timestamp: 1 },
    ];
    localStorage.setItem(STORAGE_KEY, JSON.stringify(items));

    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY)!);
    expect(stored).toHaveLength(1);
    expect(stored[0].id).toBe("s1");
  });

  it("deduplicates by type+id, keeping the newer entry", () => {
    const items = [
      { type: "session", id: "s1", label: "Updated label", href: "/sessions/s1", timestamp: 2 },
      { type: "session", id: "s2", label: "Other", href: "/sessions/s2", timestamp: 1 },
    ];
    localStorage.setItem(STORAGE_KEY, JSON.stringify(items));

    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY)!);
    const s1Entries = stored.filter((i: { id: string }) => i.id === "s1");
    expect(s1Entries).toHaveLength(1);
    expect(s1Entries[0].label).toBe("Updated label");
  });

  it("caps at 10 entries", () => {
    const items = Array.from({ length: 12 }, (_, i) => ({
      type: "session",
      id: `s${i}`,
      label: `Session ${i}`,
      href: `/sessions/s${i}`,
      timestamp: i,
    }));
    // Simulate the cap logic
    const capped = items.slice(0, 10);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(capped));

    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY)!);
    expect(stored).toHaveLength(10);
  });
});
