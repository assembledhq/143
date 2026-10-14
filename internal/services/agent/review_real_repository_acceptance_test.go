package agent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/providers"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This suite accepts no inline credentials, repository overlays, local Git
// transports or substitute runtimes. Its input and all results belong outside
// the public checkout. The token is read on the host and delivered only via
// the production preparation stdin protocol.
type realReviewManifest struct {
	Version      int                    `json:"version"`
	Repositories []realReviewRepository `json:"repositories"`
}
type realReviewRepository struct {
	Name               string            `json:"name"`
	FullName           string            `json:"full_name"`
	CloneURL           string            `json:"clone_url"`
	DefaultBranch      string            `json:"default_branch"`
	InstallationID     int64             `json:"installation_id"`
	TokenKind          string            `json:"token_kind"`
	PRNumber           int               `json:"pr_number"`
	BaseSHA            string            `json:"base_sha"`
	HeadSHA            string            `json:"head_sha"`
	ConfigSHA256       string            `json:"config_sha256"`
	HelperSHA256       map[string]string `json:"helper_sha256"`
	CPULimit           float64           `json:"cpu_limit"`
	MemoryLimitMB      int               `json:"memory_limit_mb"`
	DiskLimitGB        int               `json:"disk_limit_gb"`
	SnapshotLimitBytes int64             `json:"snapshot_limit_bytes"`
}

func decodeRealReviewManifest(raw []byte) (realReviewManifest, error) {
	var manifest realReviewManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, errors.New("invalid private acceptance manifest")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return manifest, errors.New("acceptance manifest must contain one JSON object")
	}
	if manifest.Version != 1 || len(manifest.Repositories) == 0 || len(manifest.Repositories) > 16 {
		return manifest, errors.New("unsupported acceptance inventory")
	}
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	slug := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	names := map[string]bool{}
	for _, repo := range manifest.Repositories {
		remote, err := url.Parse(repo.CloneURL)
		if err != nil || remote.Scheme != "https" || remote.Host != "github.com" || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" || remote.Path != "/"+repo.FullName+".git" || strings.Count(repo.FullName, "/") != 1 || strings.Contains(repo.FullName, "..") {
			return manifest, errors.New("repository identity must be a credential-free canonical GitHub HTTPS URL")
		}
		if !slug.MatchString(repo.Name) || names[repo.Name] || repo.DefaultBranch == "" || strings.HasPrefix(repo.DefaultBranch, "-") || strings.ContainsAny(repo.DefaultBranch, "\r\n\x00") || repo.InstallationID <= 0 || repo.PRNumber <= 0 || !sha.MatchString(repo.BaseSHA) || !sha.MatchString(repo.HeadSHA) || !digest.MatchString(repo.ConfigSHA256) {
			return manifest, errors.New("acceptance repository requires unique name, trusted identity and exact revisions")
		}
		if repo.TokenKind != "github_app_installation" && repo.TokenKind != "github_actions_job" {
			return manifest, errors.New("acceptance must identify the host token source")
		}
		if repo.CPULimit <= 0 || repo.CPULimit > 64 || repo.MemoryLimitMB < 512 || repo.MemoryLimitMB > 131072 || repo.DiskLimitGB <= 0 || repo.DiskLimitGB > 100 || repo.SnapshotLimitBytes <= 0 || repo.SnapshotLimitBytes > 20*1024*1024*1024 {
			return manifest, errors.New("acceptance resource and snapshot bounds must be explicit")
		}
		for name, hash := range repo.HelperSHA256 {
			if !strings.HasPrefix(name, ".143/") || path.Clean(name) != name || strings.ContainsAny(name, "\r\n\x00\\") || !digest.MatchString(hash) {
				return manifest, errors.New("helper identity must be an exact in-repository path and SHA256")
			}
		}
		names[repo.Name] = true
	}
	return manifest, nil
}

func readRealReviewToken(name string) (string, error) {
	descriptor, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("host credential file must be a private regular file")
	}
	file := os.NewFile(uintptr(descriptor), name)
	metadata, statErr := file.Stat()
	if statErr != nil || !metadata.Mode().IsRegular() || metadata.Mode().Perm()&0o077 != 0 || metadata.Size() < 16 || metadata.Size() > 16384 {
		if closeErr := file.Close(); closeErr != nil {
			return "", errors.New("invalid host credential descriptor could not close")
		}
		return "", errors.New("host credential file must be a private regular file of bounded size")
	}
	value, readErr := io.ReadAll(io.LimitReader(file, 16385))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(value) > 16384 {
		return "", errors.New("host credential file could not be read safely")
	}
	token := strings.TrimSuffix(string(value), "\n")
	if strings.ContainsAny(token, "\r\n\x00") || len(token) < 16 {
		return "", errors.New("host credential file must contain one token")
	}
	return token, nil
}

