package prreadiness

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	runJobQueue    = "agent"
	runJobType     = "run_pr_readiness"
	runJobPriority = 6
)

type Store interface {
	GetLatestBySession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PRReadinessRun, error)
	CreateRun(ctx context.Context, run *models.PRReadinessRun) error
}

type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type Service struct {
	store Store
	jobs  JobStore
}

func NewService(store Store, jobs JobStore) *Service {
	return &Service{store: store, jobs: jobs}
}

type EnqueueRunRequest struct {
	OrgID             uuid.UUID
	Session           models.Session
	TriggeredByUserID *uuid.UUID
	ChangesetID       *uuid.UUID
	ChangesetHeadSHA  *string
}

func (s *Service) EnqueueRun(ctx context.Context, req EnqueueRunRequest) (*models.PRReadinessRun, error) {
	if s == nil || s.store == nil || s.jobs == nil {
		return nil, fmt.Errorf("PR readiness service is not configured")
	}
	run, _, err := enqueueRunOn(ctx, s.store, s.jobs.Enqueue, req)
	return run, err
}

func EnqueueRunInTx(ctx context.Context, tx pgx.Tx, req EnqueueRunRequest) (*models.PRReadinessRun, uuid.UUID, error) {
	return enqueueRunOn(ctx, db.NewPRReadinessStore(tx), db.NewJobStore(tx).Enqueue, req)
}

func enqueueRunOn(ctx context.Context, store Store, enqueue func(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error), req EnqueueRunRequest) (*models.PRReadinessRun, uuid.UUID, error) {
	sessionID := req.Session.ID
	if req.OrgID == uuid.Nil || sessionID == uuid.Nil {
		return nil, uuid.Nil, fmt.Errorf("org_id and session_id are required")
	}

	var existing *models.PRReadinessRun
	var err error
	if req.ChangesetID != nil {
		if scoped, ok := store.(interface {
			GetLatestByChangeset(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PRReadinessRun, error)
		}); ok {
			existing, err = scoped.GetLatestByChangeset(ctx, req.OrgID, sessionID, *req.ChangesetID)
		} else {
			existing, err = store.GetLatestBySession(ctx, req.OrgID, sessionID)
		}
	} else {
		existing, err = store.GetLatestBySession(ctx, req.OrgID, sessionID)
	}
	if err == nil && existing != nil && isQueuedOrRunning(existing.Status) {
		return existing, uuid.Nil, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, err
	}

	run := &models.PRReadinessRun{
		OrgID:                      req.OrgID,
		SessionID:                  sessionID,
		RepositoryID:               req.Session.RepositoryID,
		Status:                     models.PRReadinessRunStatusQueued,
		EvaluatedWorkspaceRevision: req.Session.WorkspaceRevision,
		Summary:                    "Queued",
		TriggeredByUserID:          req.TriggeredByUserID,
	}
	if req.ChangesetID != nil {
		run.ChangesetID = *req.ChangesetID
		run.EvaluatedHeadSHA = req.ChangesetHeadSHA
	}
	if req.Session.SnapshotKey != nil && *req.Session.SnapshotKey != "" {
		snapshotKey := *req.Session.SnapshotKey
		run.EvaluatedSnapshotKey = &snapshotKey
	}
	if err := store.CreateRun(ctx, run); err != nil {
		return nil, uuid.Nil, err
	}

	payload := map[string]string{
		"org_id":       req.OrgID.String(),
		"session_id":   sessionID.String(),
		"readiness_id": run.ID.String(),
	}
	if run.ChangesetID != uuid.Nil {
		payload["changeset_id"] = run.ChangesetID.String()
	}
	dedupeTarget := run.ChangesetID
	if dedupeTarget == uuid.Nil {
		dedupeTarget = sessionID
	}
	dedupeKey := DedupeKey(dedupeTarget)
	jobID, err := enqueue(ctx, req.OrgID, runJobQueue, runJobType, payload, runJobPriority, &dedupeKey)
	if err != nil {
		return nil, uuid.Nil, err
	}
	return run, jobID, nil
}

func DedupeKey(changesetID uuid.UUID) string {
	return "pr_readiness:" + changesetID.String()
}

func isQueuedOrRunning(status models.PRReadinessRunStatus) bool {
	return status == models.PRReadinessRunStatusQueued || status == models.PRReadinessRunStatusRunning
}
