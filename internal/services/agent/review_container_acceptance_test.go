package agent_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These are deliberately synthetic, public fixture values, never credentials.
const acceptanceToken = "acceptance-fixture-token/+:not-a-credential"
const acceptanceRemote = "file:///home/sandbox/remote.git"

// TestReviewContainerAcceptance exercises production Docker exec, private
// stdin, image tools and snapshot transport. Ordinary suites leave it opt-in;
// a requested run fails preflight rather than silently skipping or using runc.
func TestReviewContainerAcceptance(t *testing.T) {
	t.Parallel()
	if os.Getenv("REVIEW_CONTAINER_ACCEPTANCE") != "1" {
		t.Skip("opt in with make test-review-container-acceptance")
	}
	require.Equal(t, "linux", runtime.GOOS, "requested acceptance must execute on Linux")
	image, networkName := os.Getenv("REVIEW_ACCEPTANCE_IMAGE"), os.Getenv("REVIEW_ACCEPTANCE_NETWORK")
	require.NotEmpty(t, image, "requested acceptance requires an explicit sandbox image")
	require.NotEmpty(t, networkName, "requested acceptance requires a scoped Docker network")
	require.Equal(t, "runsc", os.Getenv("REVIEW_ACCEPTANCE_RUNTIME"), "acceptance must exercise the production gVisor runtime")
	require.Regexp(t, `^(?:[0-9a-f]{40}|[0-9a-f]{64})$`, os.Getenv("REVIEW_ACCEPTANCE_SOURCE_SHA"), "evidence must identify the exact source revision")
	require.NotEmpty(t, os.Getenv("REVIEW_ACCEPTANCE_RUNSC_VERSION"), "evidence must record the actual gVisor version")
	require.NotEmpty(t, os.Getenv("REVIEW_ACCEPTANCE_ARTIFACT_DIR"), "requested acceptance must retain bounded evidence")
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err, "real Docker client should initialize")
	t.Cleanup(func() { require.NoError(t, cli.Close(), "Docker client should close after acceptance") })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := cli.Info(ctx)
	require.NoError(t, err, "requested acceptance requires a reachable Docker daemon")
	_, present := info.Runtimes["runsc"]
	require.True(t, present, "requested acceptance requires the registered runsc runtime")
	inspectedImage, err := cli.ImageInspect(ctx, image)
	require.NoError(t, err, "acceptance must use the locally built sandbox image")
	require.Equal(t, "linux", inspectedImage.Os, "sandbox image must target Linux")
	require.Equal(t, os.Getenv("REVIEW_ACCEPTANCE_SOURCE_SHA"), inspectedImage.Config.Labels["org.opencontainers.image.revision"], "built image must bind the exact dispatched source revision")
	_, err = cli.NetworkInspect(ctx, networkName, network.InspectOptions{})
	require.NoError(t, err, "acceptance requires its existing scoped network")

	tests := []struct{ name string }{
		{name: "cold"}, {name: "warm"}, {name: "snapshot_restore"},
		{name: "original_budget"}, {name: "expired_verification"},
		{name: "unsafe_git_config"}, {name: "corrupt_pack"}, {name: "recipe_failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAcceptanceFixture(t, tt.name)
			var logs acceptanceLogBuffer
			observed := &acceptanceDockerClient{DockerClient: cli}
			provider := &acceptanceProvider{
				DockerProvider: providers.NewDockerProvider(observed, zerolog.New(&logs).Level(zerolog.DebugLevel), providers.WithRuntime("runsc"), providers.WithNetwork(networkName)),
				client:         cli, image: image, network: networkName, fixture: fixture, mode: tt.name,
			}
			tokens := &acceptanceTokens{}
			orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
				Provider: provider, Repositories: acceptanceRepoStore{}, GitHub: tokens,
				Env: agent.NewAgentEnv(agent.AgentEnvDeps{}), Logger: zerolog.New(&logs),
				SandboxCapacity: agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: acceptanceCapacityCounter{}, MaxActive: 2, Logger: zerolog.Nop()}),
			})
			session := fixture.session(t)
			report := map[string]any{
				"scenario": tt.name, "source_sha": os.Getenv("REVIEW_ACCEPTANCE_SOURCE_SHA"),
				"image_id": inspectedImage.ID, "image_os": inspectedImage.Os, "image_architecture": inspectedImage.Architecture,
				"docker_server_version": info.ServerVersion, "runsc_version": os.Getenv("REVIEW_ACCEPTANCE_RUNSC_VERSION"), "runtime": "runsc", "cpu_limit": 2, "memory_limit_mb": 1024,
				"fixture_base": fixture.base, "fixture_head": fixture.head, "fixture_merge_base": fixture.mergeBase,
				"limits": []string{"file transport does not prove live HTTPS authentication", "synthetic dependencies do not accept repository recipes", "synthetic size does not establish largest-repository cost", "disk quota and worker egress policy are not accepted here"},
			}
			t.Cleanup(func() {
				credentialDetected := logs.CredentialDetected()
				report["passed"] = !t.Failed() && !credentialDetected
				report["provider_log_credential_detected"] = credentialDetected
				report["installation_token_requests"] = tokens.calls.Load()
				report["filtered_clone_missing_blobs"] = provider.filteredMissing.Load()
				report["real_snapshot_bytes"] = provider.snapshotBytes.Load()
				report["created_containers"] = provider.created.Load()
				report["destroyed_containers"] = provider.destroyed.Load()
				report["recipe_process_cleanup_checked"] = provider.cleanupChecked.Load()
				report["recipe_process_cleanup_passed"] = provider.cleanupPassed.Load()
				report["preparation_deadlines"] = provider.deadlines()
				report["sdk_preparation_deadlines"] = observed.deadlines()
				report["image_tool_versions"] = provider.versions()
				report["configured_fault_injections"] = provider.faultInjections.Load()
				report["configured_recipe_failure_reached"] = provider.recipeFailureReached.Load()
				report["observation_failure"] = provider.observationDiagnostic()
				report["fault_injection_evidence"] = provider.faultDiagnostic()
				report["process_cleanup_evidence"] = provider.cleanupEvidence()
				writeAcceptanceArtifact(t, tt.name, report, logs.String())
				require.False(t, credentialDetected, "the complete provider log stream through container cleanup must exclude synthetic private input")
			})
			t.Cleanup(func() { provider.cleanup(t) })
			preparationContext := jobctx.WithLockToken(context.Background(), uuid.New())
			var expectedDeadline time.Time
			if tt.name == "original_budget" {
				createdAt := time.Now().Add(-agent.CodeReviewWorkspacePreparationTimeout + 25*time.Second)
				expectedDeadline = createdAt.Add(agent.CodeReviewWorkspacePreparationTimeout)
				preparationContext = jobctx.WithJobCreatedAt(preparationContext, createdAt)
			}
			started := time.Now()
			sandbox, prepareErr := orchestrator.PrepareCodeReviewWorkspace(preparationContext, session, fixture.head)
			report["cold_elapsed_ms"] = time.Since(started).Milliseconds()
			if prepareErr != nil {
				report["prepare_error_causes"] = acceptanceSafeDiagnostic(acceptanceErrorCauses(prepareErr))
				var preparationError *agent.CodeReviewWorkspacePreparationError
				if errors.As(prepareErr, &preparationError) {
					report["prepare_error_stage"] = preparationError.Stage
				}
			}
			if tt.name == "original_budget" || tt.name == "unsafe_git_config" || tt.name == "corrupt_pack" || tt.name == "recipe_failure" {
				require.Positive(t, provider.filteredMissing.Load(), "rejection must follow successful filtered clone observation")
				var preparationError *agent.CodeReviewWorkspacePreparationError
				require.ErrorAs(t, prepareErr, &preparationError, "rejection must identify the production preparation stage")
				require.Error(t, prepareErr, "rejected cold preparation must report a visible setup failure")
				require.Nil(t, sandbox, "failed cold preparation must not transfer a sandbox")
				require.Equal(t, int64(1), provider.destroyed.Load(), "failed preparation must destroy the unpublished container")
				if tt.name == "original_budget" {
					require.ErrorIs(t, prepareErr, context.DeadlineExceeded, "queued preparation must use the original eight-minute deadline")
					require.Equal(t, agent.CodeReviewPreparationStageDependencySetup, preparationError.Stage, "budget fixture must reach its long-running dependency recipe")
					require.True(t, provider.cleanupChecked.Load(), "timeout must reach the real long-running recipe")
					require.True(t, provider.cleanupPassed.Load(), "recipe process group must stop before container destruction")
					require.GreaterOrEqual(t, len(provider.deadlines()), 4, "cold timeout must traverse multiple preparation stages")
					require.Equal(t, len(provider.deadlines()), len(observed.deadlines()), "every actual preparation input must have one attributed SDK execution")
					for _, deadline := range append(provider.deadlines(), observed.deadlines()...) {
						require.InDelta(t, float64(expectedDeadline.UnixNano())/1e9, deadline, 0.001, "all preparation stages must share the original deadline")
					}
					require.Less(t, time.Since(started), 40*time.Second, "cleanup must stay bounded after original budget expiry")
				} else if tt.name == "corrupt_pack" {
					require.Equal(t, int64(1), provider.faultInjections.Load(), "pack corruption must actually be injected after cloning")
					require.Equal(t, agent.CodeReviewPreparationStageHydration, preparationError.Stage, "present corrupt objects must be rejected before captured source hydration")
					require.ErrorIs(t, prepareErr, agent.ErrCodeReviewSourceIntegrity, "present pack corruption must be terminal source integrity failure")
				} else {
					require.NotErrorIs(t, prepareErr, agent.ErrCodeReviewSourceIntegrity, "configuration or recipe failure must not claim source corruption")
					require.Equal(t, agent.CodeReviewPreparationStageDependencySetup, preparationError.Stage, "configuration and recipe fixtures must reach their intended preparation stage")
					if tt.name == "unsafe_git_config" {
						require.Equal(t, int64(1), provider.faultInjections.Load(), "unsafe configuration must actually be injected after cloning")
						require.Contains(t, acceptanceErrorCauses(prepareErr), "review Git configuration is unsafe for authenticated preparation", "configuration rejection must identify the trusted configuration guard")
					} else {
						require.True(t, provider.recipeFailureReached.Load(), "negative recipe case must actually enter its failing command")
						require.Contains(t, acceptanceErrorCauses(prepareErr), "review workspace preparation command failed (exit=125)", "negative recipe case must reject its explicit failure rather than an earlier observer error")
					}
				}
				provider.assertNoCredentials(t, observed, &logs)
				return
			}
			require.NoError(t, prepareErr, "cold acceptance must prepare the exact captured workspace")
			require.Positive(t, provider.filteredMissing.Load(), "filtered clone must initially omit source blobs")
			assertAcceptanceSource(t, provider, sandbox, fixture)
			initialReceipt := acceptanceRead(t, provider, sandbox, ".git/143-review-evidence.json")
			var receipt struct {
				Revision struct {
					Version    int    `json:"version"`
					BaseSHA    string `json:"base_sha"`
					HeadSHA    string `json:"head_sha"`
					ConfigHash string `json:"config_hash"`
				} `json:"revision"`
				Evidence json.RawMessage `json:"evidence"`
			}
			require.NoError(t, json.Unmarshal(initialReceipt, &receipt), "real receipt must decode")
			require.Equal(t, 2, receipt.Revision.Version, "receipt must use the current exact manifest format")
			require.Equal(t, fixture.base, receipt.Revision.BaseSHA, "receipt must bind the captured base")
			require.Equal(t, fixture.head, receipt.Revision.HeadSHA, "receipt must bind the captured head")
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(fixture.config)), receipt.Revision.ConfigHash, "receipt must bind the exact recipe bytes")
			report["receipt_sha256"] = fmt.Sprintf("%x", sha256.Sum256(initialReceipt))
			require.Equal(t, "setup\n", string(acceptanceRead(t, provider, sandbox, ".143/setup-count")), "cold preparation must run real installers once")
			require.Equal(t, "verify\n", string(acceptanceRead(t, provider, sandbox, ".143/verify-count")), "cold preparation must run its real verifier once")
			requests := tokens.calls.Load()
			require.Equal(t, int64(1), requests, "cold clone should request one synthetic installation input")
			if tt.name == "snapshot_restore" {
				reader, snapshotErr := provider.Snapshot(context.Background(), sandbox)
				require.NoError(t, snapshotErr, "real Docker snapshot must start")
				archive, readErr := io.ReadAll(io.LimitReader(reader, 8*1024*1024+1))
				closeErr := reader.Close()
				require.NoError(t, readErr, "real snapshot archive must finish successfully")
				require.NoError(t, closeErr, "real snapshot stream must close")
				require.LessOrEqual(t, len(archive), 8*1024*1024, "fixture snapshot must remain bounded")
				provider.snapshotBytes.Store(int64(len(archive)))
				originalID := sandbox.ID
				require.NoError(t, provider.Destroy(context.Background(), sandbox), "original container must be removed before restore")
				cfg := agent.DefaultSandboxConfig()
				cfg.WorkDir, cfg.HomeDir, cfg.Env = sandbox.WorkDir, sandbox.HomeDir, sandbox.Env
				sandbox, err = provider.Create(context.Background(), cfg)
				require.NoError(t, err, "restore must create a separate real container")
				require.NotEqual(t, originalID, sandbox.ID, "snapshot restore must never reuse the original filesystem")
				restoreCtx, restoreCancel := context.WithTimeout(context.Background(), time.Minute)
				require.NoError(t, provider.RestoreForReviewPreparation(restoreCtx, sandbox, bytes.NewReader(archive)), "production review restore transport must extract the archive")
				restoreCancel()
				require.Equal(t, initialReceipt, acceptanceRead(t, provider, sandbox, ".git/143-review-evidence.json"), "restore must preserve the receipt bytes")
			}
			if tt.name != "cold" {
				reuseContext := context.Background()
				if tt.name == "expired_verification" {
					reuseContext = jobctx.WithJobCreatedAt(reuseContext, time.Now().Add(-agent.CodeReviewWorkspacePreparationTimeout-time.Second))
				}
				started = time.Now()
				require.NoError(t, agent.EnsureCodeReviewWorkspaceEvidenceForTest(reuseContext, orchestrator, session, sandbox), "complete warm or restored evidence must validate through the production engine")
				report["reuse_elapsed_ms"] = time.Since(started).Milliseconds()
				require.Equal(t, requests, tokens.calls.Load(), "warm, restored and expired verification must request no installation token")
				require.Equal(t, "setup\n", string(acceptanceRead(t, provider, sandbox, ".143/setup-count")), "reuse must never reinstall complete dependencies")
				wantVerifications := "verify\nverify\n"
				if tt.name == "expired_verification" {
					wantVerifications = "verify\n"
				}
				require.Equal(t, wantVerifications, string(acceptanceRead(t, provider, sandbox, ".143/verify-count")), "expired verification must not execute arbitrary recipe verifiers")
				require.Equal(t, initialReceipt, acceptanceRead(t, provider, sandbox, ".git/143-review-evidence.json"), "reuse must not replace the exact receipt")
				if tt.name == "expired_verification" {
					require.Equal(t, int32(1), provider.verifyOnlyCount.Load(), "expired reuse must select platform-only verification")
					require.Less(t, time.Since(started), agent.CodeReviewWorkspaceVerificationTimeout+5*time.Second, "expired verification must respect its separate bounded minute")
				}
				assertAcceptanceSource(t, provider, sandbox, fixture)
			}
			provider.assertNoCredentials(t, observed, &logs)
			for _, name := range []string{".git/config", ".git/143-review-evidence.json", ".143/proc-count"} {
				assertAcceptanceSecretAbsent(t, acceptanceRead(t, provider, sandbox, name), "repository metadata and recipe ancestry report")
			}
			report["recipe_proc_checks"] = strings.Count(string(acceptanceRead(t, provider, sandbox, ".143/proc-count")), "checked\n")
		})
	}
}