//nolint:paralleltest // repository cases measure bounded worker resources sequentially without sibling contention
func TestReviewRealRepositoryAcceptance(t *testing.T) {
	t.Parallel()
	if os.Getenv("REVIEW_REAL_REPOSITORY_ACCEPTANCE") != "1" {
		t.Skip("opt in with make test-review-real-repository-acceptance")
	}
	require.Equal(t, "linux", runtime.GOOS, "requested real-repository acceptance must run on Linux")
	manifestPath, tokenPath := os.Getenv("REVIEW_REAL_REPO_MANIFEST"), os.Getenv("REVIEW_REAL_REPO_TOKEN_FILE")
	require.NotEmpty(t, manifestPath, "private manifest path is required")
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err, "private manifest should be readable")
	require.LessOrEqual(t, len(raw), 65536, "private inventory must be bounded")
	manifest, err := decodeRealReviewManifest(raw)
	require.NoError(t, err, "private inventory must bind exact trusted repository identities")
	token, err := readRealReviewToken(tokenPath)
	require.NoError(t, err, "host-only credential file must be valid")
	image, networkName := os.Getenv("REVIEW_ACCEPTANCE_IMAGE"), os.Getenv("REVIEW_ACCEPTANCE_NETWORK")
	require.NotEmpty(t, image, "explicit built image is required")
	require.NotEmpty(t, networkName, "scoped existing network is required")
	require.Equal(t, "runsc", os.Getenv("REVIEW_ACCEPTANCE_RUNTIME"), "real-repository acceptance must never substitute runc")
	resolvConf := os.Getenv("REVIEW_REAL_REPO_RESOLV_CONF")
	require.True(t, filepath.IsAbs(resolvConf), "gVisor HTTPS acceptance requires an explicit host resolver file")
	resolverMetadata, resolverErr := os.Lstat(resolvConf)
	require.NoError(t, resolverErr, "host resolver file must exist")
	require.True(t, resolverMetadata.Mode().IsRegular(), "host resolver must be a regular file, not a symlink")
	sourceSHA := os.Getenv("REVIEW_ACCEPTANCE_SOURCE_SHA")
	require.Regexp(t, `^[0-9a-f]{40}$`, sourceSHA, "image evidence must bind the harness source revision")
	require.NotEmpty(t, os.Getenv("REVIEW_ACCEPTANCE_RUNSC_VERSION"), "actual runtime version is required")
	artifactRoot := os.Getenv("REVIEW_ACCEPTANCE_ARTIFACT_DIR")
	require.True(t, filepath.IsAbs(artifactRoot), "private artifact directory must be absolute")
	require.NoError(t, os.MkdirAll(artifactRoot, 0o700), "private evidence directory should exist")
	require.NoError(t, os.Chmod(artifactRoot, 0o700), "private evidence directory must not be world-readable")
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err, "real Docker client should initialize")
	t.Cleanup(func() { require.NoError(t, cli.Close(), "Docker client should close") })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := cli.Info(ctx)
	require.NoError(t, err, "Docker daemon must be reachable")
	_, registered := info.Runtimes["runsc"]
	require.True(t, registered, "gVisor must be registered")
	inspected, err := cli.ImageInspect(ctx, image)
	require.NoError(t, err, "built sandbox image must exist")
	require.Equal(t, "linux", inspected.Os, "sandbox image must target Linux")
	require.Equal(t, sourceSHA, inspected.Config.Labels["org.opencontainers.image.revision"], "image label must bind the exact harness source")
	_, err = cli.NetworkInspect(ctx, networkName, network.InspectOptions{})
	require.NoError(t, err, "scoped network must exist")
	// Sequential inventory order is deliberate: the largest repository can run
	// first without competing against sibling acceptance containers for resources.
	for _, repo := range manifest.Repositories {
		t.Run(repo.Name, func(t *testing.T) {
			// No t.Parallel: these cases measure worker-equivalent resource cost.
			logs := realReviewLogBuffer(token)
			surfaces := realReviewLogBuffer(token)
			observed := &realReviewDockerClient{DockerClient: cli, logs: surfaces}
			provider := &realReviewProvider{DockerProvider: providers.NewDockerProvider(observed, zerolog.New(logs).Level(zerolog.DebugLevel), providers.WithRuntime("runsc"), providers.WithNetwork(networkName), providers.WithRequireDiskQuota(repo.DiskLimitGB > 0), providers.WithResolvConf(resolvConf)), client: cli, image: image, network: networkName, repo: repo, logs: logs, surfaces: surfaces, containers: map[string]*agent.Sandbox{}}
			tokens := &realReviewTokens{token: token, installationID: repo.InstallationID}
			orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{Provider: provider, Repositories: realReviewRepoStore{repo}, GitHub: tokens, Env: agent.NewAgentEnv(agent.AgentEnvDeps{}), Logger: zerolog.New(logs), SandboxCapacity: agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: acceptanceCapacityCounter{}, MaxActive: 1, Logger: zerolog.Nop()})})
			report := map[string]any{"repository": repo.FullName, "base_sha": repo.BaseSHA, "head_sha": repo.HeadSHA, "config_sha256": repo.ConfigSHA256, "helper_sha256": repo.HelperSHA256, "source_sha": sourceSHA, "image_id": inspected.ID, "image_os": inspected.Os, "image_architecture": inspected.Architecture, "docker_server_version": info.ServerVersion, "docker_storage_driver": info.Driver, "docker_storage_driver_status": info.DriverStatus, "docker_data_root": info.DockerRootDir, "runsc_version": os.Getenv("REVIEW_ACCEPTANCE_RUNSC_VERSION"), "runtime": "runsc", "cpu_limit": repo.CPULimit, "memory_limit_mb": repo.MemoryLimitMB, "disk_limit_gb": repo.DiskLimitGB, "disk_quota_required": repo.DiskLimitGB > 0, "token_kind": repo.TokenKind, "observation": "trusted run/capture observers record fixed labels and process identity; original functions, payload, deadlines, recipe and security guards are preserved", "limits": []string{"no LLM reviewer execution or durable workspace publication", "GitHub Actions job tokens do not establish deployed GitHub App token issuance", "worker network policy and deployed image identity require separate acceptance"}}
			t.Cleanup(func() {
				report["passed"] = !t.Failed() && !provider.credentialDetected() && provider.created.Load() == provider.destroyed.Load()
				report["disk_quota_accepted"] = !t.Failed() && !provider.credentialDetected() && repo.DiskLimitGB > 0 && provider.created.Load() > 0 && provider.created.Load() == provider.destroyed.Load()
				report["credential_detected"] = provider.credentialDetected()
				report["token_requests"] = tokens.calls.Load()
				report["created_containers"] = provider.created.Load()
				report["destroyed_containers"] = provider.destroyed.Load()
				report["executions"] = provider.observations()
				report["process_cleanup"] = provider.cleanupObservations
				report["sdk_exec_count"] = observed.calls.Load()
				report["creation_resources"] = provider.creationResources
				report["sdk_preparation_deadlines"] = observed.deadlines()
				contents, encodeErr := json.MarshalIndent(report, "", "  ")
				require.NoError(t, encodeErr, "private result should encode")
				require.NoError(t, os.WriteFile(filepath.Join(artifactRoot, repo.Name+".json"), contents, 0o600), "private result must survive failures")
				retained := logs.String()
				if provider.credentialDetected() {
					retained = "credential detected; raw provider output withheld\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(artifactRoot, repo.Name+".log"), []byte(retained), 0o600), "bounded private provider logs should persist")
				require.False(t, provider.credentialDetected(), "independent complete SDK, environment, provider-log, stdout and stderr streams must exclude the host credential")
				require.Equal(t, provider.created.Load(), provider.destroyed.Load(), "every owned container must be destroyed")
			})
			t.Cleanup(func() { provider.cleanup(t) })
			session := realReviewSession(t, repo)
			started := time.Now()
			preparationContext := jobctx.WithJobCreatedAt(jobctx.WithLockToken(context.Background(), uuid.New()), started)
			sandbox, prepareErr := orchestrator.PrepareCodeReviewWorkspace(preparationContext, session, repo.HeadSHA)
			report["cold_elapsed_ms"] = time.Since(started).Milliseconds()
			if prepareErr != nil {
				report["preparation_error"] = realReviewSafeError(prepareErr, token)
			}
			require.NoError(t, prepareErr, "cold production preparation must succeed; consult private stage evidence on failure")
			coldReceipt := provider.read(t, sandbox, ".git/143-review-evidence.json")
			provider.assertBindings(t, sandbox, coldReceipt)
			coldTools := provider.declaredTools(t, sandbox)
			report["cold_declared_tools"] = coldTools
			// This is the first local source read available to a reviewer after the
			// real preparation call, not a claim that a model review was executed.
			handoff := provider.exec(t, sandbox, "GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 /usr/bin/git -c credential.helper= -c core.hooksPath=/dev/null -c remote.origin.promisor=false rev-parse --verify HEAD")
			require.Equal(t, repo.HeadSHA, strings.TrimSpace(string(handoff)), "first reviewer source read must see the pinned head")
			report["cold_through_first_source_read_ms"] = time.Since(started).Milliseconds()
			require.Less(t, time.Since(started), agent.CodeReviewWorkspacePreparationTimeout, "container creation, HTTPS transport, setup, receipt and first source read must fit one original budget")
			require.Equal(t, int64(1), tokens.calls.Load(), "cold production transport must request one scoped host token")
			coldExecutions := provider.observations()
			require.NotEmpty(t, coldExecutions, "production preparation must emit real payload observations")
			require.Equal(t, len(coldExecutions), len(observed.deadlines()), "every cold preparation input must have an attributed real SDK request")
			deadline := started.Add(agent.CodeReviewWorkspacePreparationTimeout)
			for index, execution := range coldExecutions {
				require.InDelta(t, float64(deadline.UnixNano())/1e9, execution.Deadline, 0.001, "all cold stages must retain the original deadline")
				require.InDelta(t, execution.Deadline, observed.deadlines()[index], 0.001, "every real SDK cold deadline must equal its serialized payload")
			}
			report["receipt_sha256"] = realReviewHash(coldReceipt)
			report["cold_observation"] = provider.inspect(t, sandbox)
			coldTrace := provider.readTrace(t, sandbox)
			report["cold_trace"] = coldTrace
			setupCount := 0
			for _, entry := range coldTrace {
				if entry.Stage == "repository_setup" {
					setupCount++
				}
			}
			require.Equal(t, 1, setupCount, "fresh real repository must execute its exact declared setup once")
			phases := []struct {
				name    string
				expired bool
			}{{"warm", false}, {"restored", false}, {"expired", true}}
			for _, phase := range phases {
				turnCreatedAt := time.Now()
				phaseContext := jobctx.WithJobCreatedAt(context.Background(), turnCreatedAt)
				turnContext, turnCancel := context.WithDeadline(phaseContext, turnCreatedAt.Add(agent.CodeReviewWorkspacePreparationTimeout))
				t.Cleanup(func() { turnCancel() })
				if phase.name == "restored" {
					snapshotStarted := time.Now()
					snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 8*time.Minute)
					reader, snapshotErr := provider.Snapshot(snapshotCtx, sandbox)
					require.NoError(t, snapshotErr, "production snapshot transport must start")
					archiveName, copied, snapshotHash, streamErr := persistRealReviewSnapshot(t.TempDir(), repo.Name, reader, repo.SnapshotLimitBytes)
					// Raw snapshots are private temporary transport, never upload artifacts.
					// Cleanup runs even if a later assertion or restore fails.
					t.Cleanup(func() {
						removeErr := os.Remove(archiveName)
						removed := removeErr == nil || errors.Is(removeErr, os.ErrNotExist)
						report["snapshot_temp_removed"] = removed
						require.True(t, removed, "private temporary snapshot must be removed before artifact upload: %v", removeErr)
					})
					snapshotCancel()
					report["snapshot_elapsed_ms"], report["snapshot_bytes"], report["snapshot_sha256"] = time.Since(snapshotStarted).Milliseconds(), copied, snapshotHash
					require.NoError(t, streamErr, "production snapshot must finish within its bounded private stream and close both descriptors")
					require.Positive(t, copied, "snapshot evidence must contain real bytes")
					oldID := sandbox.ID
					destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
					destroyErr := provider.Destroy(destroyCtx, sandbox)
					destroyCancel()
					require.NoError(t, destroyErr, "original container must be destroyed before restore")
					cfg := agent.DefaultSandboxConfig()
					cfg.WorkDir, cfg.HomeDir, cfg.Env = sandbox.WorkDir, sandbox.HomeDir, sandbox.Env
					turnCancel()
					restoreStarted := time.Now()
					turnCreatedAt = restoreStarted
					phaseContext = jobctx.WithJobCreatedAt(context.Background(), turnCreatedAt)
					turnContext, turnCancel = context.WithDeadline(phaseContext, turnCreatedAt.Add(agent.CodeReviewWorkspacePreparationTimeout))
					restored, createErr := provider.Create(turnContext, cfg)
					require.NoError(t, createErr, "restore must create a separate real container")
					sandbox = restored
					require.NotEqual(t, oldID, sandbox.ID, "snapshot restore must use a new filesystem")
					archiveInput, openErr := os.Open(archiveName)
					require.NoError(t, openErr, "snapshot stream must reopen")
					restoreErr := provider.RestoreForReviewPreparation(turnContext, sandbox, archiveInput)
					inputCloseErr := archiveInput.Close()
					report["restore_transport_elapsed_ms"] = time.Since(restoreStarted).Milliseconds()
					require.NoError(t, restoreErr, "production review restore must extract the actual archive")
					require.NoError(t, inputCloseErr, "restored snapshot input should close")
					require.NoError(t, os.Remove(archiveName), "private snapshot source should be removed after transport")
					require.Equal(t, coldReceipt, provider.read(t, sandbox, ".git/143-review-evidence.json"), "actual restore must preserve exact receipt bytes")
				}
				phaseTools := provider.declaredTools(t, sandbox)
				report[phase.name+"_declared_tools_before_verification"] = phaseTools
				require.Equal(t, coldTools, phaseTools, "healthy reuse must retain exact declared tool bytes and modes before the engine can repair anything")
				before := provider.readTrace(t, sandbox)
				callsBefore := tokens.calls.Load()
				if phase.expired {
					turnCancel()
					turnContext = jobctx.WithJobCreatedAt(context.Background(), time.Now().Add(-agent.CodeReviewWorkspacePreparationTimeout-time.Second))
				}
				executionsBefore, sdkBefore := len(provider.observations()), len(observed.deadlines())
				phaseStarted := time.Now()
				reuseErr := agent.EnsureCodeReviewWorkspaceEvidenceForTest(turnContext, orchestrator, session, sandbox)
				turnCancel()
				report[phase.name+"_elapsed_ms"] = time.Since(phaseStarted).Milliseconds()
				if reuseErr != nil {
					report[phase.name+"_error"] = realReviewSafeError(reuseErr, token)
				}
				require.NoError(t, reuseErr, "healthy real-repository evidence should reuse through the production path")
				currentExecutions, currentSDK := provider.observations()[executionsBefore:], observed.deadlines()[sdkBefore:]
				require.Len(t, currentExecutions, 1, "complete evidence must execute exactly one local production evidence validation")
				require.Equal(t, len(currentExecutions), len(currentSDK), "every reuse input must have an attributed SDK execution")
				for index, execution := range currentExecutions {
					require.False(t, execution.Standalone, "reuse must execute the full evidence engine, not a standalone command")
					require.False(t, execution.Clone, "reuse must not clone another workspace")
					require.False(t, execution.Authenticated, "reuse must not attach authentication state")
					require.InDelta(t, execution.Deadline, currentSDK[index], 0.001, "serialized and actual SDK deadlines must agree")
					require.Equal(t, phase.expired, execution.VerifyOnly, "only expired reuse may select bounded platform-only validation")
					if !phase.expired {
						require.InDelta(t, float64(turnCreatedAt.Add(agent.CodeReviewWorkspacePreparationTimeout).UnixNano())/1e9, execution.Deadline, 0.001, "restore and reuse validation must retain their turn's original absolute deadline")
					}
				}
				if phase.name == "restored" {
					report["restored_through_verification_ms"] = time.Since(turnCreatedAt).Milliseconds()
					require.Less(t, time.Since(turnCreatedAt), agent.CodeReviewWorkspacePreparationTimeout, "new container, snapshot restore and verification must share one eight-minute budget")
				}
				require.Equal(t, callsBefore, tokens.calls.Load(), "warm, restored and expired paths must acquire no token")
				require.Equal(t, coldReceipt, provider.read(t, sandbox, ".git/143-review-evidence.json"), "reuse must preserve exact receipt bytes")
				after := provider.readTrace(t, sandbox)
				require.GreaterOrEqual(t, len(after), len(before), "trusted trace must append without erasure")
				current := after[len(before):]
				require.NotEmpty(t, current, "actual reuse must emit a trusted execution trace")
				fullHashes, toolVerifiers := 0, 0
				for _, entry := range current {
					if entry.Stage == "full_object_hash" {
						fullHashes++
					}
					if entry.Stage == "tool_verifier" {
						toolVerifiers++
					}
					require.NotEqual(t, "source_hydration", entry.Stage, "complete evidence reuse may not hydrate or repair captured source")
					require.NotEqual(t, "repository_setup", entry.Stage, "reuse may not run repository installation")
					require.NotEqual(t, "tool_repair", entry.Stage, "healthy warm, restored and expired evidence may not reinstall tools")
					require.False(t, entry.Authenticated, "reuse must not execute authenticated Git")
					if phase.expired {
						require.NotEqual(t, "repository_verifier", entry.Stage, "expired verification must not execute arbitrary repository verifier")
					}
				}
				require.Positive(t, fullHashes, "each reuse must actually execute the full Git object integrity barrier")
				if len(coldTools) > 0 {
					require.Positive(t, toolVerifiers, "each reuse must execute platform verification of actual declared tool pins")
				}
				require.Equal(t, coldTools, provider.declaredTools(t, sandbox), "healthy validation must preserve exact declared tool bytes and permissions")
				report[phase.name+"_trace"] = current
				if phase.expired {
					require.Less(t, time.Since(phaseStarted), agent.CodeReviewWorkspaceVerificationTimeout, "expired platform-only verification must finish inside its original minute")
				}
				provider.assertBindings(t, sandbox, coldReceipt)
				report[phase.name+"_observation"] = provider.inspect(t, sandbox)
			}
		})
	}
}

