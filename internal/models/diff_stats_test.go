package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeDiffStats_Empty(t *testing.T) {
	t.Parallel()
	require.Nil(t, ComputeDiffStats(""))
}

func TestComputeDiffStats_SingleFile(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/main.go b/main.go
index abc..def 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
+func hello() {}
-func old() {}
 func main() {}`

	result := ComputeDiffStats(diff)
	require.NotNil(t, result)

	var stats map[string]int
	require.NoError(t, json.Unmarshal(result, &stats))
	require.Equal(t, 2, stats["added"])
	require.Equal(t, 1, stats["removed"])
	require.Equal(t, 1, stats["files_changed"])
}

func TestComputeDiffStats_MultiFile(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+func A() {}
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,3 +1,2 @@
 package b
-func Old() {}
+func New() {}
-func Remove() {}`

	result := ComputeDiffStats(diff)
	require.NotNil(t, result)

	var stats map[string]int
	require.NoError(t, json.Unmarshal(result, &stats))
	require.Equal(t, 2, stats["added"])
	require.Equal(t, 2, stats["removed"])
	require.Equal(t, 2, stats["files_changed"])
}

func TestComputeDiffStats_JSONStructure(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1 +1,2 @@
 package x
+func X() {}`

	result := ComputeDiffStats(diff)
	require.NotNil(t, result)

	// Verify it's valid JSON with exactly the expected keys.
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &raw))
	require.Len(t, raw, 3)
	require.Contains(t, raw, "added")
	require.Contains(t, raw, "removed")
	require.Contains(t, raw, "files_changed")
}
