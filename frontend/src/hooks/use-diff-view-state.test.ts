    expect(result.current.allFiles[0].newPath).toBe("src/app.ts");
  });

  it("orders files by impact so the changes list and detail view stay aligned", () => {
    const multiFileDiff = `diff --git a/src/z-last.ts b/src/z-last.ts
--- a/src/z-last.ts
+++ b/src/z-last.ts
@@ -1 +1,3 @@
-const value = 1;
+const value = 2;
+const next = 3;
+const finalValue = 4;
diff --git a/src/a-first.ts b/src/a-first.ts
--- a/src/a-first.ts
+++ b/src/a-first.ts
@@ -1 +1,3 @@
-const start = 1;
+const start = 2;
+const middle = 3;
+const end = 4;
diff --git a/src/m-middle.ts b/src/m-middle.ts
--- a/src/m-middle.ts
+++ b/src/m-middle.ts
@@ -1 +1,2 @@
-const helper = 1;
+const helper = 2;
`;

    const { result } = renderHook(() =>
      useDiffViewState(makeSession({ diff: multiFileDiff }))
    );

    expect(
      result.current.filteredFiles.map((file) => file.newPath)
    ).toEqual(
      ["src/a-first.ts", "src/z-last.ts", "src/m-middle.ts"]
    );
  });

  it("returns passes from diff_history", () => {
    const diffHistory = [
      { pass: 1, diff: simpleDiff, diff_stats: { added: 2, removed: 1, files_changed: 1 }, created_at: "2026-01-01T00:00:00Z" },