// The caller supplies the production snapshot stream and a private temporary
// directory outside uploaded artifacts. Every failure closes both descriptors
// and removes partial bytes; transport consumes at most the configured cap+1.
func persistRealReviewSnapshot(root, name string, reader io.ReadCloser, limit int64) (filename string, size int64, digest string, returnErr error) {
	var archive *os.File
	defer func() {
		returnErr = errors.Join(returnErr, reader.Close())
		if archive != nil {
			returnErr = errors.Join(returnErr, archive.Close())
			if returnErr != nil {
				removeErr := os.Remove(filename)
				if !errors.Is(removeErr, os.ErrNotExist) {
					returnErr = errors.Join(returnErr, removeErr)
				}
			}
		}
	}()
	var err error
	archive, err = os.CreateTemp(root, name+"-snapshot-*.tar.zst")
	if err != nil {
		return "", 0, "", err
	}
	filename = archive.Name()
	if err := archive.Chmod(0o600); err != nil {
		return filename, 0, "", err
	}
	hash := sha256.New()
	size, returnErr = io.Copy(io.MultiWriter(archive, hash), io.LimitReader(reader, limit+1))
	digest = hex.EncodeToString(hash.Sum(nil))
	if size > limit {
		returnErr = errors.Join(returnErr, errors.New("production snapshot exceeded private input cap"))
	}
	return
}

