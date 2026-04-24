package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlugForRepo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid org/repo", "assembledhq/143", "143"},
		{"no slash returns empty", "norepo", ""},
		{"empty input returns empty", "", ""},
		{"empty repo portion returns empty", "org/", ""},
		{"nested slashes collapse to hyphens", "org/repo/sub", "repo-sub"},
		{"leading slash treats left as empty", "/repo", "repo"},
		// Path-traversal guards: even though GitHub's repo-name grammar
		// excludes these, SlugForRepo must never produce a slug that
		// resolves "/home/sandbox/<slug>" to a parent directory.
		{"dot component is rejected", "org/.", ""},
		{"dotdot component is rejected", "org/..", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SlugForRepo(tc.in)
			if got != tc.want {
				t.Fatalf("SlugForRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_UsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("SANDBOX_IMAGE", "ghcr.io/example/custom:latest")
	t.Setenv("SANDBOX_CPU_LIMIT", "3.5")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "6144")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "24")

	cfg := DefaultSandboxConfig()

	require.Equal(t, "ghcr.io/example/custom:latest", cfg.Image, "DefaultSandboxConfig should use SANDBOX_IMAGE when provided")
	require.Equal(t, 3.5, cfg.CPULimit, "DefaultSandboxConfig should use SANDBOX_CPU_LIMIT when provided")
	require.Equal(t, 6144, cfg.MemoryLimitMB, "DefaultSandboxConfig should use SANDBOX_MEMORY_LIMIT_MB when provided")
	require.Equal(t, 24, cfg.DiskLimitGB, "DefaultSandboxConfig should use SANDBOX_DISK_LIMIT_GB when provided")
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_InvalidEnvironmentOverridesFallbackToDefaults(t *testing.T) {
	t.Setenv("SANDBOX_CPU_LIMIT", "not-a-number")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "-1")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "0")

	cfg := DefaultSandboxConfig()

	require.Equal(t, 2.0, cfg.CPULimit, "DefaultSandboxConfig should fall back to default CPU limit for invalid SANDBOX_CPU_LIMIT")
	require.Equal(t, 4096, cfg.MemoryLimitMB, "DefaultSandboxConfig should fall back to default memory limit for invalid SANDBOX_MEMORY_LIMIT_MB")
	require.Equal(t, 10, cfg.DiskLimitGB, "DefaultSandboxConfig should fall back to default disk limit for invalid SANDBOX_DISK_LIMIT_GB")
}
