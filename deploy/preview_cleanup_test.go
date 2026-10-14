package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeployPrunePreservesManagedPreviewOwnership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		role        string
		enabled     string
		volumePrune string
		want        []string
	}{
		{
			name: "worker keeps managed infrastructure for reconciliation",
			role: "worker", enabled: "1",
			want: []string{
				"container prune -f --filter until=24h --filter label!=com.143.preview.infrastructure",
				"image prune -af --filter until=24h",
				"builder prune -af --filter until=24h",
			},
		},
		{
			name: "opt-in worker volume prune preserves managed data",
			role: "worker", enabled: "1", volumePrune: "1",
			want: []string{
				"container prune -f --filter until=24h --filter label!=com.143.preview.infrastructure",
				"image prune -af --filter until=24h",
				"builder prune -af --filter until=24h",
				"volume prune -f --filter label!=com.143.preview.infrastructure",
			},
		},
		{
			name: "app never enables worker volume prune",
			role: "app", enabled: "1", volumePrune: "1",
			want: []string{
				"container prune -f --filter until=24h --filter label!=com.143.preview.infrastructure",
				"image prune -af --filter until=24h",
				"builder prune -af --filter until=24h",
			},
		},
		{
			name: "app preserves preview ownership on a shared Docker host",
			role: "app", enabled: "1",
			want: []string{
				"container prune -f --filter until=24h --filter label!=com.143.preview.infrastructure",
				"image prune -af --filter until=24h",
				"builder prune -af --filter until=24h",
			},
		},
		{name: "operator can disable pruning", role: "worker", enabled: "0", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			capture := filepath.Join(dir, "calls")
			require.NoError(t, os.WriteFile(capture, nil, 0o600), "should initialize isolated command capture")
			stub := "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s\\n' \"$*\" >> \"$PREVIEW_PRUNE_CAPTURE\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o700), "should create a hermetic Docker stub")
			script, err := os.ReadFile("scripts/deploy.sh")
			require.NoError(t, err, "should load the production prune helper")
			prune := extractShellFunction(t, string(script), "prune_unused_docker_resources", "run_worker_session_deploy_guardrail")
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+prune+"\nprune_unused_docker_resources \"$PREVIEW_PRUNE_ROLE\"\n")
			// Supply a complete environment instead of mutating process globals;
			// the tests can run concurrently and cannot use production Docker.
			cmd.Env = []string{
				"PATH=" + dir + ":/usr/bin:/bin",
				"PREVIEW_PRUNE_CAPTURE=" + capture,
				"PREVIEW_PRUNE_ROLE=" + tt.role,
				"DEPLOY_DOCKER_PRUNE=" + tt.enabled,
				"DEPLOY_DOCKER_VOLUME_PRUNE=" + tt.volumePrune,
				"DOCKER_PRUNE_UNTIL=24h",
				"IMAGE_TAG=",
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "prune helper should execute safely: %s", out)
			calls, err := os.ReadFile(capture)
			require.NoError(t, err, "should read recorded Docker commands")
			actual := []string{}
			if text := strings.TrimSpace(string(calls)); text != "" {
				actual = strings.Split(text, "\n")
			}
			require.Equal(t, tt.want, actual, "routine pruning should preserve managed infrastructure and avoid volume pruning")
		})
	}
}