// The wrapper forwards every Docker SDK request unchanged and observes only
// actual argv/environment. Private stdin is inspected by a separate reader tee
// for deadline metadata; neither stdin nor raw exec scripts become artifacts.
type acceptanceDockerClient struct {
	providers.DockerClient
	mu                   sync.Mutex
	executions           []container.ExecOptions
	preparationDeadlines []float64
}

func (c *acceptanceDockerClient) ContainerExecCreate(ctx context.Context, id string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	c.mu.Lock()
	c.executions = append(c.executions, options)
	if deadline, ok := acceptanceMarkedPreparationDeadline(ctx); ok {
		c.preparationDeadlines = append(c.preparationDeadlines, deadline)
	}
	c.mu.Unlock()
	return c.DockerClient.ContainerExecCreate(ctx, id, options)
}

func (c *acceptanceDockerClient) deadlines() []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]float64(nil), c.preparationDeadlines...)
}

// Use the real client signature without modifying resource/runtime requests.
// Container configuration is verified via actual ContainerInspect below.

type acceptanceProvider struct {
	*providers.DockerProvider
	client                                             *client.Client
	image, network, mode                               string
	fixture                                            acceptanceFixture
	mu                                                 sync.Mutex
	containers                                         map[string]*agent.Sandbox
	creationEnvironments                               [][]string
	creationRuntimes                                   []string
	toolVersions                                       map[string]string
	faultEvidence                                      string
	processCleanupEvidence                             json.RawMessage
	observationFailure                                 string
	faultInjections                                    atomic.Int64
	recipeFailureReached                               atomic.Bool
	preparationDeadlines                               []float64
	created, destroyed, filteredMissing, snapshotBytes atomic.Int64
	verifyOnlyCount                                    atomic.Int32
	cleanupChecked, cleanupPassed                      atomic.Bool
}

