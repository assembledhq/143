package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
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
	case "retire-ready":
		status := runStatus(ctx, store, os.Args[2:])
		if !status.RetireReady {
			os.Exit(3)
		}
	case "expire-budget":
		runExpireBudget(ctx, store, db.NewSessionExecutorStore(pool), os.Args[2:])
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

	out := map[string]any{
		"ok":             true,
		"mode":           *mode,
		"host":           *host,
		"node_id":        resolvedNodeID,
		"candidate_port": *candidatePort,
		"build_sha":      *buildSHA,
		"current_node":   status,
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
		fmt.Printf("node=%s status=%s intent=%s fresh_heartbeat=%t active_executors=%d active_previews=%d running_jobs=%d session_holds=%d sandbox_holders=%d endpoint_blockers=%d retire_ready=%t\n",
			status.NodeID, status.Status, status.DrainIntent, status.FreshHeartbeat, status.ActiveExecutorCount, status.ActivePreviewCount, status.OwnedRunningJobCount, status.ActiveSessionHoldCount, status.ActiveSandboxHolderCount, status.EndpointBlockerCount, status.RetireReady)
	default:
		raw, _ := json.Marshal(v)
		fmt.Println(string(raw))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: worker-deployctl preflight|mark-draining|status|retire-ready|expire-budget|force-maintenance [flags]")
}

func exitErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "worker-deployctl: "+format+"\n", args...)
	os.Exit(1)
}
