import type { CodingCredentialSummary } from "@/lib/types";

export function reorderRows(
  rows: CodingCredentialSummary[],
  sourceId: string,
  targetId: string,
  position: "before" | "after",
) {
  if (sourceId === targetId) return rows;
  const sourceIdx = rows.findIndex((row) => row.id === sourceId);
  const targetIdx = rows.findIndex((row) => row.id === targetId);
  if (sourceIdx === -1 || targetIdx === -1) return rows;
  const next = [...rows];
  const [row] = next.splice(sourceIdx, 1);
  // After removing the source, indices past it shift down by one.
  const targetIdxAfterRemove = sourceIdx < targetIdx ? targetIdx - 1 : targetIdx;
  const insertAt = position === "before" ? targetIdxAfterRemove : targetIdxAfterRemove + 1;
  if (insertAt === sourceIdx) return rows;
  next.splice(insertAt, 0, row);
  return next;
}