func (p *acceptanceProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	cfg.Image, cfg.NetworkName, cfg.CPULimit, cfg.MemoryLimitMB, cfg.DiskLimitGB = p.image, p.network, 2, 1024, 0
	sandbox, err := p.DockerProvider.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p.created.Add(1)
	inspection, inspectErr := p.client.ContainerInspect(ctx, sandbox.ID)
	if inspectErr != nil || inspection.Config == nil || inspection.HostConfig == nil {
		cleanupErr := p.DockerProvider.Destroy(context.WithoutCancel(ctx), sandbox)
		return nil, errors.Join(errors.New("actual sandbox creation inspection failed"), inspectErr, cleanupErr)
	}
	p.mu.Lock()
	p.creationEnvironments = append(p.creationEnvironments, append([]string(nil), inspection.Config.Env...))
	p.creationRuntimes = append(p.creationRuntimes, inspection.HostConfig.Runtime)
	p.mu.Unlock()
	p.mu.Lock()
	if p.containers == nil {
		p.containers = make(map[string]*agent.Sandbox)
	}
	p.containers[sandbox.ID] = sandbox
	p.mu.Unlock()
	var versionOutput, versionError bytes.Buffer
	versionCode, versionErr := p.DockerProvider.Exec(ctx, sandbox, `/usr/bin/python3 -I -S -c 'import json,subprocess; tools={"git":["/usr/bin/git","--version"],"go":["/usr/local/go/bin/go","version"],"node":["/usr/bin/node","--version"],"npm":["/usr/bin/npm","--version"],"python":["/usr/bin/python3","--version"]}; print(json.dumps({name:subprocess.check_output(command,text=True).strip() for name,command in tools.items()}))'`, &versionOutput, &versionError)
	if versionErr != nil || versionCode != 0 {
		return nil, errors.Join(errors.New("actual image tool version probe failed"), versionErr)
	}
	var versions map[string]string
	if decodeErr := json.Unmarshal(versionOutput.Bytes(), &versions); decodeErr != nil {
		return nil, fmt.Errorf("decode actual image tools: %w", decodeErr)
	}
	p.mu.Lock()
	p.toolVersions = versions
	p.mu.Unlock()
	if err := p.client.CopyToContainer(ctx, sandbox.ID, "/home/sandbox", bytes.NewReader(p.fixture.archive), container.CopyToContainerOptions{}); err != nil {
		cleanupErr := p.Destroy(context.WithoutCancel(ctx), sandbox)
		return nil, errors.Join(fmt.Errorf("copy synthetic bare fixture: %w", err), cleanupErr)
	}
	return sandbox, nil
}
func (p *acceptanceProvider) Destroy(ctx context.Context, sandbox *agent.Sandbox) error {
	if p.mode == "recipe_failure" {
		var output bytes.Buffer
		code, inspectionErr := p.DockerProvider.Exec(ctx, sandbox, "/bin/cat .143/recipe-count", &output, io.Discard)
		p.recipeFailureReached.Store(inspectionErr == nil && code == 0 && output.String() == "failed\n")
	}
	if p.mode == "original_budget" {
		var output, inspectionError bytes.Buffer
		command := "/usr/bin/python3 -I -S -c '" + strings.ReplaceAll(acceptanceProcessCleanupPython, "'", "'\"'\"'") + "'"
		code, inspectErr := p.DockerProvider.Exec(ctx, sandbox, command, &output, &inspectionError)
		var observation acceptanceCleanupObservation
		decodeErr := json.Unmarshal(output.Bytes(), &observation)
		if inspectErr == nil && decodeErr == nil && observation.PID > 1 && observation.ProcessGroup > 1 && observation.Target.PID == observation.PID {
			p.cleanupChecked.Store(true)
			stopped := acceptanceTerminalProcessState(observation.Target.State)
			for _, member := range observation.Members {
				stopped = stopped && acceptanceTerminalProcessState(member.State)
			}
			p.cleanupPassed.Store(code == 0 && stopped)
			p.mu.Lock()
			p.processCleanupEvidence = append(json.RawMessage(nil), output.Bytes()...)
			p.mu.Unlock()
		} else {
			diagnostic := acceptanceSafeDiagnostic(fmt.Sprintf("cleanup observation failed: exit=%d stderr=%s transport=%v decode=%v", code, inspectionError.String(), inspectErr, decodeErr))
			encoded, encodeErr := json.Marshal(map[string]string{"error": diagnostic})
			if encodeErr == nil {
				p.mu.Lock()
				p.processCleanupEvidence = encoded
				p.mu.Unlock()
			}
		}
	}
	if err := p.DockerProvider.Destroy(ctx, sandbox); err != nil {
		return err
	}
	p.destroyed.Add(1)
	p.mu.Lock()
	delete(p.containers, sandbox.ID)
	p.mu.Unlock()
	return nil
}
func (p *acceptanceProvider) ExecWithStdinForReviewPreparation(ctx context.Context, sandbox *agent.Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	var captured bytes.Buffer
	// This observer-only marker preserves the caller's deadline and cancellation.
	// Cleanup and version probes retain their original unmarked contexts.
	preparationCtx := context.WithValue(ctx, acceptancePreparationExecContextKey{}, true)
	code, err := p.DockerProvider.ExecWithStdinForReviewPreparation(preparationCtx, sandbox, command, io.TeeReader(input, &captured), stdout, stderr)
	var envelope struct {
		Payload struct {
			CloneDestination string  `json:"clone_destination"`
			Deadline         float64 `json:"deadline_unix"`
			VerifyOnly       bool    `json:"verify_only"`
		} `json:"payload"`
	}
	if decodeErr := json.Unmarshal(captured.Bytes(), &envelope); decodeErr == nil {
		p.mu.Lock()
		p.preparationDeadlines = append(p.preparationDeadlines, envelope.Payload.Deadline)
		p.mu.Unlock()
		if envelope.Payload.VerifyOnly {
			p.verifyOnlyCount.Add(1)
		}
		if envelope.Payload.CloneDestination != "" && err == nil && code == 0 {
			clone := acceptanceObservationSandbox(sandbox, envelope.Payload.CloneDestination)
			var missing, probeError bytes.Buffer
			probeCode, probeErr := p.DockerProvider.Exec(ctx, clone, "GIT_NO_LAZY_FETCH=1 /usr/bin/git -c core.hooksPath=/dev/null rev-list --objects --all --missing=print", &missing, &probeError)
			if probeErr != nil || probeCode != 0 {
				diagnostic := acceptanceSafeDiagnostic(fmt.Sprintf("filtered clone observation failed: exit=%d stderr=%s transport=%v", probeCode, probeError.String(), probeErr))
				p.mu.Lock()
				p.observationFailure = diagnostic
				p.mu.Unlock()
				return 1, errors.New(diagnostic)
			}
			for _, line := range strings.Split(missing.String(), "\n") {
				if strings.HasPrefix(line, "?") {
					p.filteredMissing.Add(1)
				}
			}
			if p.mode == "unsafe_git_config" || p.mode == "corrupt_pack" {
				mutation := "/usr/bin/git config core.fsmonitor '.143/never-run'"
				if p.mode == "corrupt_pack" {
					mutation = "/usr/bin/python3 -I -S -c 'import json,pathlib,stat; p=next(pathlib.Path(\".git/objects/pack\").glob(\"*.pack\")); info=p.stat(); print(json.dumps({\"path\":str(p),\"mode_before\":oct(stat.S_IMODE(info.st_mode)),\"owner_uid\":info.st_uid,\"pack_bytes\":info.st_size}),flush=True); p.chmod(stat.S_IMODE(info.st_mode)|stat.S_IWUSR); b=bytearray(p.read_bytes()); b[-1] ^= 1; p.write_bytes(b)'"
				}
				var mutationOutput, mutationError bytes.Buffer
				mutationCode, mutationErr := p.DockerProvider.Exec(ctx, clone, mutation, &mutationOutput, &mutationError)
				diagnostic := acceptanceSafeDiagnostic(fmt.Sprintf("synthetic fixture mutation: exit=%d stdout=%s stderr=%s transport=%v", mutationCode, mutationOutput.String(), mutationError.String(), mutationErr))
				p.mu.Lock()
				p.faultEvidence = diagnostic
				p.mu.Unlock()
				if mutationErr != nil || mutationCode != 0 {
					return 1, errors.New(diagnostic)
				}
				p.faultInjections.Add(1)
			}
		}
	}
	return code, err
}
func (p *acceptanceProvider) deadlines() []float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]float64(nil), p.preparationDeadlines...)
}
func (p *acceptanceProvider) versions() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]string, len(p.toolVersions))
	for name, value := range p.toolVersions {
		result[name] = value
	}
	return result
}
func (p *acceptanceProvider) cleanup(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	sandboxes := make([]*agent.Sandbox, 0, len(p.containers))
	for _, sandbox := range p.containers {
		sandboxes = append(sandboxes, sandbox)
	}
	p.mu.Unlock()
	for _, sandbox := range sandboxes {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := p.Destroy(ctx, sandbox)
		cancel()
		require.NoError(t, err, "isolated acceptance sandbox should be destroyed")
	}
}
func (p *acceptanceProvider) assertNoCredentials(t *testing.T, observed *acceptanceDockerClient, logs *acceptanceLogBuffer) {
	t.Helper()
	require.False(t, logs.CredentialDetected(), "the entire provider log stream must exclude synthetic private input, including bytes beyond the artifact cap")
	assertAcceptanceSecretAbsent(t, []byte(logs.String()), "retained provider log artifact")
	observed.mu.Lock()
	defer observed.mu.Unlock()
	for _, execution := range observed.executions {
		assertAcceptanceSecretAbsent(t, []byte(strings.Join(execution.Cmd, "\x00")+strings.Join(execution.Env, "\x00")), "actual Docker exec argv/environment")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, environment := range p.creationEnvironments {
		assertAcceptanceSecretAbsent(t, []byte(strings.Join(environment, "\x00")), "inspected creation environments including destroyed workspaces")
	}
	for _, actualRuntime := range p.creationRuntimes {
		require.Equal(t, "runsc", actualRuntime, "every actual created container must use gVisor")
	}
	for id := range p.containers {
		inspection, err := p.client.ContainerInspect(context.Background(), id)
		require.NoError(t, err, "credential assertion must inspect the actual creation environment")
		require.NotNil(t, inspection.Config, "actual Docker configuration must exist")
		assertAcceptanceSecretAbsent(t, []byte(strings.Join(inspection.Config.Env, "\x00")), "actual Docker creation environment")
		require.Equal(t, "runsc", inspection.HostConfig.Runtime, "actual container must use gVisor")
		require.Equal(t, int64(1024*1024*1024), inspection.HostConfig.Memory, "actual memory constraint must match acceptance evidence")
	}
}

type acceptanceFixture struct {
	archive, config       []byte
	base, head, mergeBase string
}

func newAcceptanceFixture(t *testing.T, mode string) acceptanceFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(t, os.MkdirAll(source, 0o755), "fixture directory should exist")
	files := map[string]string{
		".gitignore": "node_modules/\nvendor/\n.143/*-count\n.143/hang.pid\n",
		"source.txt": "merge-base source\n", "base-only.txt": "merge-base file\n",
		"go.mod":                      "module synthetic.invalid/acceptance\n\ngo 1.26\n\nrequire synthetic.invalid/dependency v0.0.0\nreplace synthetic.invalid/dependency => ./go-dependency\n",
		"main.go":                     "package main\nimport _ \"synthetic.invalid/dependency\"\nfunc main() {}\n",
		"go-dependency/go.mod":        "module synthetic.invalid/dependency\n\ngo 1.26\n",
		"go-dependency/dependency.go": "package dependency\nconst Source = \"synthetic Go source\"\n",
		"js-dependency/package.json":  `{"name":"synthetic-dependency","version":"1.0.0","main":"index.js"}`,
		"js-dependency/index.js":      "module.exports = 'synthetic JavaScript source';\n",
		"package.json":                `{"name":"acceptance","version":"1.0.0","dependencies":{"synthetic-dependency":"file:./js-dependency"}}`,
		"package-lock.json":           `{"name":"acceptance","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"acceptance","version":"1.0.0","dependencies":{"synthetic-dependency":"file:./js-dependency"}},"js-dependency":{"name":"synthetic-dependency","version":"1.0.0"},"node_modules/synthetic-dependency":{"resolved":"js-dependency","link":true}}}`,
		".143/check-private.py":       acceptanceProcInspection,
	}
	commands := []string{"/usr/bin/python3 -I -S .143/check-private.py", "/usr/local/go/bin/go mod vendor", "/usr/bin/npm ci --ignore-scripts --no-audit --no-fund", "printf 'setup\\n' >> .143/setup-count"}
	if mode == "original_budget" {
		commands = []string{"/usr/bin/python3 -I -S .143/check-private.py", "/usr/bin/python3 -I -S -c 'import json,os,pathlib,time; pathlib.Path(\".143/hang.pid\").write_text(json.dumps({\"pid\":os.getpid(),\"process_group\":os.getpgrp()})); time.sleep(90)'"}
	}
	if mode == "recipe_failure" {
		commands = []string{"/usr/bin/python3 -I -S .143/check-private.py", "printf 'failed\\n' >> .143/recipe-count", "exit 125"}
	}
	configuration := map[string]any{"review": map[string]any{"commands": commands, "verify_commands": []string{"/usr/bin/python3 -I -S .143/check-private.py", "test -f vendor/synthetic.invalid/dependency/dependency.go && test -f node_modules/synthetic-dependency/index.js", "printf 'verify\\n' >> .143/verify-count"}, "evidence": []map[string]string{{"path": "vendor/synthetic.invalid/dependency", "description": "Synthetic offline Go dependency source", "category": "go_dependencies"}, {"path": "node_modules/synthetic-dependency", "description": "Synthetic locked JavaScript dependency source", "category": "javascript_dependencies"}}}}
	encoded, err := json.Marshal(configuration)
	require.NoError(t, err, "fixture recipe should encode")
	files[".143/config.json"] = string(encoded)
	for name, contents := range files {
		target := filepath.Join(source, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755), "fixture parent should exist")
		require.NoError(t, os.WriteFile(target, []byte(contents), 0o644), "fixture source should be written")
	}
	git := func(args ...string) string {
		cmd := exec.Command("/usr/bin/git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", source}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=Acceptance", "GIT_AUTHOR_EMAIL=acceptance@example.invalid", "GIT_COMMITTER_NAME=Acceptance", "GIT_COMMITTER_EMAIL=acceptance@example.invalid")
		output, commandErr := cmd.CombinedOutput()
		require.NoError(t, commandErr, "synthetic Git fixture should build: %s", output)
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet", "--initial-branch=main")
	git("add", ".")
	git("commit", "--quiet", "-m", "synthetic merge base")
	mergeBase := git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(source, "base-only.txt"), []byte("captured base source\n"), 0o644), "base branch should diverge")
	git("add", ".")
	git("commit", "--quiet", "-m", "synthetic base")
	base := git("rev-parse", "HEAD")
	git("checkout", "--quiet", "-b", "review", mergeBase)
	require.NoError(t, os.WriteFile(filepath.Join(source, "source.txt"), []byte("captured head source\n"), 0o644), "head branch should diverge")
	git("add", ".")
	git("commit", "--quiet", "-m", "synthetic head")
	head := git("rev-parse", "HEAD")
	remote := filepath.Join(root, "remote.git")
	git("clone", "--quiet", "--bare", source, remote)
	for _, args := range [][]string{{"symbolic-ref", "HEAD", "refs/heads/main"}, {"update-ref", "refs/pull/42/head", head}, {"config", "uploadpack.allowFilter", "true"}, {"config", "uploadpack.allowAnySHA1InWant", "true"}} {
		cmd := exec.Command("/usr/bin/git", append([]string{"--git-dir=" + remote}, args...)...)
		output, commandErr := cmd.CombinedOutput()
		require.NoError(t, commandErr, "synthetic remote must support filtered pinned fetches: %s", output)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	err = filepath.WalkDir(remote, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		relative, relErr := filepath.Rel(root, name)
		if relErr != nil {
			return relErr
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name, header.Uid, header.Gid = filepath.ToSlash(relative), 1000, 1000
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, readErr := os.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		_, writeErr := writer.Write(contents)
		return writeErr
	})
	require.NoError(t, err, "synthetic remote archive should encode")
	require.NoError(t, writer.Close(), "synthetic archive should finish")
	return acceptanceFixture{archive: archive.Bytes(), config: encoded, base: base, head: head, mergeBase: mergeBase}
}
func (f acceptanceFixture) session(t *testing.T) *models.Session {
	t.Helper()
	repoID := uuid.New()
	revision, err := json.Marshal(map[string]any{"kind": "code_review", "github_pr_number": 42, "base_sha": f.base, "head_sha": f.head})
	require.NoError(t, err, "captured review revision should encode")
	return &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview, AgentType: models.AgentTypeCodex, RepositoryID: &repoID, RevisionContext: revision}
}

