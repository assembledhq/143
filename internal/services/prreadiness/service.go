package prreadiness

import (
	"context"
	"errors"
	"fmt"

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
}

func (s *Service) EnqueueRun(ctx context.Context, req EnqueueRunRequest) (*models.PRReadinessRun, error) {
	if s == nil || s.store == nil || s.jobs == nil {
		return nil, fmt.Errorf("PR readiness service is not configured")
	}
	sessionID := req.Session.ID
	if req.OrgID == uuid.Nil || sessionID == uuid.Nil {
		return nil, fmt.Errorf("org_id and session_id are required")
	}

	existing, err := s.store.GetLatestBySession(ctx, req.OrgID, sessionID)
	if err == nil && existing != nil && isQueuedOrRunning(existing.Status) {
		return existing, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	run := &models.PRReadinessRun{
		OrgID:                      req.OrgID,
		SessionID:                  sessionID,
		RepositoryID:               req.Session.RepositoryID,
		Status:                     models.PRReadinessRunStatusQueued,
		EvaluatedWorkspaceRevision: req.Session.WorkspaceGeneration,
		Summary:                    "Queued",
		TriggeredByUserID:          req.TriggeredByUserID,
	}
	if req.Session.SnapshotKey != nil && *req.Session.SnapshotKey != "" {
		snapshotKey := *req.Session.SnapshotKey
		run.EvaluatedSnapshotKey = &snapshotKey
	}
	if err := s.store.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	payload := map[string]string{
		"org_id":       req.OrgID.String(),
		"session_id":   sessionID.String(),
		"readiness_id": run.ID.String(),
	}
	dedupeKey := DedupeKey(sessionID)
	if _, err := s.jobs.Enqueue(ctx, req.OrgID, runJobQueue, runJobType, payload, runJobPriority, &dedupeKey); err != nil {
		return nil, err
	}
	return run, nil
}

func DedupeKey(sessionID uuid.UUID) string {
	return "pr_readiness:" + sessionID.String()
}

func isQueuedOrRunning(status models.PRReadinessRunStatus) bool {
	return status == models.PRReadinessRunStatusQueued || status == models.PRReadinessRunStatusRunning
}
