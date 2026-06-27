package readiness

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

const (
	prReadinessQueue     = "agent"
	prReadinessJobType   = "run_pr_readiness"
	prReadinessPriority  = 6
	prReadinessDedupeKey = "pr_readiness:"
)

type Runner struct {
	readiness *db.PRReadinessStore
	jobs      *db.JobStore
}

func NewRunner(readiness *db.PRReadinessStore, jobs *db.JobStore) *Runner {
	return &Runner{readiness: readiness, jobs: jobs}
}

func (r *Runner) EnqueueRun(ctx context.Context, orgID uuid.UUID, session models.Session, triggeredByUserID *uuid.UUID) (*models.PRReadinessRun, error) {
	if r.readiness == nil || r.jobs == nil {
		return nil, errors.New("PR readiness runner is not configured")
	}
	run, _, err := enqueueRunOn(ctx, r.readiness, func(ctx context.Context, orgID uuid.UUID, run models.PRReadinessRun) (uuid.UUID, error) {
		dedupeKey := PRReadinessDedupeKey(session.ID)
		return r.jobs.Enqueue(ctx, orgID, prReadinessQueue, prReadinessJobType, PRReadinessPayload(orgID, session.ID, run.ID), prReadinessPriority, &dedupeKey)
	}, orgID, session, triggeredByUserID)
	return run, err
}

func EnqueueRunInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, session models.Session, triggeredByUserID *uuid.UUID) (*models.PRReadinessRun, uuid.UUID, error) {
	readinessStore := db.NewPRReadinessStore(tx)
	jobStore := db.NewJobStore(tx)
	return enqueueRunOn(ctx, readinessStore, func(ctx context.Context, orgID uuid.UUID, run models.PRReadinessRun) (uuid.UUID, error) {
		dedupeKey := PRReadinessDedupeKey(session.ID)
		return jobStore.EnqueueInTxWithOpts(ctx, tx, orgID, db.EnqueueOpts{
			Queue:     prReadinessQueue,
			JobType:   prReadinessJobType,
			Payload:   PRReadinessPayload(orgID, session.ID, run.ID),
			Priority:  prReadinessPriority,
			DedupeKey: &dedupeKey,
		})
	}, orgID, session, triggeredByUserID)
}

func enqueueRunOn(ctx context.Context, store *db.PRReadinessStore, enqueue func(context.Context, uuid.UUID, models.PRReadinessRun) (uuid.UUID, error), orgID uuid.UUID, session models.Session, triggeredByUserID *uuid.UUID) (*models.PRReadinessRun, uuid.UUID, error) {
	existing, err := store.GetLatestBySession(ctx, orgID, session.ID)
	if err == nil && existing != nil && (existing.Status == models.PRReadinessRunStatusQueued || existing.Status == models.PRReadinessRunStatusRunning) {
		return existing, uuid.Nil, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, err
	}

	run := &models.PRReadinessRun{
		OrgID:                      orgID,
		SessionID:                  session.ID,
		RepositoryID:               session.RepositoryID,
		Status:                     models.PRReadinessRunStatusQueued,
		EvaluatedWorkspaceRevision: session.WorkspaceGeneration,
		Summary:                    "Queued",
		TriggeredByUserID:          triggeredByUserID,
	}
	if session.SnapshotKey != nil && *session.SnapshotKey != "" {
		snapshotKey := *session.SnapshotKey
		run.EvaluatedSnapshotKey = &snapshotKey
	}
	if err := store.CreateRun(ctx, run); err != nil {
		return nil, uuid.Nil, err
	}
	jobID, err := enqueue(ctx, orgID, *run)
	if err != nil {
		return nil, uuid.Nil, err
	}
	return run, jobID, nil
}

func PRReadinessDedupeKey(sessionID uuid.UUID) string {
	return prReadinessDedupeKey + sessionID.String()
}

func PRReadinessPayload(orgID, sessionID, runID uuid.UUID) map[string]string {
	return map[string]string{
		"org_id":       orgID.String(),
		"session_id":   sessionID.String(),
		"readiness_id": runID.String(),
	}
}