type acceptanceRepoStore struct{}

func (acceptanceRepoStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return models.Repository{FullName: "synthetic/repo", CloneURL: acceptanceRemote, DefaultBranch: "main"}, nil
}

type acceptanceTokens struct{ calls atomic.Int64 }

func (p *acceptanceTokens) GetInstallationToken(context.Context, int64) (string, error) {
	p.calls.Add(1)
	return acceptanceToken, nil
}

type acceptanceCapacityCounter struct{}

func (acceptanceCapacityCounter) CountLiveSandboxes(context.Context) (int, error) { return 0, nil }
func acceptanceRead(t *testing.T, p *acceptanceProvider, sandbox *agent.Sandbox, relative string) []byte {
	t.Helper()
	value, err := p.ReadFile(context.Background(), sandbox, filepath.Join(sandbox.WorkDir, relative))
	require.NoError(t, err, "acceptance evidence should be readable: %s", relative)
	return value
}
func assertAcceptanceSource(t *testing.T, p *acceptanceProvider, sandbox *agent.Sandbox, fixture acceptanceFixture) {
	t.Helper()
	tests := []struct{ revision, name, expected string }{{fixture.head, "source.txt", "captured head source\n"}, {fixture.base, "base-only.txt", "captured base source\n"}, {fixture.mergeBase, "source.txt", "merge-base source\n"}}
	for _, tt := range tests {
		var output, stderr bytes.Buffer
		code, err := p.Exec(context.Background(), sandbox, "GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 /usr/bin/git -c credential.helper= -c core.hooksPath=/dev/null show "+tt.revision+":"+tt.name, &output, &stderr)
		require.NoError(t, err, "pinned source must read with lazy hydration disabled")
		require.Equal(t, 0, code, "pinned source must exist locally: %s", stderr.String())
		require.Equal(t, tt.expected, output.String(), "captured head/base/merge-base contents must match exactly")
	}
}
func assertAcceptanceSecretAbsent(t *testing.T, value []byte, surface string) {
	t.Helper()
	for _, forbidden := range acceptanceCredentialForms() {
		require.NotContains(t, string(value), forbidden, "synthetic private input must not reach %s", surface)
	}
}

