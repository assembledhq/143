import type { AutomationRun, AutomationRunStatus } from "@/lib/types";

// Mirror of QUIET_RUN_STATUSES in run-card.tsx, kept here too so the
// grouping helpers stay independent of the React component module (the
// unit test imports just this file).
const QUIET_STATUSES: ReadonlyArray<AutomationRunStatus> = ["completed_noop", "skipped"];

export function isQuietStatus(status: AutomationRunStatus): boolean {
  return QUIET_STATUSES.includes(status);
}

export type RunGroup =
  | { kind: "single"; run: AutomationRun }
  | { kind: "quiet"; runs: AutomationRun[]; groupKey: string };

// Collapse runs of consecutive quiet statuses into a single group.
//
// Rules (matched 1:1 in the UX spec):
// - ≥2 consecutive quiet runs → one "quiet" group rendered as a thin
//   collapsible bar with "N quiet runs" headline.
// - A lone quiet run (sandwiched between two non-quiet ones, or at the
//   ends of the list) renders as its own thin row (kind: "single") so we
//   never show a group of one.
// - Order is preserved — the input is expected DESC by triggered_at and
//   the groups carry that ordering through.
//
// Group keying uses the *oldest* run in the streak (the last element,
// since DESC ordering puts the oldest at the end). New quiet runs landing
// on top of an existing streak grow the streak without changing its key,
// so RunsTab's user-collapse state survives polling refetches. The key
// only changes once the oldest run scrolls off the visible page entirely.
export function groupRuns(runs: AutomationRun[]): RunGroup[] {
  const out: RunGroup[] = [];
  let buf: AutomationRun[] = [];

  const flushQuiet = () => {
    if (buf.length >= 2) {
      out.push({ kind: "quiet", runs: buf, groupKey: buf[buf.length - 1].id });
    } else if (buf.length === 1) {
      out.push({ kind: "single", run: buf[0] });
    }
    buf = [];
  };

  for (const run of runs) {
    if (isQuietStatus(run.status)) {
      buf.push(run);
    } else {
      flushQuiet();
      out.push({ kind: "single", run });
    }
  }
  flushQuiet();
  return out;
}
