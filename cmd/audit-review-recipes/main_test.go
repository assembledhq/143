package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/reviewaudit"
	"github.com/stretchr/testify/require"
)

func TestRecipeAuditCommandExitGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, owner    string
		expectedCode   int
		expectedIssues []string
	}{
		{name: "reviewed git-only inventory opens gate", owner: "repo-maintainers", expectedCode: 0, expectedIssues: []string{}},
		{name: "unowned inventory closes gate", expectedCode: 1, expectedIssues: []string{"a recipe/config owner is required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inventory := reviewaudit.Inventory{Version: 1, Repositories: []reviewaudit.Repository{{Repository: "example/repo", Revision: strings.Repeat("a", 40), ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256(nil)), Owner: tt.owner, RequiredEvidence: []reviewaudit.EvidenceCategory{"git"}}}}
			data, err := json.Marshal(inventory)
			require.NoError(t, err, "audit fixture should encode")
			file := filepath.Join(t.TempDir(), "inventory.json")
			require.NoError(t, os.WriteFile(file, data, 0o600), "private inventory should be written")
			var stdout, stderr bytes.Buffer
			require.Equal(t, tt.expectedCode, run([]string{"--manifest", file}, &stdout, &stderr), "command exit should enforce the audit gate")
			require.Equal(t, "", stderr.String(), "valid inventories should produce no operational error logs")
			var report reviewaudit.Report
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), "command should emit a machine-readable report")
			require.Equal(t, reviewaudit.Report{Ready: tt.expectedCode == 0, Repositories: []reviewaudit.RepositoryReport{{Repository: "example/repo", Revision: strings.Repeat("a", 40), ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256(nil)), Owner: tt.owner, Ready: tt.expectedCode == 0, DeclaredTools: []string{}, RequiredEvidence: []reviewaudit.EvidenceCategory{"git"}, CoveredEvidence: []reviewaudit.EvidenceCategory{"git"}, Issues: tt.expectedIssues}}}, report, "report should explain exactly why the release gate opens or closes")
		})
	}
}

func TestRecipeAuditCommandRequiresExplicitInventory(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	require.Equal(t, 2, run(nil, &stdout, &stderr), "missing inventory must not silently open the gate")
	require.Equal(t, "", stdout.String(), "invalid invocation should not emit a readiness report")
	require.Equal(t, "{\"level\":\"error\",\"message\":\"pass --manifest with an explicit private repository inventory\"}\n", stderr.String(), "invalid invocation should explain the required audit input")
}