const acceptanceProviderLogArtifactLimit = 64 * 1024

// Retention and credential detection are independent. Scan every incoming byte
// and retain only the longest credential's boundary overlap between writes;
// the artifact still contains at most the first 64 KiB of provider logs.
type acceptanceLogBuffer struct {
	mu                 sync.Mutex
	buffer             bytes.Buffer
	needles            [][]byte
	scanTail           []byte
	maxScanTail        int
	credentialDetected bool
}

func (b *acceptanceLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.needles == nil {
		for _, form := range acceptanceCredentialForms() {
			b.needles = append(b.needles, []byte(form))
			b.maxScanTail = max(b.maxScanTail, len(form)-1)
		}
	}
	// A cross-write match can involve only the saved suffix and the incoming
	// prefix. Never concatenate the whole write into scanner-owned storage.
	prefixSize := min(len(value), b.maxScanTail)
	boundary := make([]byte, len(b.scanTail)+prefixSize)
	copy(boundary, b.scanTail)
	copy(boundary[len(b.scanTail):], value[:prefixSize])
	for _, needle := range b.needles {
		if bytes.Contains(value, needle) || bytes.Contains(boundary, needle) {
			b.credentialDetected = true
		}
	}
	if len(value) >= b.maxScanTail {
		b.scanTail = append(b.scanTail[:0], value[len(value)-b.maxScanTail:]...)
	} else {
		b.scanTail = append(b.scanTail[:0], boundary[max(0, len(boundary)-b.maxScanTail):]...)
	}
	if b.buffer.Len() < acceptanceProviderLogArtifactLimit {
		remaining := min(len(value), acceptanceProviderLogArtifactLimit-b.buffer.Len())
		if _, err := b.buffer.Write(value[:remaining]); err != nil {
			return 0, err
		}
	}
	return len(value), nil
}
func (b *acceptanceLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
func (b *acceptanceLogBuffer) CredentialDetected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.credentialDetected
}
func acceptanceCredentialForms() []string {
	return []string{acceptanceToken, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + acceptanceToken)), url.QueryEscape(acceptanceToken), url.PathEscape(acceptanceToken)}
}
func writeAcceptanceArtifact(t *testing.T, name string, report map[string]any, logs string) {
	t.Helper()
	root := os.Getenv("REVIEW_ACCEPTANCE_ARTIFACT_DIR")
	require.NoError(t, os.MkdirAll(root, 0o755), "artifact directory must exist")
	encoded, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err, "bounded evidence should encode")
	require.NoError(t, os.WriteFile(filepath.Join(root, name+".json"), encoded, 0o644), "acceptance evidence must persist even after failure")
	require.NoError(t, os.WriteFile(filepath.Join(root, name+".log"), []byte(logs), 0o644), "bounded provider logs must persist")
}

