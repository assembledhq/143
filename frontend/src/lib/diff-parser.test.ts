import { describe, it, expect } from "vitest";
import {
  parseDiff,
  parseDiffStats,
  computeDiffDelta,
  getDiffHunkContext,
  type DiffFile,
} from "./diff-parser";

const sampleDiff = `diff --git a/src/app.ts b/src/app.ts
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,3 +1,4 @@
 import express from "express";
+import cors from "cors";
 const app = express();
 app.listen(3000);
diff --git a/src/utils.ts b/src/utils.ts
--- a/src/utils.ts
+++ b/src/utils.ts
@@ -10,3 +10,3 @@
 function add(a: number, b: number) {
-  return a + b;
+  return a + b + 0;
 }`;

describe("parseDiffStats", () => {
  it("counts additions, removals, and files", () => {
    const stats = parseDiffStats(sampleDiff);
    expect(stats.added).toBe(2);
    expect(stats.removed).toBe(1);
    expect(stats.filesChanged).toBe(2);
  });

  it("returns zeros for empty diff", () => {
    const stats = parseDiffStats("");
    expect(stats).toEqual({ added: 0, removed: 0, filesChanged: 0 });
  });
});

describe("parseDiff", () => {
  it("parses files and hunks correctly", () => {
    const files = parseDiff(sampleDiff);
    expect(files).toHaveLength(2);
    expect(files[0].newPath).toBe("src/app.ts");
    expect(files[0].stats.added).toBe(1);
    expect(files[0].stats.removed).toBe(0);
    expect(files[1].newPath).toBe("src/utils.ts");
    expect(files[1].stats.added).toBe(1);
    expect(files[1].stats.removed).toBe(1);
  });

  it("assigns correct line numbers", () => {
    const files = parseDiff(sampleDiff);
    const hunk = files[0].hunks[0];
    // context line: import express
    expect(hunk.lines[0].oldLineNumber).toBe(1);
    expect(hunk.lines[0].newLineNumber).toBe(1);
    // add line: import cors
    expect(hunk.lines[1].oldLineNumber).toBeNull();
    expect(hunk.lines[1].newLineNumber).toBe(2);
  });

  it("infers language from file extension", () => {
    const files = parseDiff(sampleDiff);
    expect(files[0].language).toBe("typescript");
  });
});

describe("computeDiffDelta", () => {
  const pass1Diff = `diff --git a/src/app.ts b/src/app.ts
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,3 +1,4 @@
 import express from "express";
+import cors from "cors";
 const app = express();
 app.listen(3000);
diff --git a/src/utils.ts b/src/utils.ts
--- a/src/utils.ts
+++ b/src/utils.ts
@@ -10,3 +10,3 @@
 function add(a: number, b: number) {
-  return a + b;
+  return a + b + 0;
 }`;

  const pass2Diff = `diff --git a/src/app.ts b/src/app.ts
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,3 +1,4 @@
 import express from "express";
+import cors from "cors";
 const app = express();
 app.listen(3000);
diff --git a/src/utils.ts b/src/utils.ts
--- a/src/utils.ts
+++ b/src/utils.ts
@@ -10,3 +10,4 @@
 function add(a: number, b: number) {
-  return a + b;
+  if (typeof a !== "number") throw new Error("a must be number");
+  return a + b + 0;
 }
diff --git a/src/new-file.ts b/src/new-file.ts
--- /dev/null
+++ b/src/new-file.ts
@@ -0,0 +1,3 @@
+export function newFeature() {
+  return true;
+}`;

  it("returns empty when diffs are identical", () => {
    const older = parseDiff(pass1Diff);
    const newer = parseDiff(pass1Diff);
    const delta = computeDiffDelta(older, newer);
    expect(delta).toHaveLength(0);
  });

  it("includes files that changed between passes", () => {
    const older = parseDiff(pass1Diff);
    const newer = parseDiff(pass2Diff);
    const delta = computeDiffDelta(older, newer);

    // src/utils.ts changed (different hunks), src/new-file.ts is new
    // src/app.ts is identical → excluded
    const paths = delta.map((f) => f.newPath).sort();
    expect(paths).toEqual(["src/new-file.ts", "src/utils.ts"]);
  });

  it("includes reverted files as empty-hunk entries", () => {
    const older = parseDiff(pass2Diff); // has src/new-file.ts
    // Build a newer diff that doesn't have src/new-file.ts
    const newerDiff = `diff --git a/src/app.ts b/src/app.ts
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,3 +1,4 @@
 import express from "express";
+import cors from "cors";
 const app = express();
 app.listen(3000);
diff --git a/src/utils.ts b/src/utils.ts
--- a/src/utils.ts
+++ b/src/utils.ts
@@ -10,3 +10,4 @@
 function add(a: number, b: number) {
-  return a + b;
+  if (typeof a !== "number") throw new Error("a must be number");
+  return a + b + 0;
 }`;
    const newer = parseDiff(newerDiff);
    const delta = computeDiffDelta(older, newer);

    // src/new-file.ts was in older but not newer → reverted
    const revertedFile = delta.find((f) => f.newPath === "src/new-file.ts");
    expect(revertedFile).toBeDefined();
    expect(revertedFile!.hunks).toHaveLength(0);
    expect(revertedFile!.stats).toEqual({ added: 0, removed: 0 });
  });

  it("includes new files not in older diff", () => {
    const older = parseDiff(pass1Diff);
    const newer = parseDiff(pass2Diff);
    const delta = computeDiffDelta(older, newer);

    const newFile = delta.find((f) => f.newPath === "src/new-file.ts");
    expect(newFile).toBeDefined();
    expect(newFile!.stats.added).toBe(3);
  });

  it("handles empty older diff", () => {
    const older: DiffFile[] = [];
    const newer = parseDiff(pass1Diff);
    const delta = computeDiffDelta(older, newer);
    expect(delta).toHaveLength(2);
  });

  it("handles empty newer diff", () => {
    const older = parseDiff(pass1Diff);
    const newer: DiffFile[] = [];
    const delta = computeDiffDelta(older, newer);
    // All files in older are "reverted"
    expect(delta).toHaveLength(2);
    expect(delta.every((f) => f.hunks.length === 0)).toBe(true);
  });
});

