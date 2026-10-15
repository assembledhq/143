package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrNodeRetirementBusy denotes positively verified work, not a failed or
// incomplete observation. Callers may wait on busy; other errors fail closed.
var ErrNodeRetirementBusy = errors.New("worker generation still owns work")

type SealNodeRetirementParams struct {
	NodeID   string
	DeployID string
	BuildSHA string
}

type NodeRetirementSeal struct {
	NodeID   string    `json:"node_id"`
	DeployID string    `json:"deploy_id"`
	BuildSHA string    `json:"build_sha"`
	SealedAt time.Time `json:"sealed_at"`
	Sealed   bool      `json:"sealed"`
}

type NodeRetirementStore struct{ db TxStarter }

func NewNodeRetirementStore(db TxStarter) *NodeRetirementStore {
	return &NodeRetirementStore{db: db}
}

// Seal permanently closes admission to one drained process generation. It does
// not stop containers or revoke existing owners. The seal survives node GC.
// lint:allow-no-orgid reason="worker retirement admission is cluster-scoped across all tenant owners"
func (s *NodeRetirementStore) Seal(ctx context.Context, params SealNodeRetirementParams) (NodeRetirementSeal, error) {
	if strings.TrimSpace(params.NodeID) == "" || strings.TrimSpace(params.DeployID) == "" || strings.TrimSpace(params.BuildSHA) == "" {
		return NodeRetirementSeal{}, fmt.Errorf("retirement seal requires node id, deploy id and build sha")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("begin retirement seal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`); err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("set retirement isolation: %w", err)
	}
	// Positive reconciliation is read-only and remains possible after node GC.
	// Immutability makes an already committed exact seal sufficient evidence.
	if seal, found, err := readNodeRetirementSeal(ctx, tx, params); err != nil || found {
		return seal, err
	}
	// Node control writes already acquire this row before their admission
	// trigger. Follow that order so a concurrent resume/intent change cannot
	// invalidate the drain proof. NO KEY UPDATE also lets admissions already
	// holding the shared advisory lock finish FK KEY SHARE checks on this row;
	// FOR UPDATE would invert those locks while we await exclusive admission.
	// Never lock job/preview ownership rows below.
	var lockedNodeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM nodes WHERE id=$1 FOR NO KEY UPDATE`, params.NodeID).Scan(&lockedNodeID); err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("lock exact retirement generation: %w", err)
	}
	// Keep this lock in its own statement: a CTE sharing the observation's
	// statement snapshot can miss admissions committed while the lock waits.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('worker-retirement:' || $1::text, 0))`, params.NodeID); err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("lock retirement admission: %w", err)
	}
	if seal, found, err := readNodeRetirementSeal(ctx, tx, params); err != nil || found {
		return seal, err
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT
		n.status='draining' AND n.drain_intent='planned_rollout'
		AND n.mode IN ('worker','all')
		AND n.last_heartbeat_at BETWEEN clock_timestamp()-interval '60 seconds' AND clock_timestamp()+interval '5 seconds'
		AND n.drain_requested_at IS NOT NULL
		AND EXISTS (
			SELECT 1 FROM worker_deploy_events e WHERE e.id=(
				SELECT id FROM worker_deploy_events WHERE node_id=n.id AND event_type='node_marked_draining' ORDER BY id DESC LIMIT 1
			) AND e.deploy_id=$2 AND e.build_sha=$3 AND e.drain_intent='planned_rollout'
			  AND e.created_at >= n.drain_requested_at
		)
		FROM nodes n WHERE n.id=$1`, params.NodeID, params.DeployID, params.BuildSHA).Scan(&eligible)
	if err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("verify exact retirement generation: %w", err)
	}
	if !eligible {
		return NodeRetirementSeal{}, fmt.Errorf("retirement generation lacks fresh planned drain for this deployment")
	}
	status, err := NewNodeStore(tx).WorkerDeployStatus(ctx, params.NodeID)
	if err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("observe retirement owners: %w", err)
	}
	// Lease expiry is not physical retirement: an expired runtime may still be
	// serving traffic or a startup may still finish. Count all durable owners.
	var previewOwners int64
	err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM preview_instances WHERE worker_node_id=$1 AND status IN ('starting','ready','partially_ready','unhealthy')) +
		(SELECT count(*) FROM preview_runtimes WHERE worker_node_id=$1 AND status IN ('starting','ready','draining'))`, params.NodeID).Scan(&previewOwners)
	if err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("observe lease-independent preview owners: %w", err)
	}
	if status.Status != models.NodeStatusDraining || status.DrainIntent != models.DrainIntentPlannedRollout {
		return NodeRetirementSeal{}, fmt.Errorf("retirement generation changed drain intent")
	}
	if !status.RetireReady || previewOwners != 0 {
		return NodeRetirementSeal{}, ErrNodeRetirementBusy
	}
	var seal NodeRetirementSeal
	err = tx.QueryRow(ctx, `INSERT INTO worker_retirement_seals(node_id,deploy_id,build_sha) VALUES($1,$2,$3)
		RETURNING node_id,deploy_id,build_sha,sealed_at`, params.NodeID, params.DeployID, params.BuildSHA).
		Scan(&seal.NodeID, &seal.DeployID, &seal.BuildSHA, &seal.SealedAt)
	if err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("persist retirement seal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return NodeRetirementSeal{}, fmt.Errorf("commit retirement seal (outcome unknown): %w", err)
	}
	seal.Sealed = true
	return seal, nil
}

func readNodeRetirementSeal(ctx context.Context, q DBTX, params SealNodeRetirementParams) (NodeRetirementSeal, bool, error) {
	var seal NodeRetirementSeal
	err := q.QueryRow(ctx, `SELECT node_id,deploy_id,build_sha,sealed_at FROM worker_retirement_seals WHERE node_id=$1`, params.NodeID).
		Scan(&seal.NodeID, &seal.DeployID, &seal.BuildSHA, &seal.SealedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeRetirementSeal{}, false, nil
	}
	if err != nil {
		return NodeRetirementSeal{}, false, fmt.Errorf("read retirement seal: %w", err)
	}
	if seal.DeployID != params.DeployID || seal.BuildSHA != params.BuildSHA {
		return NodeRetirementSeal{}, false, fmt.Errorf("retirement seal belongs to a different deployment attempt")
	}
	seal.Sealed = true
	return seal, true, nil
}