const acceptanceProcInspection = `
import base64, os, pathlib, urllib.parse
secret = "acceptance-fixture-token/+:not-a-credential"
needles = [secret.encode(), base64.b64encode(("x-access-token:" + secret).encode()), urllib.parse.quote(secret, safe="").encode()]
pid, checked = os.getpid(), 0
while pid > 1:
    proc = pathlib.Path("/proc") / str(pid)
    values = [(proc / "cmdline").read_bytes(), (proc / "environ").read_bytes()]
    for entry in (proc / "fd").iterdir():
        try:
            target = entry.resolve()
            if str(target).startswith("/home/sandbox/") and target.is_file() and target.stat().st_size < 65536:
                values.append(target.read_bytes())
        except (OSError, RuntimeError):
            pass
    if any(needle in value for needle in needles for value in values):
        raise RuntimeError("private input leaked into recipe ancestry")
    pid = int((proc / "stat").read_text().rsplit(")", 1)[1].split()[1])
    checked += 1
if checked == 0:
    raise RuntimeError("Linux recipe process ancestry was not inspected")
if os.read(0, 1):
    raise RuntimeError("repository child inherited private stdin")
with open(".143/proc-count", "a") as report:
    report.write("checked\n")
`

// Fixture construction is independently exercised in ordinary host suites;
// this verifies fixture contracts, never substitutes for container acceptance.
func TestReviewContainerAcceptanceFixture(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{{"cold"}, {"original_budget"}, {"recipe_failure"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAcceptanceFixture(t, tt.name)
			parsed, err := repoconfig.Parse(fixture.config)
			require.NoError(t, err, "synthetic recipe must obey the production schema")
			require.NotNil(t, parsed.Review, "fixture must declare review recipes")
			require.Equal(t, []string{"vendor/synthetic.invalid/dependency", "node_modules/synthetic-dependency"}, []string{parsed.Review.Evidence[0].Path, parsed.Review.Evidence[1].Path}, "fixture must map both dependency source roots")
			require.NotEqual(t, fixture.base, fixture.head, "captured branches must diverge")
			require.NotEqual(t, fixture.mergeBase, fixture.base, "base must follow the captured merge base")
			require.NotEqual(t, fixture.mergeBase, fixture.head, "head must follow the captured merge base")
			require.Positive(t, len(fixture.archive), "bare source transport must have actual Git objects")
			require.NotContains(t, string(fixture.config), acceptanceToken, "recipe configuration must not carry private authentication")
		})
	}
}

