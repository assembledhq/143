"use client";

import { useMemo } from "react";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { SessionThread, SessionThreadFileEvent } from "@/lib/types";

// ThreadAttributionFilterValue is the union of filter modes the user can pick
// in the Changes view. Distinct strings (rather than enums) make the URL
// query-string serialization in the future (see nuqs) trivial.
//
// "unattributed" is intentionally not exposed: with the current pipeline,
// every file event carries a thread_id, so the only "unattributed" paths
// are ones that show up in the workspace diff but were never recorded as a
// thread edit (e.g. preview hydration touched them). Distinguishing those
// from the diff alone needs the full diff file list — out of scope for the
// filter component, which only sees the event timeline.
export type ThreadAttributionFilterValue =
  | { kind: "all" }
  | { kind: "touched_by"; threadId: string }
  | { kind: "overlap" };

interface ThreadAttributionFilterProps {
  threads: SessionThread[];
  value: ThreadAttributionFilterValue;
  onChange: (next: ThreadAttributionFilterValue) => void;
}

// Serializable single-string form used by the underlying Select primitive.
function toKey(v: ThreadAttributionFilterValue): string {
  if (v.kind === "touched_by") return `tab:${v.threadId}`;
  return v.kind;
}
function fromKey(key: string): ThreadAttributionFilterValue {
  if (key.startsWith("tab:")) return { kind: "touched_by", threadId: key.slice(4) };
  if (key === "overlap") return { kind: "overlap" };
  return { kind: "all" };
}

// ThreadAttributionFilter renders a compact dropdown the user can use to
// scope the Changes view to a single tab's outputs or the overlap between
// tabs. The filter is visual-only — it returns paths the parent uses to
// gate the file list.
export function ThreadAttributionFilter({ threads, value, onChange }: ThreadAttributionFilterProps) {
  // Show the filter only when there is more than one tab, otherwise it is
  // noise — single-tab sessions have nothing to attribute.
  if (threads.length < 2) return null;
  return (
    <Select value={toKey(value)} onValueChange={(k) => onChange(fromKey(k))}>
      <SelectTrigger size="sm" className="h-7 w-[180px] text-xs">
        <SelectValue placeholder="Filter changes" />
      </SelectTrigger>
      <SelectContent align="end">
        <SelectItem value="all">All changes</SelectItem>
        <SelectItem value="overlap">Overlap with another tab</SelectItem>
        {threads.map((t) => (
          <SelectItem key={t.id} value={`tab:${t.id}`}>
            Touched by {t.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

// computeAttributionAllowedPaths returns the set of paths a given filter
// admits, derived from the file-event timeline. Returns null when the
// filter is "all", which the caller treats as "no path-level filtering".
//
// Implementation detail: built on a Map of path -> Set of owner thread IDs
// so each filter mode is a simple set predicate. The frontend re-runs this
// whenever events change; eventCount is bounded by turns × tabs so this is
// well within rendering budget.
export function useAttributionAllowedPaths(
  filter: ThreadAttributionFilterValue,
  events: SessionThreadFileEvent[] | undefined,
): Set<string> | null {
  return useMemo(() => {
    if (filter.kind === "all") return null;
    if (!events || events.length === 0) return new Set<string>();
    const owners = new Map<string, Set<string>>();
    for (const e of events) {
      let bucket = owners.get(e.path);
      if (!bucket) {
        bucket = new Set<string>();
        owners.set(e.path, bucket);
      }
      if (e.thread_id) bucket.add(e.thread_id);
    }
    const out = new Set<string>();
    for (const [path, ids] of owners) {
      switch (filter.kind) {
        case "touched_by":
          if (ids.has(filter.threadId)) out.add(path);
          break;
        case "overlap":
          if (ids.size >= 2) out.add(path);
          break;
      }
    }
    return out;
  }, [filter, events]);
}
