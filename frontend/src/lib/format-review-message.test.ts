import { describe, it, expect } from "vitest";
import { formatReviewMessage } from "./format-review-message";
import type { SessionReviewComment } from "./types";
import type { DiffFile } from "./diff-parser";

function makeComment(overrides: Partial<SessionReviewComment> = {}): SessionReviewComment {
  return {
    id: "c1",
    session_id: "s1",
    org_id: "o1",
    user_id: "u1",
    file_path: "src/app.ts",
    line_number: 10,
    diff_side: "new",
    body: "Fix this bug",
    resolved: false,
    pass_number: 1,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

const diffFiles: DiffFile[] = [
  {
    oldPath: "src/app.ts",
    newPath: "src/app.ts",
    hunks: [
      {
        oldStart: 9,
        oldCount: 3,
        newStart: 9,
        newCount: 4,
        header: "@@ -9,3 +9,4 @@",
        lines: [
          { type: "context", content: "const a = 1;", oldLineNumber: 9, newLineNumber: 9 },
          { type: "add", content: "const b = 2;", oldLineNumber: null, newLineNumber: 10 },
          { type: "context", content: "const c = 3;", oldLineNumber: 10, newLineNumber: 11 },
          { type: "context", content: "export default a;", oldLineNumber: 11, newLineNumber: 12 },
        ],
      },
    ],
    stats: { added: 1, removed: 0 },
    language: "typescript",
  },
];

describe("formatReviewMessage", () => {
  it("returns general instructions only when no comments", () => {
    const result = formatReviewMessage([], [], "Add tests please");
    expect(result).toBe("Add tests please");
  });

  it("returns empty string when no comments and no instructions", () => {
    const result = formatReviewMessage([], [], "");
    expect(result).toBe("");
  });

  it("formats a single comment with file, line, and body", () => {
    const result = formatReviewMessage([makeComment()], diffFiles, "");
    expect(result).toContain("1. **src/app.ts:10** (new side)");
    expect(result).toContain('Requested change: "Fix this bug"');
  });

  it("includes diff hunk context when available", () => {
    const result = formatReviewMessage([makeComment()], diffFiles, "");
    expect(result).toContain("```");
    expect(result).toContain("+ const b = 2;");
    expect(result).toContain("  const a = 1;");
  });

  it("marks the exact reviewed line inside diff context", () => {
    const result = formatReviewMessage([makeComment()], diffFiles, "");
    expect(result).toContain("Target line: `src/app.ts:10` (new side)");
    expect(result).toContain(">>> new 10 + const b = 2;");
    expect(result).toContain("    new 9   const a = 1;");
  });

  it("formats multiple comments with numbered list", () => {
    const comments = [
      makeComment({ id: "c1", file_path: "src/app.ts", line_number: 10, body: "First comment" }),
      makeComment({ id: "c2", file_path: "src/utils.ts", line_number: 5, body: "Second comment" }),
    ];
    const result = formatReviewMessage(comments, diffFiles, "");
    expect(result).toContain("1. **src/app.ts:10**");
    expect(result).toContain("2. **src/utils.ts:5**");
    expect(result).toContain('"First comment"');
    expect(result).toContain('"Second comment"');
  });

  it("separates general instructions with divider when comments present", () => {
    const result = formatReviewMessage([makeComment()], diffFiles, "Also add tests");
    expect(result).toContain("---");
    expect(result).toContain("Additional instructions:");
    expect(result).toContain("Also add tests");
  });

  it("does not include divider when only general instructions", () => {
    const result = formatReviewMessage([], [], "Just do this");
    expect(result).not.toContain("---");
    expect(result).not.toContain("Additional instructions:");
    expect(result).toBe("Just do this");
  });

  it("handles multi-line comment bodies with indentation", () => {
    const comment = makeComment({ body: "Line one\nLine two\nLine three" });
    const result = formatReviewMessage([comment], diffFiles, "");
    expect(result).toContain('Requested change: "Line one\n   Line two\n   Line three"');
  });

  it("skips diff hunk when file not found in diffFiles", () => {
    const comment = makeComment({ file_path: "nonexistent.ts" });
    const result = formatReviewMessage([comment], diffFiles, "");
    expect(result).toContain("1. **nonexistent.ts:10**");
    expect(result).not.toContain("```");
  });

  it("includes diff side in the header", () => {
    const comment = makeComment({ diff_side: "old" });
    const result = formatReviewMessage([comment], diffFiles, "");
    expect(result).toContain("(old side)");
  });

  it("starts with the standard preamble when comments are present", () => {
    const result = formatReviewMessage([makeComment()], diffFiles, "");
    expect(result).toMatch(/^Please address the following code review comments:/);
  });

  it("trims whitespace from general instructions", () => {
    const result = formatReviewMessage([], [], "  padded text  ");
    expect(result).toBe("padded text");
  });
});