// The clone input sandbox is deliberately anchored outside the destination.
// Observations must rebind its owned Git environment as well as WorkingDir;
// otherwise Git can inspect the parent's nonexistent or unrelated .git.
func acceptanceObservationSandbox(original *agent.Sandbox, destination string) *agent.Sandbox {
	observed := *original
	observed.WorkDir = destination
	observed.Env = make(map[string]string, len(original.Env))
	for name, value := range original.Env {
		observed.Env[name] = value
	}
	gitDir := filepath.Join(destination, ".git")
	observed.Env["GIT_DIR"], observed.Env["GIT_COMMON_DIR"] = gitDir, gitDir
	observed.Env["GIT_WORK_TREE"] = destination
	observed.Env["GIT_OBJECT_DIRECTORY"] = filepath.Join(gitDir, "objects")
	observed.Env["GIT_INDEX_FILE"] = filepath.Join(gitDir, "index")
	return &observed
}
func acceptanceSafeDiagnostic(value string) string {
	for _, forbidden := range acceptanceCredentialForms() {
		value = strings.ReplaceAll(value, forbidden, "[redacted]")
	}
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}
func acceptanceErrorCauses(err error) string {
	var causes []string
	for err != nil {
		causes = append(causes, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(causes, "\n")
}
func (p *acceptanceProvider) observationDiagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.observationFailure
}

func TestReviewContainerAcceptanceObservationGitBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		createParent bool
	}{{name: "parent has no Git directory"}, {name: "parent has an unrelated repository", createParent: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			parent := filepath.Join(root, "parent")
			destination := filepath.Join(parent, "repo")
			require.NoError(t, os.MkdirAll(destination, 0o755), "observation fixture directory must exist")
			environment := []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
			initialize := func(directory string) {
				command := exec.Command("/usr/bin/git", "init", "--quiet", directory)
				command.Env = environment
				output, err := command.CombinedOutput()
				require.NoError(t, err, "real Git should initialize the observation fixture: %s", output)
			}
			if tt.createParent {
				initialize(parent)
			}
			initialize(destination)
			original := &agent.Sandbox{WorkDir: parent, Env: map[string]string{"GIT_DIR": filepath.Join(parent, ".git"), "GIT_COMMON_DIR": filepath.Join(parent, ".git"), "GIT_WORK_TREE": parent, "GIT_OBJECT_DIRECTORY": filepath.Join(parent, ".git/objects"), "GIT_INDEX_FILE": filepath.Join(parent, ".git/index"), "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"}}
			observed := acceptanceObservationSandbox(original, destination)
			command := exec.Command("/usr/bin/git", "rev-parse", "--absolute-git-dir")
			command.Dir = observed.WorkDir
			command.Env = []string{"PATH=/usr/bin:/bin"}
			for name, value := range observed.Env {
				command.Env = append(command.Env, name+"="+value)
			}
			output, err := command.CombinedOutput()
			require.NoError(t, err, "observation must inspect the cloned destination instead of its parent: %s", output)
			physicalGitDir, resolveErr := filepath.EvalSymlinks(filepath.Join(destination, ".git"))
			require.NoError(t, resolveErr, "host fixture Git path should resolve physically")
			require.Equal(t, physicalGitDir+"\n", string(output), "real Git must resolve the exact destination metadata")
			require.Equal(t, filepath.Join(parent, ".git"), original.Env["GIT_DIR"], "observation must not mutate the production input sandbox")
		})
	}
}

func (p *acceptanceProvider) faultDiagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.faultEvidence
}
func (p *acceptanceProvider) cleanupEvidence() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(json.RawMessage(nil), p.processCleanupEvidence...)
}

type acceptanceProcessObservation struct {
	PID          int    `json:"pid"`
	State        string `json:"state"`
	ProcessGroup int    `json:"process_group"`
	Stat         string `json:"stat"`
}
type acceptanceCleanupObservation struct {
	PID          int                            `json:"pid"`
	ProcessGroup int                            `json:"process_group"`
	Target       acceptanceProcessObservation   `json:"target"`
	Members      []acceptanceProcessObservation `json:"members"`
}

func acceptanceTerminalProcessState(state string) bool {
	return state == "absent" || state == "Z" || state == "X"
}