type realReviewDeclaredTool struct {
	DeclaredVersion string `json:"declared_version"`
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
	Mode            uint32 `json:"mode"`
}

type realReviewExecution struct {
	Deadline      float64 `json:"deadline_unix"`
	ElapsedMS     int64   `json:"elapsed_ms"`
	VerifyOnly    bool    `json:"verify_only"`
	Standalone    bool    `json:"standalone"`
	Authenticated bool    `json:"authenticated"`
	Clone         bool    `json:"clone"`
	Exit          int     `json:"exit"`
}
type realReviewTrace struct {
	Stage         string `json:"stage"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	Authenticated bool   `json:"authenticated"`
	PID           int    `json:"pid"`
	Group         int    `json:"group"`
	Start         string `json:"start"`
}
type realReviewDockerClient struct {
	providers.DockerClient
	logs                 *acceptanceLogBuffer
	mu                   sync.Mutex
	preparationDeadlines []float64
	calls                atomic.Int64
}

func (c *realReviewDockerClient) ContainerExecCreate(ctx context.Context, id string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	c.calls.Add(1)
	_, err := c.logs.Write([]byte(strings.Join(options.Cmd, "\x00") + strings.Join(options.Env, "\x00")))
	if err != nil {
		return container.ExecCreateResponse{}, err
	}
	if value, ok := acceptanceMarkedPreparationDeadline(ctx); ok {
		c.mu.Lock()
		c.preparationDeadlines = append(c.preparationDeadlines, value)
		c.mu.Unlock()
	}
	return c.DockerClient.ContainerExecCreate(ctx, id, options)
}
func (c *realReviewDockerClient) deadlines() []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]float64(nil), c.preparationDeadlines...)
}

type realReviewProvider struct {
	*providers.DockerProvider
	client              *client.Client
	image, network      string
	repo                realReviewRepository
	logs                *acceptanceLogBuffer
	surfaces            *acceptanceLogBuffer
	outputScanners      []*acceptanceLogBuffer
	mu                  sync.Mutex
	containers          map[string]*agent.Sandbox
	executions          []realReviewExecution
	cleanupObservations []json.RawMessage
	created, destroyed  atomic.Int64
	creationResources   []map[string]any
}

// Independent streams need independent boundary tails: interleaving stderr,
// logger or SDK bytes must never erase a partial stdout credential match.
// Each output retains at most the existing 64 KiB artifact cap and its tail;
// only detection is aggregated, and output buffers are never written to disk.
func (p *realReviewProvider) observedOutput(original io.Writer) io.Writer {
	p.logs.mu.Lock()
	scanner := &acceptanceLogBuffer{needles: append([][]byte(nil), p.logs.needles...), maxScanTail: p.logs.maxScanTail}
	p.logs.mu.Unlock()
	p.mu.Lock()
	p.outputScanners = append(p.outputScanners, scanner)
	p.mu.Unlock()
	return io.MultiWriter(scanner, original)
}
func (p *realReviewProvider) credentialDetected() bool {
	if p.logs.CredentialDetected() || p.surfaces.CredentialDetected() {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, scanner := range p.outputScanners {
		if scanner.CredentialDetected() {
			return true
		}
	}
	return false
}

func (p *realReviewProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	cfg.Image, cfg.NetworkName, cfg.CPULimit, cfg.MemoryLimitMB, cfg.DiskLimitGB = p.image, p.network, p.repo.CPULimit, p.repo.MemoryLimitMB, p.repo.DiskLimitGB
	sandbox, err := p.DockerProvider.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p.created.Add(1)
	p.mu.Lock()
	p.containers[sandbox.ID] = sandbox
	p.mu.Unlock()
	actual, err := p.client.ContainerInspect(ctx, sandbox.ID)
	if err != nil {
		return nil, errors.New("actual container resource inspection failed")
	}
	if actual.Config == nil || actual.HostConfig == nil {
		return nil, errors.New("actual container configuration absent")
	}
	if _, err := p.surfaces.Write([]byte(strings.Join(actual.Config.Env, "\x00"))); err != nil {
		return nil, err
	}
	if actual.HostConfig.Runtime != "runsc" || actual.HostConfig.Memory != int64(p.repo.MemoryLimitMB)*1024*1024 || actual.HostConfig.NanoCPUs != int64(p.repo.CPULimit*1e9) {
		return nil, errors.New("actual runtime or resource limits differ from private input")
	}
	if p.repo.DiskLimitGB > 0 && actual.HostConfig.StorageOpt["size"] != fmt.Sprintf("%dG", p.repo.DiskLimitGB) {
		return nil, errors.New("actual disk quota differs from required private input")
	}
	p.creationResources = append(p.creationResources, map[string]any{"runtime": actual.HostConfig.Runtime, "memory_bytes": actual.HostConfig.Memory, "nano_cpus": actual.HostConfig.NanoCPUs, "storage_options": actual.HostConfig.StorageOpt, "storage_driver": actual.Driver, "network_mode": actual.HostConfig.NetworkMode})
	var output bytes.Buffer
	code, traceErr := p.DockerProvider.Exec(ctx, sandbox, "umask 077; : > /tmp/143-real-review-observation.jsonl", &output, io.Discard)
	if traceErr != nil || code != 0 {
		return nil, errors.New("private trusted observation trace initialization failed")
	}
	return sandbox, nil
}
func (p *realReviewProvider) ExecWithStdinForReviewPreparation(ctx context.Context, sandbox *agent.Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return -1, errors.New("production preparation input exceeded observation bound")
	}
	var envelope struct {
		Payload struct {
			Deadline         float64 `json:"deadline_unix"`
			VerifyOnly       bool    `json:"verify_only"`
			Standalone       bool    `json:"standalone"`
			CloneDestination string  `json:"clone_destination"`
		}
		Authentication *json.RawMessage `json:"authentication"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return -1, errors.New("production preparation payload could not be attributed")
	}
	instrumented, err := instrumentRealReviewCommand(command)
	if err != nil {
		return -1, err
	}
	started := time.Now()
	marked := context.WithValue(ctx, acceptancePreparationExecContextKey{}, true)
	code, execErr := p.DockerProvider.ExecWithStdinForReviewPreparation(marked, sandbox, instrumented, bytes.NewReader(raw), p.observedOutput(stdout), p.observedOutput(stderr))
	p.mu.Lock()
	p.executions = append(p.executions, realReviewExecution{Deadline: envelope.Payload.Deadline, ElapsedMS: time.Since(started).Milliseconds(), VerifyOnly: envelope.Payload.VerifyOnly, Standalone: envelope.Payload.Standalone, Authenticated: envelope.Authentication != nil, Clone: envelope.Payload.CloneDestination != "", Exit: code})
	p.mu.Unlock()
	return code, execErr
}
func instrumentRealReviewCommand(command string) (string, error) {
	anchor := "\ntry:\n    check_workspace_quarantine()\n    message = read_preparation_input()"
	if strings.Count(command, anchor) != 1 {
		return "", errors.New("trusted preparation observation anchor changed")
	}
	return strings.Replace(command, anchor, "\n"+realReviewTraceObserver+anchor, 1), nil
}
func (p *realReviewProvider) observations() []realReviewExecution {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]realReviewExecution(nil), p.executions...)
}
func (p *realReviewProvider) Destroy(ctx context.Context, sandbox *agent.Sandbox) error {
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var output bytes.Buffer
	code, observeErr := p.DockerProvider.Exec(observeCtx, sandbox, realReviewPython(realReviewCleanupPython), &output, io.Discard)
	if output.Len() <= 1024*1024 && json.Valid(output.Bytes()) {
		p.cleanupObservations = append(p.cleanupObservations, append(json.RawMessage(nil), output.Bytes()...))
	}
	destroyErr := p.DockerProvider.Destroy(ctx, sandbox)
	if destroyErr == nil {
		p.destroyed.Add(1)
		p.mu.Lock()
		delete(p.containers, sandbox.ID)
		p.mu.Unlock()
	}
	if observeErr != nil || code != 0 {
		return errors.Join(errors.New("live process group remained or cleanup observation failed before destruction"), destroyErr)
	}
	return destroyErr
}
func (p *realReviewProvider) cleanup(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	owned := make([]*agent.Sandbox, 0, len(p.containers))
	for _, sandbox := range p.containers {
		owned = append(owned, sandbox)
	}
	p.mu.Unlock()
	for _, sandbox := range owned {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := p.Destroy(ctx, sandbox)
		cancel()
		require.NoError(t, err, "all acceptance-owned containers must stop and be destroyed")
	}
}
func (p *realReviewProvider) read(t *testing.T, sandbox *agent.Sandbox, relative string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value, err := p.ReadFile(ctx, sandbox, path.Join(sandbox.WorkDir, relative))
	require.NoError(t, err, "pinned private repository metadata must be readable")
	return value
}
func (p *realReviewProvider) exec(t *testing.T, sandbox *agent.Sandbox, command string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var output bytes.Buffer
	code, err := p.DockerProvider.Exec(ctx, sandbox, command, &output, io.Discard)
	require.NoError(t, err, "platform-only private evidence probe transport must succeed")
	require.Equal(t, 0, code, "platform-only private evidence probe must succeed")
	require.LessOrEqual(t, output.Len(), 1024*1024, "private observation output must remain bounded")
	return output.Bytes()
}
func (p *realReviewProvider) readTrace(t *testing.T, sandbox *agent.Sandbox) []realReviewTrace {
	t.Helper()
	raw := p.exec(t, sandbox, "/bin/cat /tmp/143-real-review-observation.jsonl")
	result := []realReviewTrace{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry realReviewTrace
		require.NoError(t, json.Unmarshal(line, &entry), "trusted execution trace should decode")
		result = append(result, entry)
	}
	return result
}
func (p *realReviewProvider) assertBindings(t *testing.T, sandbox *agent.Sandbox, receipt []byte) {
	t.Helper()
	var actual struct {
		Revision struct {
			Version int    `json:"version"`
			Base    string `json:"base_sha"`
			Head    string `json:"head_sha"`
			Config  string `json:"config_hash"`
		}
	}
	require.NoError(t, json.Unmarshal(receipt, &actual), "exact receipt should decode")
	require.Equal(t, 2, actual.Revision.Version, "receipt format should match current engine")
	require.Equal(t, p.repo.BaseSHA, actual.Revision.Base, "receipt must bind captured base")
	require.Equal(t, p.repo.HeadSHA, actual.Revision.Head, "receipt must bind captured head")
	require.Equal(t, p.repo.ConfigSHA256, actual.Revision.Config, "receipt must bind candidate recipe hash")
	require.Equal(t, p.repo.ConfigSHA256, realReviewHash(p.read(t, sandbox, ".143/config.json")), "actual recipe bytes must match private candidate")
	for name, hash := range p.repo.HelperSHA256 {
		require.Equal(t, hash, realReviewHash(p.read(t, sandbox, name)), "actual helper bytes must match private candidate")
	}
	status := p.exec(t, sandbox, "GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 /usr/bin/git -c credential.helper= -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.untrackedCache=false -c remote.origin.promisor=false diff --no-ext-diff --no-textconv --name-only HEAD --")
	require.Empty(t, string(status), "tracked source and lockfiles must remain unchanged")
	for _, relative := range []string{".git/config", ".git/143-review-evidence.json"} {
		_, err := p.surfaces.Write(p.read(t, sandbox, relative))
		require.NoError(t, err, "metadata credential scan should consume the complete value")
	}
}
func (p *realReviewProvider) declaredTools(t *testing.T, sandbox *agent.Sandbox) map[string]realReviewDeclaredTool {
	t.Helper()
	var configured struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(p.read(t, sandbox, ".143/config.json"), &configured), "declared tool pins should decode from exact recipe bytes")
	raw := p.exec(t, sandbox, realReviewPython(realReviewDeclaredToolsPython))
	tools := map[string]realReviewDeclaredTool{}
	require.NoError(t, json.Unmarshal(raw, &tools), "declared tool byte and mode observation should decode")
	require.Len(t, tools, len(configured.Dependencies), "every declared tool must have a filesystem observation")
	for name, version := range configured.Dependencies {
		actual, exists := tools[name]
		require.True(t, exists, "declared tool %s must exist before validation", name)
		require.Equal(t, version, actual.DeclaredVersion, "observed tool must bind its exact declared pin")
		require.Equal(t, path.Join(sandbox.HomeDir, ".local/bin", name), actual.Path, "declared executable must stay in the platform tool directory")
		require.Regexp(t, `^[0-9a-f]{64}$`, actual.SHA256, "declared tool observation must hash exact executable bytes")
		require.NotZero(t, actual.Mode&0o111, "declared tool must retain executable permissions")
	}
	return tools
}

