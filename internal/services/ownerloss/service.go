package ownerloss

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	defaultDeadOwnerTimeout = 90 * time.Second
	defaultReclaimLimit     = 100
)

type SessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

type ExecutorStore interface {
	ReclaimLostForSession(ctx context.Context, orgID, sessionID uuid.UUID, staleBefore time.Time, limit int) (int64, error)
}

type JobStore interface {
	ReclaimLostRunningSessionJobsForSession(ctx context.Context, orgID, sessionID uuid.UUID, staleBefore time.Time, limit int) (int64, error)
}

type Waker interface {
	Wake(ctx context.Context)
}

type Service struct {
	sessions         SessionStore
	executors        ExecutorStore
	jobs             JobStore
	waker            Waker
	logger           zerolog.Logger
	deadOwnerTimeout time.Duration
	reclaimLimit     int
}

type Option func(*Service)

func WithDeadOwnerTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		s.deadOwnerTimeout = timeout
	}
}

func WithReclaimLimit(limit int) Option {
	return func(s *Service) {
		s.reclaimLimit = limit
	}
}

func NewService(sessions SessionStore, executors ExecutorStore, jobs JobStore, waker Waker, logger zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		sessions:         sessions,
		executors:        executors,
		jobs:             jobs,
		waker:            waker,
		logger:           logger,
		deadOwnerTimeout: defaultDeadOwnerTimeout,
		reclaimLimit:     defaultReclaimLimit,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.deadOwnerTimeout <= 0 {
		s.deadOwnerTimeout = defaultDeadOwnerTimeout
	}
	if s.reclaimLimit <= 0 {
		s.reclaimLimit = defaultReclaimLimit
	}
	return s
}

func (s *Service) RecoverLostOwner(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID) error {
	if s == nil {
		return nil
	}
	if s.sessions == nil {
		return fmt.Errorf("owner-loss recovery session store is not configured")
	}
	if s.executors == nil {
		return fmt.Errorf("owner-loss recovery executor store is not configured")
	}
	if s.jobs == nil {
		return fmt.Errorf("owner-loss recovery job store is not configured")
	}

	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("load session for owner-loss recovery: %w", err)
	}

	staleBefore := time.Now().Add(-s.deadOwnerTimeout)
	executorsReclaimed, err := s.executors.ReclaimLostForSession(ctx, orgID, sessionID, staleBefore, s.reclaimLimit)
	if err != nil {
		return fmt.Errorf("reclaim lost executors for session: %w", err)
	}
	jobsReclaimed, err := s.jobs.ReclaimLostRunningSessionJobsForSession(ctx, orgID, sessionID, staleBefore, s.reclaimLimit)
	if err != nil {
		return fmt.Errorf("reclaim lost jobs for session: %w", err)
	}

	total := executorsReclaimed + jobsReclaimed
	if total > 0 && s.waker != nil {
		s.waker.Wake(ctx)
	}

	alreadyQueued := session.RecoveryState == models.RecoveryStateQueued || session.RecoveryState == models.RecoveryStateRecovering
	checkpointAvailable := session.SnapshotKey != nil && *session.SnapshotKey != ""
	event := s.logger.Info().
		Str("org_id", orgID.String()).
		Str("session_id", sessionID.String()).
		Int64("executors_reclaimed", executorsReclaimed).
		Int64("jobs_reclaimed", jobsReclaimed).
		Bool("checkpoint_available", checkpointAvailable).
		Bool("recovery_already_queued", alreadyQueued).
		Str("recovery_state", string(session.RecoveryState))
	if threadID != nil {
		event = event.Str("thread_id", threadID.String())
	}
	event.Msg("proactive owner-loss recovery checked session")
	return nil
}
