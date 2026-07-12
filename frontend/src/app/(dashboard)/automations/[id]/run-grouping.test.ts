import { describe, expect, it } from "vitest";

import type { AutomationRun, AutomationRunStatus } from "@/lib/types";

import { groupRuns, isQuietStatus } from "./run-grouping";

function makeRun(id: string, status: AutomationRunStatus): AutomationRun {
  return {
    id,
    live_version: 1,
    automation_id: "auto-1",
    triggered_at: "2026-04-30T00:00:00Z",
    triggered_by: "schedule",
    goal_snapshot: "g",
    status,
    created_at: "2026-04-30T00:00:00Z",
    updated_at: "2026-04-30T00:00:00Z",
  };
}

describe("isQuietStatus", () => {
  it.each<AutomationRunStatus>(["completed_noop", "skipped"])("classifies %s as quiet", (status) => {
    expect(isQuietStatus(status)).toBe(true);
  });

  it.each<AutomationRunStatus>(["completed", "failed", "running", "pending"])(
    "classifies %s as loud",
    (status) => {
      expect(isQuietStatus(status)).toBe(false);
    },
  );
});

describe("groupRuns", () => {
  it("returns an empty list for no runs", () => {
    expect(groupRuns([])).toEqual([]);
  });

  it("renders all-loud runs as singles", () => {
    const runs = [
      makeRun("a", "completed"),
      makeRun("b", "failed"),
      makeRun("c", "running"),
    ];
    const groups = groupRuns(runs);
    expect(groups.map((g) => g.kind)).toEqual(["single", "single", "single"]);
  });

  it("groups ≥2 consecutive quiet runs into one quiet group", () => {
    const runs = [
      makeRun("a", "completed_noop"),
      makeRun("b", "skipped"),
      makeRun("c", "completed_noop"),
    ];
    const groups = groupRuns(runs);
    expect(groups).toHaveLength(1);
    // groupKey is the oldest run in the streak (last in DESC order) so
    // the key stays stable as new runs land on top during polling.
    expect(groups[0]).toMatchObject({
      kind: "quiet",
      groupKey: "c",
    });
    if (groups[0].kind === "quiet") {
      expect(groups[0].runs.map((r) => r.id)).toEqual(["a", "b", "c"]);
    }
  });

  it("renders a lone quiet run as a single (never a group of one)", () => {
    const runs = [
      makeRun("a", "completed"),
      makeRun("b", "completed_noop"),
      makeRun("c", "completed"),
    ];
    const groups = groupRuns(runs);
    expect(groups.map((g) => g.kind)).toEqual(["single", "single", "single"]);
    expect(groups[1]).toMatchObject({ kind: "single", run: { id: "b" } });
  });

  it("collapses consecutive quiet runs but lets loud runs split groups", () => {
    const runs = [
      makeRun("a", "completed_noop"),
      makeRun("b", "completed_noop"),
      makeRun("c", "failed"),
      makeRun("d", "skipped"),
      makeRun("e", "skipped"),
      makeRun("f", "completed"),
    ];
    const groups = groupRuns(runs);
    expect(groups.map((g) => g.kind)).toEqual(["quiet", "single", "quiet", "single"]);
    if (groups[0].kind === "quiet") expect(groups[0].runs.map((r) => r.id)).toEqual(["a", "b"]);
    if (groups[2].kind === "quiet") expect(groups[2].runs.map((r) => r.id)).toEqual(["d", "e"]);
  });

  it("groups all-quiet input into a single block", () => {
    const runs = [
      makeRun("a", "completed_noop"),
      makeRun("b", "skipped"),
      makeRun("c", "completed_noop"),
      makeRun("d", "completed_noop"),
    ];
    const groups = groupRuns(runs);
    expect(groups).toHaveLength(1);
    if (groups[0].kind === "quiet") {
      expect(groups[0].runs).toHaveLength(4);
      expect(groups[0].groupKey).toBe("d");
    }
  });

  it("keys quiet groups by the oldest run id so the key is stable across polling", () => {
    const initial = [
      makeRun("a", "completed_noop"),
      makeRun("b", "completed_noop"),
    ];
    const initialKey = (groupRuns(initial)[0] as { groupKey: string }).groupKey;

    // Simulate a polling tick: a new quiet run lands on top, but the
    // existing two stay. The streak's oldest run is still "b", so the
    // key — and therefore RunsTab's user-collapse state — survives.
    const after = [makeRun("z", "completed_noop"), ...initial];
    const afterKey = (groupRuns(after)[0] as { groupKey: string }).groupKey;
    expect(initialKey).toBe("b");
    expect(afterKey).toBe("b");
  });

  it("preserves run order inside a quiet group", () => {
    const runs = [
      makeRun("first", "completed_noop"),
      makeRun("second", "skipped"),
      makeRun("third", "completed_noop"),
    ];
    const groups = groupRuns(runs);
    if (groups[0].kind === "quiet") {
      expect(groups[0].runs.map((r) => r.id)).toEqual(["first", "second", "third"]);
    }
  });
});