func (p *realReviewProvider) inspect(t *testing.T, sandbox *agent.Sandbox) json.RawMessage {
	t.Helper()
	raw := p.exec(t, sandbox, realReviewPython(realReviewMetricsPython))
	require.True(t, json.Valid(raw), "actual capacity and tool observation should be valid JSON")
	var observation map[string]any
	require.NoError(t, json.Unmarshal(raw, &observation), "platform observation should decode")
	statsCtx, statsCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer statsCancel()
	response, statsErr := p.client.ContainerStatsOneShot(statsCtx, sandbox.ID)
	require.NoError(t, statsErr, "actual Docker resource statistics should be available")
	var statistics container.StatsResponse
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&statistics)
	closeErr := response.Body.Close()
	require.NoError(t, decodeErr, "actual Docker resource statistics should decode within bounds")
	require.NoError(t, closeErr, "resource statistics response must close")
	observation["docker_resources"] = map[string]any{"cpu_total_ns": statistics.CPUStats.CPUUsage.TotalUsage, "memory_usage_bytes": statistics.MemoryStats.Usage, "memory_max_usage_bytes": statistics.MemoryStats.MaxUsage, "memory_limit_bytes": statistics.MemoryStats.Limit, "pids_current": statistics.PidsStats.Current, "block_io": statistics.BlkioStats}
	encoded, err := json.Marshal(observation)
	require.NoError(t, err, "combined private capacity observation should encode")
	return encoded
}

