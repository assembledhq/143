import { getDiffHunkContext, type DiffFile } from "./diff-parser";
import type { SessionReviewComment } from "./types";

/**
 * Format review comments and optional general instructions into a structured
 * message for the coding agent. Inline comments include file path, line number,
 * diff side, surrounding code context, and the comment body. General instructions
 * are separated with a divider.
 */
export function formatReviewMessage(
  openComments: SessionReviewComment[],
  diffFiles: DiffFile[],
  generalInstructions: string
): string {
  const parts: string[] = [];

  // Section 1: Inline comments (anchored to specific file:line with diff context)
  if (openComments.length > 0) {
    parts.push("Please address the following code review comments:\n");
    openComments.forEach((c, i) => {
      parts.push(`${i + 1}. **${c.file_path}:${c.line_number}** (${c.diff_side} side)`);
      parts.push(`   Target line: \`${c.file_path}:${c.line_number}\` (${c.diff_side} side)`);
      const hunk = getDiffHunkContext(diffFiles, c.file_path, c.line_number, c.diff_side as "old" | "new");
      if (hunk) {
        parts.push("   ```");
        hunk.split("\n").forEach((line) => parts.push(`   ${line}`));
        parts.push("   ```");
      }
      const indented = c.body.replace(/\n/g, "\n   ");
      parts.push(`   Requested change: "${indented}"\n`);
    });
  }

  // Section 2: General instructions (not tied to a specific line)
  if (generalInstructions.trim()) {
    if (openComments.length > 0) {
      parts.push("---\n");
      parts.push("Additional instructions:\n");
      parts.push(generalInstructions.trim());
    } else {
      parts.push(generalInstructions.trim());
    }
  }

  return parts.join("\n").trim();
}
