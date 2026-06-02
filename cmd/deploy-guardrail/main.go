package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/deployguardrail"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "worker-sessions" {
		fmt.Fprintln(os.Stderr, "usage: deploy-guardrail worker-sessions")
		os.Exit(2)
	}

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := db.NewPoolWithOptions(ctx, cfg.DatabaseURL, db.PoolOptions{MaxConns: 1})
	if err != nil {
		if deployguardrail.IsDatabaseConnectionSaturated(err) {
			fmt.Fprintf(os.Stderr, "deploy guardrail: warning: database connection slots are exhausted; skipping active-session check so deploy can roll forward with bounded pools: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "deploy guardrail: connect database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	counts, err := deployguardrail.LoadWorkerSessionCounts(ctx, pool)
	if err != nil {
		if errors.Is(err, deployguardrail.ErrGuardrailSchemaUnavailable) {
			fmt.Fprintf(os.Stderr, "deploy guardrail: warning: %v; skipping active-session check until migrations are applied\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "deploy guardrail: %v\n", err)
		os.Exit(1)
	}

	force := os.Getenv("FORCE_DEPLOY_WITH_ACTIVE_SESSIONS") == "1"
	decision := deployguardrail.EvaluateWorkerSessionDeploy(counts, force)
	fmt.Printf("worker deploy session guardrail: worker_owned_active_long_jobs=%d executor_owned_active_sessions=%d worker_owned_no_checkpoint=%d draining_session_executors=%d force=%t\n",
		counts.WorkerOwnedActiveLongJobs,
		counts.ExecutorOwnedActiveSessions,
		counts.WorkerOwnedNoCheckpoint,
		counts.DrainingSessionExecutorCount,
		force,
	)
	if counts.ExecutorOwnedActiveSessions > 0 {
		fmt.Printf("worker deploy session guardrail: warning: %d executor-owned active session(s) will continue outside the worker container\n", counts.ExecutorOwnedActiveSessions)
	}
	if decision.Blocked {
		fmt.Fprintf(os.Stderr, "worker deploy session guardrail: blocked: %s; set FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=1 to override\n", decision.Reason)
		os.Exit(3)
	}
}