type realReviewRepoStore struct{ repo realReviewRepository }

func (s realReviewRepoStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return models.Repository{FullName: s.repo.FullName, CloneURL: s.repo.CloneURL, DefaultBranch: s.repo.DefaultBranch, InstallationID: s.repo.InstallationID}, nil
}

type realReviewTokens struct {
	token          string
	installationID int64
	calls          atomic.Int64
}

func (p *realReviewTokens) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if installationID != p.installationID {
		return "", errors.New("private host token identity mismatch")
	}
	p.calls.Add(1)
	return p.token, nil
}
func realReviewSession(t *testing.T, repo realReviewRepository) *models.Session {
	t.Helper()
	repositoryID := uuid.New()
	revision, err := json.Marshal(map[string]any{"kind": "code_review", "github_pr_number": repo.PRNumber, "base_sha": repo.BaseSHA, "head_sha": repo.HeadSHA})
	require.NoError(t, err, "exact private comparison must encode")
	return &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview, AgentType: models.AgentTypeCodex, RepositoryID: &repositoryID, RevisionContext: revision}
}
func realReviewHash(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }
func realReviewPython(script string) string {
	return "/usr/bin/python3 -I -S -c '" + strings.ReplaceAll(script, "'", "'\"'\"'") + "'"
}
func realReviewSafeError(err error, token string) string {
	message := acceptanceErrorCauses(err)
	for _, form := range []string{token, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token)), url.QueryEscape(token), url.PathEscape(token)} {
		message = strings.ReplaceAll(message, form, "[redacted]")
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return message
}
func realReviewLogBuffer(token string) *acceptanceLogBuffer {
	buffer := &acceptanceLogBuffer{}
	for _, form := range []string{token, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token)), url.QueryEscape(token), url.PathEscape(token)} {
		buffer.needles = append(buffer.needles, []byte(form))
		buffer.maxScanTail = max(buffer.maxScanTail, len(form)-1)
	}
	return buffer
}

