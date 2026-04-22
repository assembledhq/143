import { useMemo, useState } from "react";
import { parseDiff, computeDiffDelta } from "@/lib/diff-parser";
import type { DiffFile } from "@/lib/diff-parser";
import { sortDiffFiles } from "@/lib/diff-file-order";
import type { PassRange, DiffPassEntry } from "@/components/code-review";
import type { Session } from "@/lib/types";

  }, [passRange, passes, allFiles]);

  // Filter files by search query (matches file path or line content)
  const orderedFiles = useMemo(() => sortDiffFiles(files), [files]);

  const filteredFiles = useMemo(() => {
    if (!diffSearchQuery.trim()) return orderedFiles;
    const q = diffSearchQuery.toLowerCase();
    return orderedFiles.filter((f) => {
      if (f.newPath.toLowerCase().includes(q)) return true;
      if (f.oldPath.toLowerCase().includes(q)) return true;
      return f.hunks.some((h) =>
        h.lines.some((l) => l.content.toLowerCase().includes(q))
      );
    });
  }, [orderedFiles, diffSearchQuery]);

  return {
    allFiles,
    files: orderedFiles,
    filteredFiles,
    passes,
    passRange,
