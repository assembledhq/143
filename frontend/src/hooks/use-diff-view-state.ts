import { useMemo, useState } from "react";
import { parseDiff, computeDiffDelta } from "@/lib/diff-parser";
import type { DiffFile } from "@/lib/diff-parser";
import type { PassRange, DiffPassEntry } from "@/components/code-review";
import type { Session } from "@/lib/types";

export interface UseDiffViewStateResult {
  allFiles: DiffFile[];
  files: DiffFile[];
  filteredFiles: DiffFile[];
  passes: DiffPassEntry[];
  passRange: PassRange | null;
  setPassRange: (range: PassRange | null) => void;
  diffSearchQuery: string;
  setDiffSearchQuery: (q: string) => void;
}

/**
 * Extracts pass-selection and diff-parsing logic from ChangesTab.
 * Parses the session diff, computes pass deltas, and filters by search query.
 */
export function useDiffViewState(session: Session): UseDiffViewStateResult {
  const [passRange, setPassRange] = useState<PassRange | null>(null);
  const [diffSearchQuery, setDiffSearchQuery] = useState("");

  const passes: DiffPassEntry[] = useMemo(
    () => session.diff_history ?? [],
    [session.diff_history]
  );

  // Parse the full (latest) diff
  const allFiles = useMemo(
    () => (session.diff ? parseDiff(session.diff) : []),
    [session.diff]
  );

  // When a pass range is selected, compute the delta between two passes
  const files = useMemo(() => {
    if (!passRange || passes.length < 2) return allFiles;

    const fromPass = passes.find((p) => p.pass === passRange.from);
    const toPass = passes.find((p) => p.pass === passRange.to);

    // "Base → Pass N" — show that pass's diff directly
    if (passRange.from === 0 && toPass?.diff) {
      return parseDiff(toPass.diff);
    }

    // "Pass A → Pass B" — compute delta
    if (fromPass?.diff && toPass?.diff) {
      const olderFiles = parseDiff(fromPass.diff);
      const newerFiles = parseDiff(toPass.diff);
      return computeDiffDelta(olderFiles, newerFiles);
    }

    return allFiles;
  }, [passRange, passes, allFiles]);

  // Filter files by search query (matches file path or line content)
  const filteredFiles = useMemo(() => {
    if (!diffSearchQuery.trim()) return files;
    const q = diffSearchQuery.toLowerCase();
    return files.filter((f) => {
      if (f.newPath.toLowerCase().includes(q)) return true;
      if (f.oldPath.toLowerCase().includes(q)) return true;
      return f.hunks.some((h) =>
        h.lines.some((l) => l.content.toLowerCase().includes(q))
      );
    });
  }, [files, diffSearchQuery]);

  return {
    allFiles,
    files,
    filteredFiles,
    passes,
    passRange,
    setPassRange,
    diffSearchQuery,
    setDiffSearchQuery,
  };
}