// This observer never reads authentication or serializes script contents. It
// decorates the actual functions without changing their return/exception path.
const realReviewTraceObserver = `
import pathlib
_real_run = run
_real_capture = capture
def _real_record(value):
    descriptor = os.open("/tmp/143-real-review-observation.jsonl", os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(descriptor, "a") as target:
        if not stat.S_ISREG(os.fstat(target.fileno()).st_mode) or os.fstat(target.fileno()).st_size > 1048576:
            raise RuntimeError("acceptance observer requires bounded regular trace")
        target.write(json.dumps(value) + "\n")
def _real_identity(child):
    try:
        fields = pathlib.Path("/proc/%d/stat" % child.pid).read_text().rsplit(")", 1)[1].split()
        _real_record({"stage":"process_group","pid":child.pid,"group":int(fields[2]),"start":fields[19]})
    except FileNotFoundError:
        pass
_real_popen = subprocess.Popen
def _real_observed_popen(*args, **kwargs):
    child = _real_popen(*args, **kwargs)
    _real_identity(child)
    return child
subprocess.Popen = _real_observed_popen
def run(script, required=True, authenticate=False, platform=False):
    label = "standalone" if p.get("standalone", False) else next((name for key, name in (("prepare","repository_setup"),("verify_recipe","repository_verifier"),("verify_integrity","full_object_hash"),("verify_trees","captured_tree_closure"),("verify_git","pinned_source"),("verify_tools","tool_verifier"),("repair_tools","tool_repair"),("hydrate_trees","source_hydration")) if script == p.get(key)), "platform")
    started = time.monotonic()
    try:
        return _real_run(script, required, authenticate, platform)
    finally:
        _real_record({"stage":label,"elapsed_ms":int((time.monotonic()-started)*1000),"authenticated":authenticate})
def capture(args):
    started = time.monotonic()
    try:
        return _real_capture(args)
    finally:
        _real_record({"stage":"platform_capture","elapsed_ms":int((time.monotonic()-started)*1000),"authenticated":False})
`
const realReviewCleanupPython = `
import json,os,pathlib
trace=pathlib.Path("/tmp/143-real-review-observation.jsonl")
groups=set()
if trace.exists():
    for line in trace.read_text().splitlines():
        value=json.loads(line)
        if value.get("group",0)>1: groups.add(value["group"])
members=[]
for entry in pathlib.Path("/proc").iterdir():
    if not entry.name.isdigit(): continue
    try:
        fields=(entry/"stat").read_text().rsplit(")",1)[1].split()
        group=int(fields[2])
        if group in groups: members.append({"pid":int(entry.name),"group":group,"state":fields[0]})
    except (FileNotFoundError,ProcessLookupError): pass
live=[member for member in members if member["state"] not in ("Z","X","x")]
print(json.dumps({"observed_groups":sorted(groups),"remaining_members":members,"live_members":live}))
raise SystemExit(1 if live else 0)
`
const realReviewDeclaredToolsPython = `
import hashlib,json,os,pathlib,re,stat
configured=json.loads(pathlib.Path(".143/config.json").read_text()).get("dependencies",{})
home=pathlib.Path.home()
tools={}
for name,version in sorted(configured.items()):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}",name) or not isinstance(version,str):
        raise RuntimeError("declared tool observation requires a literal platform executable name and pin")
    for parent in (home,home/".local",home/".local/bin"):
        if not stat.S_ISDIR(parent.lstat().st_mode) or parent.resolve()!=parent:
            raise RuntimeError("declared tool directory is missing or redirected")
    executable=home/".local/bin"/name
    metadata=executable.lstat()
    if not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode&0o111 or metadata.st_size>256*1024*1024:
        raise RuntimeError("declared tool is missing, linked, not executable or outside the observation bound")
    digest=hashlib.sha256()
    with executable.open("rb") as source:
        for block in iter(lambda:source.read(1024*1024),b""):
            digest.update(block)
    tools[name]={"declared_version":version,"path":str(executable),"sha256":digest.hexdigest(),"mode":stat.S_IMODE(metadata.st_mode)}
print(json.dumps(tools,sort_keys=True))
`