describe("parseDiff fallback path parsing", () => {
  it("falls back to first-line path parsing when --- / +++ headers are missing", () => {
    const minimalDiff = `diff --git a/src/hello.ts b/src/hello.ts
@@ -1,2 +1,2 @@
 const greeting = "hello";
-export default greeting;
+export { greeting };`;
    const files = parseDiff(minimalDiff);
    expect(files).toHaveLength(1);
    expect(files[0].newPath).toBe("src/hello.ts");
  });
});

describe("getDiffHunkContext", () => {
  const files = parseDiff(sampleDiff);

  it("returns surrounding lines around a target line", () => {
    // Line 2 (new side) is the added "import cors" line
    const ctx = getDiffHunkContext(files, "src/app.ts", 2, "new");
    expect(ctx).not.toBeNull();
    expect(ctx).toContain("import cors");
    expect(ctx).toContain("import express");
  });

  it("returns null when file is not found", () => {
    const ctx = getDiffHunkContext(files, "nonexistent.ts", 1, "new");
    expect(ctx).toBeNull();
  });

  it("returns null when line number is not in any hunk", () => {
    const ctx = getDiffHunkContext(files, "src/app.ts", 999, "new");
    expect(ctx).toBeNull();
  });

  it("formats add lines with + prefix", () => {
    const ctx = getDiffHunkContext(files, "src/app.ts", 2, "new");
    expect(ctx).toContain("+ import cors");
  });

  it("formats context lines with space prefix", () => {
    const ctx = getDiffHunkContext(files, "src/app.ts", 2, "new");
    expect(ctx).toContain("  import express");
  });

  it("works with old side line numbers", () => {
    // src/utils.ts has a removal on old line 11
    const ctx = getDiffHunkContext(files, "src/utils.ts", 11, "old");
    expect(ctx).not.toBeNull();
    expect(ctx).toContain("-   return a + b;");
  });

  it("limits context to 3 lines before and after", () => {
    const ctx = getDiffHunkContext(files, "src/app.ts", 2, "new");
    const lines = ctx!.split("\n");
    // Hunk has 4 lines total (1 context + 1 add + 2 context), so all fit within ±3
    expect(lines.length).toBeLessThanOrEqual(7);
  });

  it("matches file by oldPath when newPath differs", () => {
    const renamedFiles: DiffFile[] = [{
      oldPath: "src/old-name.ts",
      newPath: "src/new-name.ts",
      hunks: [{
        oldStart: 1, oldCount: 1, newStart: 1, newCount: 1,
        header: "@@ -1,1 +1,1 @@",
        lines: [
          { type: "remove", content: "old line", oldLineNumber: 1, newLineNumber: null },
          { type: "add", content: "new line", oldLineNumber: null, newLineNumber: 1 },
        ],
      }],
      stats: { added: 1, removed: 1 },
      language: "typescript",
    }];
    const ctx = getDiffHunkContext(renamedFiles, "src/old-name.ts", 1, "old");
    expect(ctx).not.toBeNull();
    expect(ctx).toContain("- old line");
  });
});
