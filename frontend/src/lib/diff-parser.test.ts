import { describe, it, expect } from "vitest";
import {
  parseDiff,
  parseDiffStats,
  computeDiffDelta,
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
