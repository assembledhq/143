import { useCallback, useState } from "react";

/** localStorage key for persisting which files have been reviewed.
 *  Namespaced by sessionId (UUIDs are globally unique, so org collision is not a concern). */
const REVIEWED_FILES_KEY = "diff-reviewed-files";

function loadReviewedFiles(sessionId: string): Set<string> {
  if (typeof window === "undefined") return new Set();
  try {
    const stored = localStorage.getItem(`${REVIEWED_FILES_KEY}:${sessionId}`);
    if (stored) return new Set(JSON.parse(stored) as string[]);
  } catch { /* ignore corrupt data */ }
  return new Set();
}

function saveReviewedFiles(sessionId: string, files: Set<string>) {
  if (typeof window === "undefined") return;
  localStorage.setItem(`${REVIEWED_FILES_KEY}:${sessionId}`, JSON.stringify([...files]));
}

export interface UseReviewedFilesResult {
  reviewedFiles: Set<string>;
  toggleReviewed: (filePath: string) => void;
}

/**
 * Hook that manages the "reviewed" state for diff files, persisted in localStorage.
 */
export function useReviewedFiles(sessionId: string): UseReviewedFilesResult {
  const [reviewedFiles, setReviewedFiles] = useState<Set<string>>(
    () => loadReviewedFiles(sessionId)
  );

  const toggleReviewed = useCallback(
    (filePath: string) => {
      setReviewedFiles((prev) => {
        const next = new Set(prev);
        if (next.has(filePath)) {
          next.delete(filePath);
        } else {
          next.add(filePath);
        }
        saveReviewedFiles(sessionId, next);
        return next;
      });
    },
    [sessionId]
  );

  return { reviewedFiles, toggleReviewed };
}
