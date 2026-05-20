package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

type sessionExecutorDispatcher interface {
	Dispatch(ctx context.Context, jobType string, session models.Session, threadID *uuid.UUID) (uuid.UUID, error)
}

func maybeDispatchSessionExecutor(ctx context.Context, services *Services, jobType string, session models.Session, threadID *uuid.UUID) error {
	if services == nil || services.SessionExecutorDispatcher == nil {
		return nil
	}
	executorID, err := services.SessionExecutorDispatcher.Dispatch(ctx, jobType, session, threadID)
	if err != nil {
		return fmt.Errorf("dispatch session executor: %w", err)
	}
	return &HandoffError{Err: fmt.Errorf("session executor %s owns %s job for session %s", executorID, jobType, session.ID)}
}

type ExecutorLaunchSpec struct {
	ExecutorID uuid.UUID
	OrgID      uuid.UUID
	SessionID  uuid.UUID
	ThreadID   *uuid.UUID
	JobID      uuid.UUID
	JobType    string
	LockToken  uuid.UUID
	Image      string
	BuildSHA   string
	NodeID     string
}

type ExecutorLauncher interface {
	Launch(ctx context.Context, spec ExecutorLaunchSpec) error
	Cleanup(ctx context.Context, spec ExecutorLaunchSpec) error
}

type sessionExecutorLifecycleStore interface {
	CreateStarting(ctx context.Context, orgID uuid.UUID, params models.CreateSessionExecutorParams) (uuid.UUID, error)
	MarkTerminalWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, status models.SessionExecutorStatus, exitCode *int, lastError string) (bool, error)
}

type sessionExecutorJobHandoffStore interface {
	HandoffToSessionExecutorWithLease(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (bool, error)
}

type DurableSessionExecutorDispatcher struct {
	Executors sessionExecutorLifecycleStore
	Jobs      sessionExecutorJobHandoffStore
	Launcher  ExecutorLauncher
	NodeID    string
	Image     string
	BuildSHA  string
}

func (d *DurableSessionExecutorDispatcher) Dispatch(ctx context.Context, jobType string, session models.Session, threadID *uuid.UUID) (uuid.UUID, error) {
	if d == nil {
		return uuid.Nil, fmt.Errorf("session executor dispatcher is nil")
	}
	if d.Executors == nil {
		return uuid.Nil, fmt.Errorf("session executor store is not configured")
	}
	if d.Jobs == nil {
		return uuid.Nil, fmt.Errorf("job store is not configured")
	}
	if d.Launcher == nil {
		return uuid.Nil, fmt.Errorf("executor launcher is not configured")
	}
	jobID, ok := jobctx.JobIDFromContext(ctx)
	if !ok || jobID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("job id missing from context")
	}
	lockToken, ok := jobctx.LockTokenFromContext(ctx)
	if !ok || lockToken == uuid.Nil {
		return uuid.Nil, fmt.Errorf("lock token missing from context")
	}

	executorID, err := d.Executors.CreateStarting(ctx, session.OrgID, models.CreateSessionExecutorParams{
		SessionID:  session.ID,
		ThreadID:   threadID,
		JobID:      jobID,
		JobType:    jobType,
		HostNodeID: d.NodeID,
		OwnerID:    d.NodeID,
		LockToken:  lockToken,
		Image:      d.Image,
		BuildSHA:   d.BuildSHA,
	})
	if err != nil {
		return uuid.Nil, err
	}

	spec := ExecutorLaunchSpec{
		ExecutorID: executorID,
		OrgID:      session.OrgID,
		SessionID:  session.ID,
		ThreadID:   threadID,
		JobID:      jobID,
		JobType:    jobType,
		LockToken:  lockToken,
		Image:      d.Image,
		BuildSHA:   d.BuildSHA,
		NodeID:     d.NodeID,
	}
	if err := d.Launcher.Launch(ctx, spec); err != nil {
		dispatchErr := fmt.Errorf("launch session executor: %w", err)
		return uuid.Nil, errors.Join(dispatchErr, d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr))
	}

	ok, err = d.Jobs.HandoffToSessionExecutorWithLease(ctx, session.OrgID, jobID, lockToken, executorID)
	if err != nil {
		dispatchErr := fmt.Errorf("job handoff failed: %w", err)
		return uuid.Nil, errors.Join(
			dispatchErr,
			d.cleanupLaunchedExecutor(ctx, spec),
			d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
		)
	}
	if !ok {
		dispatchErr := fmt.Errorf("job handoff lost fencing race")
		return uuid.Nil, errors.Join(
			dispatchErr,
			d.cleanupLaunchedExecutor(ctx, spec),
			d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
		)
	}
	return executorID, nil
}

func (d *DurableSessionExecutorDispatcher) markExecutorDispatchFailed(ctx context.Context, orgID, executorID, lockToken uuid.UUID, cause error) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	exitCode := 1
	if ok, err := d.Executors.MarkTerminalWithLease(writeCtx, orgID, executorID, lockToken, models.SessionExecutorStatusFailed, &exitCode, cause.Error()); err != nil {
		return fmt.Errorf("mark executor dispatch failed: %w", err)
	} else if !ok {
		return fmt.Errorf("mark executor dispatch failed: lost fencing")
	}
	return nil
}

func (d *DurableSessionExecutorDispatcher) cleanupLaunchedExecutor(ctx context.Context, spec ExecutorLaunchSpec) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := d.Launcher.Cleanup(cleanupCtx, spec); err != nil {
		return fmt.Errorf("cleanup launched executor: %w", err)
	}
	return nil
}
