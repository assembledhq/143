import { describe, it, expect } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useDiffViewState } from "./use-diff-view-state";
import type { Session } from "@/lib/types";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "session-1",
    issue_id: "issue-1",
    org_id: "org-1",
    agent_type: "coding",
    status: "completed",
    autonomy_level: "full",
    token_mode: "standard",
    current_turn: 1,
    sandbox_state: "ready",
    ...overrides,
  } as Session;
}

const simpleDiff = `diff --git a/src/app.ts b/src/app.ts
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,3 +1,4 @@
 const x = 1;
-const y = 2;
+const y = "updated";
+const z = 3;
 const w = 4;
`;

describe("useDiffViewState", () => {
  it("returns empty files when session has no diff", () => {
    const { result } = renderHook(() => useDiffViewState(makeSession()));
    expect(result.current.allFiles).toEqual([]);
    expect(result.current.files).toEqual([]);
    expect(result.current.filteredFiles).toEqual([]);
  });

  it("parses session diff into files", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    expect(result.current.allFiles.length).toBe(1);
    expect(result.current.allFiles[0].newPath).toBe("src/app.ts");
  });

  it("returns passes from diff_history", () => {
    const diffHistory = [
      { pass: 1, diff: simpleDiff, diff_stats: { added: 2, removed: 1, files_changed: 1 }, created_at: "2026-01-01T00:00:00Z" },
      { pass: 2, diff: simpleDiff, diff_stats: { added: 3, removed: 1, files_changed: 1 }, created_at: "2026-01-01T01:00:00Z" },
    ];
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff, diff_history: diffHistory }))
    );
    expect(result.current.passes.length).toBe(2);
  });

  it("filters files by search query on file path", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    act(() => {
      result.current.setDiffSearchQuery("app");
    });
    expect(result.current.filteredFiles.length).toBe(1);

    act(() => {
      result.current.setDiffSearchQuery("nonexistent");
    });
    expect(result.current.filteredFiles.length).toBe(0);
  });

  it("filters files by search query on line content", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    act(() => {
      result.current.setDiffSearchQuery("updated");
    });
    expect(result.current.filteredFiles.length).toBe(1);
  });

  it("returns all files when search query is empty", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    act(() => {
      result.current.setDiffSearchQuery("test");
    });
    expect(result.current.filteredFiles.length).toBe(0);

    act(() => {
      result.current.setDiffSearchQuery("");
    });
    expect(result.current.filteredFiles.length).toBe(1);
  });

  it("setPassRange updates passRange", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    expect(result.current.passRange).toBeNull();
    act(() => {
      result.current.setPassRange({ from: 1, to: 2 });
    });
    expect(result.current.passRange).toEqual({ from: 1, to: 2 });
  });

  it("returns allFiles when passRange is set but fewer than 2 passes", () => {
    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: simpleDiff }))
    );
    act(() => {
      result.current.setPassRange({ from: 0, to: 1 });
    });
    // With no passes, should fall back to allFiles
    expect(result.current.files).toEqual(result.current.allFiles);
  });
});
