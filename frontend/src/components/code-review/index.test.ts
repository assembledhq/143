import { describe, it, expect } from "vitest";
import * as CodeReview from "./index";

describe("code-review/index re-exports", () => {
  it("exports DiffStatsBadge", () => {
    expect(CodeReview.DiffStatsBadge).toBeDefined();
  });

  it("exports DiffLineRow", () => {
    expect(CodeReview.DiffLineRow).toBeDefined();
  });

  it("exports DiffHunk", () => {
    expect(CodeReview.DiffHunk).toBeDefined();
  });

  it("exports SplitDiffHunk", () => {
    expect(CodeReview.SplitDiffHunk).toBeDefined();
  });

  it("exports FileDiffHeader", () => {
    expect(CodeReview.FileDiffHeader).toBeDefined();
  });

  it("exports FileDiffSection", () => {
    expect(CodeReview.FileDiffSection).toBeDefined();
  });

  it("exports FileTree", () => {
    expect(CodeReview.FileTree).toBeDefined();
  });

  it("exports ReviewToolbar", () => {
    expect(CodeReview.ReviewToolbar).toBeDefined();
  });

  it("exports PassSelector", () => {
    expect(CodeReview.PassSelector).toBeDefined();
  });

  it("exports DiffPane", () => {
    expect(CodeReview.DiffPane).toBeDefined();
  });

  it("exports ContextExpander", () => {
    expect(CodeReview.ContextExpander).toBeDefined();
  });

  it("exports KeyboardHelpOverlay", () => {
    expect(CodeReview.KeyboardHelpOverlay).toBeDefined();
  });

  it("exports CommentInput", () => {
    expect(CodeReview.CommentInput).toBeDefined();
  });

  it("exports CommentThread", () => {
    expect(CodeReview.CommentThread).toBeDefined();
  });

  it("exports CommentsSummary", () => {
    expect(CodeReview.CommentsSummary).toBeDefined();
  });

  it("exports RepoExplorer", () => {
    expect(CodeReview.RepoExplorer).toBeDefined();
  });
});
