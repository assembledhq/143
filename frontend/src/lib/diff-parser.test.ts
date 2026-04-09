    expect(files).toHaveLength(1);
    expect(files[0].newPath).toBe("src/hello.ts");
  });

  it("uses the old path for deleted files instead of /dev/null", () => {
    const deletedFileDiff = `diff --git a/src/deleted.ts b/src/deleted.ts
--- a/src/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-export const removed = true;
-export default removed;`;

    const files = parseDiff(deletedFileDiff);

    expect(files).toHaveLength(1);
    expect(files[0].oldPath).toBe("src/deleted.ts");
    expect(files[0].newPath).toBe("src/deleted.ts");
    expect(files[0].stats).toEqual({ added: 0, removed: 2 });
  });

  it("matches deleted files by their original path when extracting hunk context", () => {
    const deletedFileDiff = `diff --git a/src/deleted.ts b/src/deleted.ts
--- a/src/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-export const removed = true;
-export default removed;`;

    const files = parseDiff(deletedFileDiff);
    const context = getDiffHunkContext(files, "src/deleted.ts", 1, "old");

    expect(context).toContain("- export const removed = true;");
  });
});

describe("getDiffHunkContext", () => {