// Capture target and process-group state before container destruction. A
// zombie is terminal but can remain until PID 1 reaps it; all live or unknown
// states fail. The snapshot records the observed states rather than inferring
// termination from signal delivery or a later container removal.
const acceptanceProcessCleanupPython = `
import json, pathlib, sys
marker = json.loads(pathlib.Path(".143/hang.pid").read_text())
pid, group = marker["pid"], marker["process_group"]
if not isinstance(pid,int) or not isinstance(group,int) or pid <= 1 or group <= 1:
    raise RuntimeError("cleanup fixture process identity is invalid")
def observe(candidate):
    try:
        with open("/proc/%d/stat" % candidate) as source:
            value = source.read(4097)
    except FileNotFoundError:
        return {"pid":candidate,"state":"absent","process_group":0,"stat":""}
    if len(value) > 4096:
        raise RuntimeError("cleanup process stat exceeds capacity")
    fields = value.rsplit(")",1)[1].split()
    return {"pid":candidate,"state":fields[0],"process_group":int(fields[2]),"stat":value[:512]}
target = observe(pid)
members = []
for entry in pathlib.Path("/proc").iterdir():
    if entry.name.isdecimal():
        current = observe(int(entry.name))
        if current["process_group"] == group:
            members.append(current)
            if len(members) > 16:
                raise RuntimeError("cleanup process group exceeds fixture capacity")
record = {"pid":pid,"process_group":group,"target":target,"members":sorted(members,key=lambda item:item["pid"])}
print(json.dumps(record),flush=True)
terminal = {"absent","Z","X"}
sys.exit(0 if target["state"] in terminal and all(item["state"] in terminal for item in members) else 1)
`

func TestReviewContainerAcceptanceTerminalProcessStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state    string
		expected bool
	}{{"absent", true}, {"Z", true}, {"X", true}, {"R", false}, {"S", false}, {"D", false}, {"T", false}, {"t", false}, {"I", false}, {"W", false}, {"", false}, {"unknown", false}}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, acceptanceTerminalProcessState(tt.state), "cleanup must accept only absent or explicitly terminal processes")
		})
	}
}

// Deadline evidence is attributed by the call path, never by command spelling:
// trusted preparation, image-version probes and cleanup all execute Python.
type acceptancePreparationExecContextKey struct{}

func acceptanceMarkedPreparationDeadline(ctx context.Context) (float64, bool) {
	marked, ok := ctx.Value(acceptancePreparationExecContextKey{}).(bool)
	if !ok || !marked {
		return 0, false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	return float64(deadline.UnixNano()) / 1e9, true
}
func TestReviewContainerAcceptanceDeadlineAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		preparation, canceled bool
	}{{name: "trusted preparation", preparation: true}, {name: "unmarked version or cleanup observer"}, {name: "canceled trusted preparation", preparation: true, canceled: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			originalDeadline := time.Now().Add(25 * time.Second)
			original, cancel := context.WithDeadline(context.Background(), originalDeadline)
			defer cancel()
			if tt.canceled {
				cancel()
			}
			observed := original
			if tt.preparation {
				observed = context.WithValue(original, acceptancePreparationExecContextKey{}, true)
			}
			deadline, attributed := acceptanceMarkedPreparationDeadline(observed)
			require.Equal(t, tt.preparation, attributed, "only the preparation call path may contribute SDK preparation evidence")
			actualDeadline, ok := observed.Deadline()
			require.True(t, ok, "attribution must preserve the original context deadline")
			require.Equal(t, originalDeadline, actualDeadline, "observer metadata must not reset preparation or cleanup deadlines")
			require.Equal(t, original.Err(), observed.Err(), "observer metadata must preserve cancellation")
			if tt.preparation {
				require.Equal(t, float64(originalDeadline.UnixNano())/1e9, deadline, "attributed SDK deadline must exactly match its original context")
			}
		})
	}
}

func TestReviewContainerAcceptanceProviderLogScan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, credential string
		split            bool
	}{
		{name: "late raw", credential: acceptanceToken},
		{name: "late Basic", credential: base64.StdEncoding.EncodeToString([]byte("x-access-token:" + acceptanceToken))},
		{name: "late query escaped", credential: url.QueryEscape(acceptanceToken)},
		{name: "late path escaped", credential: url.PathEscape(acceptanceToken)},
		{name: "split raw", credential: acceptanceToken, split: true},
		{name: "split Basic", credential: base64.StdEncoding.EncodeToString([]byte("x-access-token:" + acceptanceToken)), split: true},
		{name: "split query escaped", credential: url.QueryEscape(acceptanceToken), split: true},
		{name: "split path escaped", credential: url.PathEscape(acceptanceToken), split: true},
		{name: "large clean stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs acceptanceLogBuffer
			prefix := bytes.Repeat([]byte("."), 2*acceptanceProviderLogArtifactLimit)
			written, err := logs.Write(prefix)
			require.NoError(t, err, "large provider writes must be consumed without unbounded retention")
			require.Equal(t, len(prefix), written, "the log writer must report the full accepted write after the artifact fills")
			require.False(t, logs.CredentialDetected(), "clean bytes beyond the artifact limit must not produce a false credential alarm")
			if tt.split {
				// A one-byte chunk exercises every possible credential boundary, after
				// retention has already stopped.
				for _, value := range []byte(tt.credential) {
					written, err = logs.Write([]byte{value})
					require.NoError(t, err, "split provider log writes must preserve scan progress")
					require.Equal(t, 1, written, "split writes must be fully consumed")
				}
			} else if tt.credential != "" {
				written, err = logs.Write([]byte(tt.credential))
				require.NoError(t, err, "late credential bytes must be scanned after artifact retention stops")
				require.Equal(t, len(tt.credential), written, "late writes must be fully consumed")
			} else {
				cleanChunk := bytes.Repeat([]byte("clean-log\n"), 4096)
				for range 100 {
					written, err = logs.Write(cleanChunk)
					require.NoError(t, err, "large clean streams must stay bounded")
					require.Equal(t, len(cleanChunk), written, "every clean write must be consumed")
				}
			}
			require.Equal(t, tt.credential != "", logs.CredentialDetected(), "detection must cover late and split raw, Basic and URL-encoded forms")
			require.Equal(t, string(prefix[:acceptanceProviderLogArtifactLimit]), logs.String(), "artifact retention must preserve its exact original 64 KiB cap")
			require.LessOrEqual(t, len(logs.scanTail), logs.maxScanTail, "scanner state must stay bounded independently of total log length")
			require.LessOrEqual(t, cap(logs.scanTail), logs.maxScanTail*2, "boundary storage must not grow with streamed output")
		})
	}
}
