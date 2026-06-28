import { describe, expect, it } from "vitest";
import {
  applyInlineDiffRangesToHtml,
  buildInlineDiffRanges,
} from "./inline-diff-highlight";
import type { DiffLine } from "@/lib/diff-parser";

describe("inline diff highlighting", () => {
  it("suppresses inline highlights for unrelated prose rewrites", () => {
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: 'agent receives an empty "Session task" seed and silently ignores any',
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "add",
        content: "metadata here because automation goals often scope themselves relative",
        oldLineNumber: null,
        newLineNumber: 1,
      },
    ];

    const ranges = buildInlineDiffRanges(lines);

    expect(ranges.get(0)).toBeUndefined();
    expect(ranges.get(1)).toBeUndefined();
  });

  it("does not treat prose punctuation as code structure", () => {
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: "agent receives empty metadata, then silently ignores the update.",
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "add",
        content: "automation goals scope relative context, then preserve repository state.",
        oldLineNumber: null,
        newLineNumber: 1,
      },
    ];

    const ranges = buildInlineDiffRanges(lines);

    expect(ranges.get(0)).toBeUndefined();
    expect(ranges.get(1)).toBeUndefined();
  });

  it("highlights whole changed tokens for related code lines", () => {
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: "goalSeed = &g",
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "add",
        content: "goalSeed, err := automationRunPromptSeed(run)",
        oldLineNumber: null,
        newLineNumber: 1,
      },
    ];

    const ranges = buildInlineDiffRanges(lines);

    expect(ranges.get(0)).toEqual([{ start: 9, end: 13 }]);
    expect(ranges.get(1)).toEqual([{ start: 8, end: 45 }]);
  });

  it("keeps inline highlights for tiny code substitutions", () => {
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: "x = y",
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "add",
        content: "x = z",
        oldLineNumber: null,
        newLineNumber: 1,
      },
    ];

    const ranges = buildInlineDiffRanges(lines);

    expect(ranges.get(0)).toEqual([{ start: 4, end: 5 }]);
    expect(ranges.get(1)).toEqual([{ start: 4, end: 5 }]);
  });

  it("skips weak line pairs before computing inline ranges", () => {
    const lines: DiffLine[] = [
      {
        type: "remove",
        content: "const status = getStatus(run)",
        oldLineNumber: 1,
        newLineNumber: null,
      },
      {
        type: "remove",
        content: "metadata here because automation goals often scope themselves relative",
        oldLineNumber: 2,
        newLineNumber: null,
      },
      {
        type: "add",
        content: "const status = getRunStatus(run)",
        oldLineNumber: null,
        newLineNumber: 1,
      },
      {
        type: "add",
        content: "agent receives an empty session task seed and silently ignores it",
        oldLineNumber: null,
        newLineNumber: 2,
      },
    ];

    const ranges = buildInlineDiffRanges(lines);

    expect(ranges.get(0)).toEqual([{ start: 15, end: 24 }]);
    expect(ranges.get(1)).toBeUndefined();
    expect(ranges.get(2)).toEqual([{ start: 15, end: 27 }]);
    expect(ranges.get(3)).toBeUndefined();
  });

  it("does not force unrelated long prose through the long-line fallback", () => {
    const removedContent = Array.from(
      { length: 30 },
      (_, index) => `removedToken${index}`
    ).join(" ");
    const addedContent = Array.from(
      { length: 30 },
      (_, index) => `addedToken${index}`
    ).join(" ");
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

    expect(ranges.get(0)).toBeUndefined();
    expect(ranges.get(1)).toBeUndefined();
  });

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
