import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useRecentPaletteItems } from "./use-recent-palette-items";

describe("useRecentPaletteItems", () => {
  it("filters saved project links from command palette recents", () => {
    const session = {
      type: "session",
      id: "session-1",
      label: "Fix login",
      href: "/sessions/session-1",
      timestamp: 2,
    };
    localStorage.setItem(
      "143:command-palette:recents",
      JSON.stringify([
        {
          type: "project",
          id: "project-1",
          label: "Retired project",
          href: "/projects/project-1",
          timestamp: 3,
        },
        session,
      ])
    );

    const { result } = renderHook(() => useRecentPaletteItems());

    expect(result.current.displayItems).toEqual([session]);
  });
});
