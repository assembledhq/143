import { describe, expect, it } from "vitest";
import {
  applyInlineDiffRangesToHtml,
  buildInlineDiffRanges,
} from "./inline-diff-highlight";
import type { DiffLine } from "@/lib/diff-parser";

describe("inline diff highlighting", () => {
  it("applies inline highlights inside syntax-highlighted HTML", () => {
    const html = '<span style="color:#f00">alpha: 9, beta: 8</span>';
    const highlighted = applyInlineDiffRangesToHtml(
      html,
      [
        { start: 7, end: 8 },
        { start: 16, end: 17 },
      ],
      "add"
    );
    expect(highlighted).toContain('style="color:#f00"');
    expect(highlighted).toContain('class="rounded-[2px] bg-green-200/80 dark:bg-green-800/45"');
    expect(highlighted).toContain(">9</span>");
    expect(highlighted).toContain(">8</span>");
  });

  it("counts escaped HTML entities as one source character", () => {
    const html = '<span style="color:#f00">a &amp; b</span>';
    const highlighted = applyInlineDiffRangesToHtml(
      html,
      [{ start: 2, end: 3 }],
      "remove"
    );
    expect(highlighted).toContain(
      '<span class="rounded-[2px] bg-red-200/80 dark:bg-red-800/45">&amp;</span>'
    );
  });

  it("falls back to a bounded changed range for very long paired lines", () => {
    const removedContent = "a".repeat(400);
    const addedContent = "b".repeat(400);
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: removedContent,
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "add",
        content: addedContent,
        oldLineNumber: null,
        newLineNumber: 1,
      },
    ];
    const ranges = buildInlineDiffRanges(lines);
    expect(ranges.get(0)).toEqual([{ start: 0, end: 400 }]);
    expect(ranges.get(1)).toEqual([{ start: 0, end: 400 }]);
  });
});
