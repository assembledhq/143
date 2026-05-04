package worker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// Pin the diff-header parser so a regression cannot silently misattribute
// touched files. Each case stresses one bucket the orchestrator's emitter
// depends on: created, modified, deleted, renamed.
func TestParseDiffFileEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		diff     string
		expected []models.SessionThreadFileEvent
	}{
		{
			name:     "empty diff yields no events",
			diff:     "",
			expected: nil,
		},
		{
			name: "modified file",
			diff: "diff --git a/foo.go b/foo.go\nindex 1..2 100644\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
			expected: []models.SessionThreadFileEvent{
				{Path: "foo.go", EventType: models.FileEventTypeModified},
			},
		},
		{
			name: "newly created file",
			diff: "diff --git a/new.go b/new.go\nnew file mode 100644\nindex 0000000..abcdef\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1 @@\n+content\n",
			expected: []models.SessionThreadFileEvent{
				{Path: "new.go", EventType: models.FileEventTypeCreated},
			},
		},
		{
			name: "deleted file",
			diff: "diff --git a/gone.go b/gone.go\ndeleted file mode 100644\nindex abcdef..0000000\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-content\n",
			expected: []models.SessionThreadFileEvent{
				{Path: "gone.go", EventType: models.FileEventTypeDeleted},
			},
		},
		{
			name: "renamed file emits delete + create",
			diff: "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			expected: []models.SessionThreadFileEvent{
				{Path: "old.go", EventType: models.FileEventTypeDeleted},
				{Path: "new.go", EventType: models.FileEventTypeCreated},
			},
		},
		{
			name: "multiple files in one diff",
			diff: "diff --git a/a.go b/a.go\nindex 1..2\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n" +
				"diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n",
			expected: []models.SessionThreadFileEvent{
				{Path: "a.go", EventType: models.FileEventTypeModified},
				{Path: "b.go", EventType: models.FileEventTypeCreated},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseDiffFileEvents(tt.diff)
			require.Equal(t, len(tt.expected), len(got), "event count mismatch")
			for i := range tt.expected {
				require.Equal(t, tt.expected[i].Path, got[i].Path, "event %d path", i)
				require.Equal(t, tt.expected[i].EventType, got[i].EventType, "event %d type", i)
			}
		})
	}
}
