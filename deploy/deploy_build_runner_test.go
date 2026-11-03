package deploy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type buildRunnerWorkflow struct {
	Jobs map[string]struct {
		RunsOn string `yaml:"runs-on"`
		Steps  []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readBuildRunnerWorkflow(t *testing.T, path string) buildRunnerWorkflow {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "runner boundary checks should read the actual workflow")
	var workflow buildRunnerWorkflow
	require.NoError(t, yaml.Unmarshal(raw, &workflow), "runner boundary checks should parse valid workflow YAML")
	return workflow
}

func TestDeployWorkflowKeepsRolloutOnGitHub(t *testing.T) {
	t.Parallel()

	tests := []struct {
		job      string
		expected string
	}{
		{job: "build", expected: "blacksmith-4vcpu-ubuntu-2404"},
		{job: "predeploy-latest", expected: "ubuntu-latest"},
		{job: "deploy", expected: "ubuntu-latest"},
	}
	for _, tt := range tests {
		t.Run(tt.job, func(t *testing.T) {
			t.Parallel()
			workflow := readBuildRunnerWorkflow(t, "../.github/workflows/deploy.yml")
			job, ok := workflow.Jobs[tt.job]
			require.True(t, ok, "deployment runner boundaries must include each expected job")
			require.Equal(t, tt.expected, job.RunsOn, "only image builds should use Blacksmith; rollout jobs must stay on GitHub runners")
		})
	}
}

func TestDeployBuildRetainsAuthenticatedRegistryCache(t *testing.T) {
	t.Parallel()

	workflow := readBuildRunnerWorkflow(t, "../.github/workflows/deploy.yml")
	job, ok := workflow.Jobs["build"]
	require.True(t, ok, "cache boundary checks should find the release image-build job")
	var actions []string
	var login, build map[string]string
	for _, step := range job.Steps {
		usesStickyBuilder := strings.HasPrefix(step.Uses, "useblacksmith/setup-docker-builder@") ||
			strings.HasPrefix(step.Uses, "useblacksmith/build-push-action@")
		require.False(t, usesStickyBuilder, "release builds must not use the shared sticky-disk builder")
		switch {
		case strings.HasPrefix(step.Uses, "docker/setup-buildx-action@"):
			actions = append(actions, "builder")
		case strings.HasPrefix(step.Uses, "docker/login-action@"):
			actions = append(actions, "login")
			login = step.With
		case strings.HasPrefix(step.Uses, "docker/build-push-action@"):
			actions = append(actions, "build")
			build = step.With
		}
	}

	require.Equal(t, []string{"builder", "login", "build"}, actions, "release builds must authenticate the standard builder before using registry caches")
	require.Equal(t, map[string]string{
		"registry": "ghcr.io",
		"username": "${{ github.actor }}",
		"password": "${{ secrets.GITHUB_TOKEN }}",
	}, login, "release cache access must authenticate to GHCR with the workflow token")
	require.Equal(t, map[string]string{
		"cache-from": "type=registry,ref=${{ matrix.image }}:buildcache",
		"cache-to":   "type=registry,ref=${{ matrix.image }}:buildcache,mode=max",
	}, map[string]string{
		"cache-from": build["cache-from"],
		"cache-to":   build["cache-to"],
	}, "release builds must retain their authenticated per-image registry cache")
}