const realReviewMetricsPython = `
import json,os,pathlib,subprocess,time
root=pathlib.Path(".")
receipt=json.loads((root/".git/143-review-evidence.json").read_text())
evidence=[]
for entry in receipt.get("evidence",[]):
    files=entry.get("files",[])
    evidence.append({"path":entry["path"],"entries":len(files),"path_bytes":sum(len(os.fsencode(item[0])) for item in files),"regular_bytes":sum(item[2] for item in files if item[1] in ("file","resolved_file")),"regular_files":sum(item[1] in ("file","resolved_file") for item in files)})
regular_bytes=regular_files=0
for directory,dirs,files in os.walk(root,followlinks=False):
    dirs[:]=[name for name in dirs if not pathlib.Path(directory,name).is_symlink()]
    for name in files:
        entry=pathlib.Path(directory,name)
        if entry.is_symlink(): continue
        metadata=entry.stat()
        regular_bytes+=metadata.st_size
        regular_files+=1
versions={}
for name,args in {"git":["/usr/bin/git","--version"],"go":["/usr/local/go/bin/go","version"],"node":["/usr/bin/node","--version"],"npm":["/usr/bin/npm","--version"],"python":["/usr/bin/python3","--version"]}.items():
    result=subprocess.run(args,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
    versions[name]={"exit":result.returncode,"version":result.stdout.strip()}
print(json.dumps({"workspace_regular_bytes":regular_bytes,"workspace_regular_files":regular_files,"evidence_manifest":evidence,"tool_versions":versions,"cgroup_cpu_stat":pathlib.Path("/sys/fs/cgroup/cpu.stat").read_text() if pathlib.Path("/sys/fs/cgroup/cpu.stat").exists() else None,"cgroup_memory_peak":pathlib.Path("/sys/fs/cgroup/memory.peak").read_text().strip() if pathlib.Path("/sys/fs/cgroup/memory.peak").exists() else None}))
`
