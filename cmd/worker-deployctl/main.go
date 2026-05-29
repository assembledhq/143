package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		exitErr("connect database: %v", err)
	}
	defer pool.Close()

	store := db.NewNodeStore(pool)
	switch os.Args[1] {
	case "preflight":
		runPreflight(ctx, store, os.Args[2:])
	case "mark-draining":
		runMarkDraining(ctx, store, os.Args[2:], models.DrainIntentPlannedRollout)
	case "force-maintenance":
		runMarkDraining(ctx, store, os.Args[2:], models.DrainIntentHostMaintenance)
	case "status":
		runStatus(ctx, store, os.Args[2:])
	case "impact":
		runImpact(ctx, store, os.Args[2:])
	case "retire-ready":
		status := runStatus(ctx, store, os.Args[2:])
		if !status.RetireReady {
			os.Exit(3)
		}
	case "expire-budget":
		runExpireBudget(ctx, store, db.NewSessionExecutorStore(pool), os.Args[2:])
	case "extend-drain":
		runExtendDrain(ctx, store, os.Args[2:])
	case "retain-images":
		runRetainImages(ctx, store, os.Args[2:])
	case "release-retained-images":
		runReleaseRetainedImages(ctx, store, os.Args[2:])
	case "wave":
		runWave(ctx, db.NewWorkerDeployWaveStore(pool), os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func runExpireBudget(ctx context.Context, nodes *db.NodeStore, executors *db.SessionExecutorStore, args []string) {
	fs := flag.NewFlagSet("expire-budget", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "node id")
	deployID := fs.String("deploy-id", "", "deploy id")
	reason := fs.String("reason", "planned rollout drain budget expired", "budget expiry reason")
	requestedBy := fs.String("requested-by", "", "requesting operator or system")
	buildSHA := fs.String("build-sha", "", "build sha")
	graceWindow := fs.Duration("grace-window", 30*time.Second, "graceful checkpoint window")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	if *nodeID == "" {
		exitErr("--node-id is required")
	}
	updated, err := executors.MarkDeployBudgetExpiredByNode(ctx, *nodeID, time.Now().UTC(), *graceWindow)
	if err != nil {
		exitErr("%v", err)
	}
	if updated > 0 {
		if err := nodes.MarkDraining(ctx, db.MarkNodeDrainingParams{
			NodeID:      *nodeID,
			Intent:      models.DrainIntentDeployBudgetExpired,
			DeployID:    *deployID,
			Reason:      *reason,
			RequestedBy: *requestedBy,
			BuildSHA:    *buildSHA,
			Metadata: map[string]any{
				"command":           "expire-budget",
				"grace_window":      graceWindow.String(),
				"updated_executors": updated,
			},
		}); err != nil {
			exitErr("%v", err)
		}
	}
	status, err := nodes.WorkerDeployStatus(ctx, *nodeID)
	if err != nil {
		exitErr("load worker status after budget expiry: %v", err)
	}
	out := map[string]any{
		"updated_executors": updated,
		"status":            status,
	}
	writeOutput(out, *jsonOut)
}

func runPreflight(ctx context.Context, store *db.NodeStore, args []string) {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	mode := fs.String("mode", "routine", "deploy mode: routine, maintenance, emergency")
	host := fs.String("host", "", "worker host")
	nodeID := fs.String("node-id", "", "current worker node id")
	candidatePort := fs.String("candidate-port", "", "candidate worker host port")
	buildSHA := fs.String("build-sha", "", "candidate build sha")
	expectedSchemaVersion := fs.Int("expected-schema-version", 0, "minimum schema migration version required by the candidate")
	supportFingerprint := fs.String("support-services-fingerprint", "", "candidate support-service config fingerprint")
	expectedSupportFingerprint := fs.String("expected-support-services-fingerprint", "", "currently active support-service config fingerprint")
	freeMemoryMB := fs.Int("free-memory-mb", -1, "observed free memory on the host")
	minFreeMemoryMB := fs.Int("min-free-memory-mb", 0, "minimum free memory required for temporary worker overlap")
	idleCPUMillis := fs.Int("idle-cpu-millis", -1, "observed idle CPU budget on the host in millicores")
	minIdleCPUMillis := fs.Int("min-idle-cpu-millis", 0, "minimum idle CPU budget required for temporary worker overlap")
	includeImpact := fs.Bool("include-impact", false, "include affected runtime identities")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	if *mode != "routine" && *mode != "maintenance" && *mode != "emergency" {
		exitErr("invalid deploy mode %q", *mode)
	}
	if *host == "" && *nodeID == "" {
		exitErr("--node-id or --host is required")
	}
	if *mode == "routine" && *candidatePort == "" {
		exitErr("routine worker deploy requires an explicit safe candidate port")
	}

	resolvedNodeID := *nodeID
	if resolvedNodeID == "" {
		node, err := store.GetLatestByHost(ctx, *host)
		if err != nil {
			exitErr("load latest host node: %v", err)
		}
		resolvedNodeID = node.ID
	}
	status, err := store.WorkerDeployStatus(ctx, resolvedNodeID)
	if err != nil {
		exitErr("load worker status: %v", err)
	}
	if *expectedSchemaVersion > 0 {
		version, dirty, err := store.MigrationVersion(ctx)
		if err != nil {
			exitErr("load schema version: %v", err)
		}
		if dirty || version < *expectedSchemaVersion {
			exitErr("schema version %d dirty=%t is not compatible with candidate requiring >= %d", version, dirty, *expectedSchemaVersion)
		}
	}
	if *mode == "routine" && *expectedSupportFingerprint != "" && *supportFingerprint != "" && *expectedSupportFingerprint != *supportFingerprint {
		exitErr("support-service config fingerprint changed during routine deploy; run maintenance mode (current=%s candidate=%s)", *expectedSupportFingerprint, *supportFingerprint)
	}
	if *mode == "routine" && *minFreeMemoryMB > 0 && (*freeMemoryMB < 0 || *freeMemoryMB < *minFreeMemoryMB) {
		exitErr("insufficient free memory for worker overlap: free_memory_mb=%d min_free_memory_mb=%d", *freeMemoryMB, *minFreeMemoryMB)
	}
	if *mode == "routine" && *minIdleCPUMillis > 0 && (*idleCPUMillis < 0 || *idleCPUMillis < *minIdleCPUMillis) {
		exitErr("insufficient idle CPU for worker overlap: idle_cpu_millis=%d min_idle_cpu_millis=%d", *idleCPUMillis, *minIdleCPUMillis)
	}

	out := map[string]any{
		"ok":                           true,
		"mode":                         *mode,
		"host":                         *host,
		"node_id":                      resolvedNodeID,
		"candidate_port":               *candidatePort,
		"build_sha":                    *buildSHA,
		"current_node":                 status,
		"free_memory_mb":               *freeMemoryMB,
		"min_free_memory_mb":           *minFreeMemoryMB,
		"idle_cpu_millis":              *idleCPUMillis,
		"min_idle_cpu_millis":          *minIdleCPUMillis,
		"support_services_fingerprint": *supportFingerprint,
		"expected_schema_version":      *expectedSchemaVersion,
	}
	if *includeImpact {
		impact, err := store.WorkerDeployImpact(ctx, resolvedNodeID)
		if err != nil {
			exitErr("load worker impact: %v", err)
		}
		out["impact"] = impact
	}
	writeOutput(out, *jsonOut)
}

func runMarkDraining(ctx context.Context, store *db.NodeStore, args []string, defaultIntent models.DrainIntent) {
	fs := flag.NewFlagSet("mark-draining", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "node id")
	intentRaw := fs.String("intent", string(defaultIntent), "drain intent")
	deployID := fs.String("deploy-id", "", "deploy id")
	reason := fs.String("reason", "", "drain reason")
	requestedBy := fs.String("requested-by", "", "requesting operator or system")
	buildSHA := fs.String("build-sha", "", "build sha")
	force := fs.Bool("force", false, "allow maintenance/emergency drain while runtimes are active")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	if *nodeID == "" {
		exitErr("--node-id is required")
	}
	intent := models.DrainIntent(*intentRaw)
	if err := intent.Validate(); err != nil {
		exitErr("%v", err)
	}
	if intent == models.DrainIntentHostMaintenance || intent == models.DrainIntentEmergencyForce {
		if *reason == "" || *requestedBy == "" {
			exitErr("maintenance and emergency drains require --reason and --requested-by")
		}
		status, err := store.WorkerDeployStatus(ctx, *nodeID)
		if err != nil {
			exitErr("load worker status before force drain: %v", err)
		}
		active := status.ActiveExecutorCount + status.ActivePreviewCount + status.OwnedRunningJobCount + status.ActiveSessionHoldCount + status.ActiveSandboxHolderCount + status.EndpointBlockerCount
		if active > 0 && !*force && os.Getenv("FORCE_INTERRUPT_ACTIVE_RUNTIMES") != "1" {
			exitErr("maintenance/emergency drain would affect active runtimes; pass --force or FORCE_INTERRUPT_ACTIVE_RUNTIMES=1 with --reason and --requested-by (executors=%d previews=%d running_jobs=%d session_holds=%d sandbox_holders=%d endpoint_blockers=%d)",
				status.ActiveExecutorCount,
				status.ActivePreviewCount,
				status.OwnedRunningJobCount,
				status.ActiveSessionHoldCount,
				status.ActiveSandboxHolderCount,
				status.EndpointBlockerCount)
		}
	}
	if err := store.MarkDraining(ctx, db.MarkNodeDrainingParams{
		NodeID:      *nodeID,
		Intent:      intent,
		DeployID:    *deployID,
		Reason:      *reason,
		RequestedBy: *requestedBy,
		BuildSHA:    *buildSHA,
		Metadata: map[string]any{
			"command": os.Args[1],
		},
	}); err != nil {
		exitErr("%v", err)
	}
	status, err := store.WorkerDeployStatus(ctx, *nodeID)
	if err != nil {
		exitErr("load worker status after mark-draining: %v", err)
	}
	writeOutput(status, *jsonOut)
}

func runStatus(ctx context.Context, store *db.NodeStore, args []string) db.WorkerDeployStatus {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "node id")
	host := fs.String("host", "", "worker host")
	requireFresh := fs.Bool("require-fresh", false, "fail unless the node has a fresh DB heartbeat")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	resolvedNodeID := *nodeID
	if resolvedNodeID == "" {
		if *host == "" {
			exitErr("--node-id or --host is required")
		}
		node, err := store.GetLatestByHost(ctx, *host)
		if err != nil {
			exitErr("load latest host node: %v", err)
		}
		resolvedNodeID = node.ID
	}
	status, err := store.WorkerDeployStatus(ctx, resolvedNodeID)
	if err != nil {
		exitErr("%v", err)
	}
	if *requireFresh && !status.FreshHeartbeat {
		exitErr("node %s does not have a fresh database heartbeat", resolvedNodeID)
	}
	writeOutput(status, *jsonOut)
	return status
}

func runImpact(ctx context.Context, store *db.NodeStore, args []string) {
	fs := flag.NewFlagSet("impact", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "node id")
	host := fs.String("host", "", "worker host")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	resolvedNodeID := *nodeID
	if resolvedNodeID == "" {
		if *host == "" {
			exitErr("--node-id or --host is required")
		}
		node, err := store.GetLatestByHost(ctx, *host)
		if err != nil {
			exitErr("load latest host node: %v", err)
		}
		resolvedNodeID = node.ID
	}
	impact, err := store.WorkerDeployImpact(ctx, resolvedNodeID)
	if err != nil {
		exitErr("%v", err)
	}
	writeOutput(impact, *jsonOut)
}

func runExtendDrain(ctx context.Context, store *db.NodeStore, args []string) {
	fs := flag.NewFlagSet("extend-drain", flag.ExitOnError)
	orgIDRaw := fs.String("org-id", "", "org id")
	sessionIDRaw := fs.String("session-id", "", "session id")
	threadIDRaw := fs.String("thread-id", "", "thread id")
	nodeID := fs.String("node-id", "", "node id")
	deployID := fs.String("deploy-id", "", "deploy id")
	requestedBy := fs.String("requested-by", "", "requesting operator")
	reason := fs.String("reason", "", "extension reason")
	extendFor := fs.Duration("extend-for", 0, "extension duration")
	extendUntilRaw := fs.String("extend-until", "", "absolute RFC3339 extension deadline")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	orgID, err := uuid.Parse(*orgIDRaw)
	if err != nil {
		exitErr("--org-id must be a uuid: %v", err)
	}
	sessionID, err := uuid.Parse(*sessionIDRaw)
	if err != nil {
		exitErr("--session-id must be a uuid: %v", err)
	}
	var threadID *uuid.UUID
	if *threadIDRaw != "" {
		parsed, err := uuid.Parse(*threadIDRaw)
		if err != nil {
			exitErr("--thread-id must be a uuid: %v", err)
		}
		threadID = &parsed
	}
	extendUntil := time.Time{}
	if *extendUntilRaw != "" {
		extendUntil, err = time.Parse(time.RFC3339, *extendUntilRaw)
		if err != nil {
			exitErr("--extend-until must be RFC3339: %v", err)
		}
	} else if *extendFor > 0 {
		extendUntil = time.Now().UTC().Add(*extendFor)
	}
	if err := store.ExtendSessionDrain(ctx, orgID, sessionID, threadID, *nodeID, *deployID, *requestedBy, *reason, extendUntil); err != nil {
		exitErr("%v", err)
	}
	writeOutput(map[string]any{"ok": true, "extend_until": extendUntil}, *jsonOut)
}

func runRetainImages(ctx context.Context, store *db.NodeStore, args []string) {
	fs := flag.NewFlagSet("retain-images", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "node id")
	deployID := fs.String("deploy-id", "", "deploy id")
	reason := fs.String("reason", "active executor image retention", "retention reason")
	retainFor := fs.Duration("retain-for", 24*time.Hour, "retention duration")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	retained, err := store.RetainActiveExecutorImages(ctx, db.RetainWorkerImagesParams{
		NodeID:    *nodeID,
		DeployID:  *deployID,
		Reason:    *reason,
		ExpiresAt: time.Now().UTC().Add(*retainFor),
	})
	if err != nil {
		exitErr("%v", err)
	}
	writeOutput(map[string]any{"retained_images": retained}, *jsonOut)
}

func runReleaseRetainedImages(ctx context.Context, store *db.NodeStore, args []string) {
	fs := flag.NewFlagSet("release-retained-images", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	released, err := store.ReleaseExpiredImageRetention(ctx, time.Now().UTC())
	if err != nil {
		exitErr("%v", err)
	}
	writeOutput(map[string]any{"released_retention_rows": released}, *jsonOut)
}

func runWave(ctx context.Context, store *db.WorkerDeployWaveStore, args []string) {
	if len(args) == 0 {
		exitErr("wave requires a subcommand: create|pause")
	}
	switch args[0] {
	case "create":
		runWaveCreate(ctx, store, args[1:])
	case "pause":
		runWavePause(ctx, store, args[1:])
	default:
		exitErr("unknown wave subcommand %q", args[0])
	}
}

func runWaveCreate(ctx context.Context, store *db.WorkerDeployWaveStore, args []string) {
	fs := flag.NewFlagSet("wave create", flag.ExitOnError)
	waveID := fs.String("wave-id", "", "wave id")
	mode := fs.String("mode", "routine", "deploy mode")
	buildSHA := fs.String("build-sha", "", "build sha")
	region := fs.String("region", "", "region")
	bucket := fs.String("bucket", "", "bucket")
	requestedBy := fs.String("requested-by", "", "requesting operator")
	reason := fs.String("reason", "", "reason")
	maxConcurrent := fs.String("max-concurrent", "1", "max concurrent hosts")
	canaryCount := fs.String("canary-count", "1", "canary host count")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	maxConcurrentInt, err := strconv.Atoi(*maxConcurrent)
	if err != nil {
		exitErr("--max-concurrent must be numeric: %v", err)
	}
	canaryCountInt, err := strconv.Atoi(*canaryCount)
	if err != nil {
		exitErr("--canary-count must be numeric: %v", err)
	}
	wave, err := store.Create(ctx, db.CreateWorkerDeployWaveParams{
		ID:            *waveID,
		Mode:          *mode,
		BuildSHA:      *buildSHA,
		Region:        *region,
		Bucket:        *bucket,
		RequestedBy:   *requestedBy,
		Reason:        *reason,
		MaxConcurrent: maxConcurrentInt,
		CanaryCount:   canaryCountInt,
		Metadata: map[string]any{
			"command": "wave create",
		},
	})
	if err != nil {
		exitErr("%v", err)
	}
	writeOutput(wave, *jsonOut)
}

func runWavePause(ctx context.Context, store *db.WorkerDeployWaveStore, args []string) {
	fs := flag.NewFlagSet("wave pause", flag.ExitOnError)
	waveID := fs.String("wave-id", "", "wave id")
	reason := fs.String("reason", "", "pause reason")
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	if err := store.Pause(ctx, *waveID, *reason); err != nil {
		exitErr("%v", err)
	}
	writeOutput(map[string]any{"ok": true, "wave_id": *waveID, "status": "paused"}, *jsonOut)
}

func writeOutput(v any, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			exitErr("encode JSON: %v", err)
		}
		return
	}
	switch status := v.(type) {
	case db.WorkerDeployStatus:
		fmt.Printf("node=%s status=%s intent=%s fresh_heartbeat=%t active_executors=%d active_previews=%d running_jobs=%d session_holds=%d sandbox_holders=%d endpoint_blockers=%d pending_snapshot_uploads=%d detached_cleanup_jobs=%d retire_ready=%t\n",
			status.NodeID, status.Status, status.DrainIntent, status.FreshHeartbeat, status.ActiveExecutorCount, status.ActivePreviewCount, status.OwnedRunningJobCount, status.ActiveSessionHoldCount, status.ActiveSandboxHolderCount, status.EndpointBlockerCount, status.PendingSnapshotUploadCount, status.DetachedCleanupJobCount, status.RetireReady)
	case db.WorkerDeployImpact:
		fmt.Printf("node=%s impacted_runtimes=%d\n", status.NodeID, len(status.Items))
	default:
		raw, _ := json.Marshal(v)
		fmt.Println(string(raw))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: worker-deployctl preflight|mark-draining|status|impact|retire-ready|expire-budget|extend-drain|retain-images|release-retained-images|force-maintenance|wave [flags]")
}

func exitErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "worker-deployctl: "+format+"\n", args...)
	os.Exit(1)
}
