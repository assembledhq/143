package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type sessionExecutorDispatcher interface {
	Dispatch(ctx context.Context, jobType string, session models.Session, threadID *uuid.UUID) (uuid.UUID, error)
}

func maybeDispatchSessionExecutor(ctx context.Context, services *Services, jobType string, session models.Session, threadID *uuid.UUID) error {
	if services == nil {
		return nil
	}
	if services.SessionExecutorDispatcher == nil {
		if services.RequireSessionExecutorDispatcher {
			return fmt.Errorf("session executor dispatcher is required for %s job for session %s", jobType, session.ID)
		}
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

type ExecutorLaunchResult struct {
	ContainerID string
}

type ExecutorLauncher interface {
	Launch(ctx context.Context, spec ExecutorLaunchSpec) (ExecutorLaunchResult, error)
	Cleanup(ctx context.Context, spec ExecutorLaunchSpec) error
}

type sessionExecutorLifecycleStore interface {
	ClearPreHandoffReservation(ctx context.Context, orgID, sessionID, jobID uuid.UUID) (int64, error)
	CreateStarting(ctx context.Context, orgID uuid.UUID, params models.CreateSessionExecutorParams) (uuid.UUID, error)
	RecordContainerIDWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, containerID string) (bool, error)
	MarkTerminalWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, status models.SessionExecutorStatus, exitCode *int, lastError string) (bool, error)
}

type sessionExecutorJobHandoffStore interface {
	HandoffToSessionExecutorWithLease(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (bool, error)
}

type DurableSessionExecutorDispatcher struct {
	Executors             sessionExecutorLifecycleStore
	Jobs                  sessionExecutorJobHandoffStore
	Launcher              ExecutorLauncher
	NodeID                string
	Image                 string
	BuildSHA              string
	ResolveRuntimeCeiling func(context.Context, uuid.UUID) time.Duration
	Logger                zerolog.Logger
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

	logger := d.logger().With().
		Str("org_id", session.OrgID.String()).
		Str("session_id", session.ID.String()).
		Str("job_id", jobID.String()).
		Str("job_type", jobType).
		Str("lock_token", lockToken.String()).
		Str("host_node_id", d.NodeID).
		Logger()
	logger.Info().Msg("session executor dispatch starting")
	logSessionExecutorHostResourceSnapshot(ctx, logger, "dispatch_start")

	cleared, err := d.Executors.ClearPreHandoffReservation(ctx, session.OrgID, session.ID, jobID)
	if err != nil {
		logger.Error().Err(err).Msg("session executor pre-handoff cleanup failed")
		return uuid.Nil, err
	}
	if cleared > 0 {
		logger.Warn().Int64("cleared_count", cleared).Msg("cleared stale session executor pre-handoff reservations")
	} else {
		logger.Debug().Msg("no stale session executor pre-handoff reservations found")
	}

	var runtimeDeadlineAt *time.Time
	if d.ResolveRuntimeCeiling != nil {
		deadline := time.Now().UTC().Add(d.ResolveRuntimeCeiling(ctx, session.OrgID) + agent.HandlerCleanupBuffer)
		runtimeDeadlineAt = &deadline
	}

	executorID, err := d.Executors.CreateStarting(ctx, session.OrgID, models.CreateSessionExecutorParams{
		SessionID:         session.ID,
		ThreadID:          threadID,
		JobID:             jobID,
		JobType:           jobType,
		HostNodeID:        d.NodeID,
		OwnerID:           d.NodeID,
		LockToken:         lockToken,
		Image:             d.Image,
		BuildSHA:          d.BuildSHA,
		RuntimeDeadlineAt: runtimeDeadlineAt,
	})
	if err != nil {
		logger.Error().Err(err).Msg("session executor row creation failed")
		return uuid.Nil, err
	}
	logger = logger.With().Str("executor_id", executorID.String()).Logger()
	createEvent := logger.Info()
	if runtimeDeadlineAt != nil {
		createEvent.Time("runtime_deadline_at", *runtimeDeadlineAt)
	}
	createEvent.
		Str("image", d.Image).
		Str("build_sha", d.BuildSHA).
		Msg("session executor row created")

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
	logger.Info().Msg("session executor container launch starting")
	logSessionExecutorHostResourceSnapshot(ctx, logger, "pre_container_launch")
	launchResult, err := d.Launcher.Launch(ctx, spec)
	if err != nil {
		dispatchErr := fmt.Errorf("launch session executor: %w", err)
		logger.Error().Err(dispatchErr).Msg("session executor container launch failed")
		return uuid.Nil, errors.Join(dispatchErr, d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr))
	}
	logger.Info().Str("container_id", launchResult.ContainerID).Msg("session executor container launch returned")
	logSessionExecutorHostResourceSnapshot(ctx, logger, "post_container_launch")
	if launchResult.ContainerID != "" {
		ok, err := d.Executors.RecordContainerIDWithLease(ctx, session.OrgID, executorID, lockToken, launchResult.ContainerID)
		if err != nil {
			dispatchErr := fmt.Errorf("record session executor container id: %w", err)
			logger.Error().Err(dispatchErr).Str("container_id", launchResult.ContainerID).Msg("session executor container id recording failed")
			return uuid.Nil, errors.Join(
				dispatchErr,
				d.cleanupLaunchedExecutor(ctx, spec),
				d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
			)
		}
		if !ok {
			dispatchErr := fmt.Errorf("record session executor container id lost fencing race")
			logger.Warn().Str("container_id", launchResult.ContainerID).Msg("session executor container id recording lost fencing race")
			return uuid.Nil, errors.Join(
				dispatchErr,
				d.cleanupLaunchedExecutor(ctx, spec),
				d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
			)
		}
		logger.Info().Str("container_id", launchResult.ContainerID).Msg("session executor container id recorded")
	} else {
		logger.Warn().Msg("session executor launch returned without a container id")
	}

	logger.Info().Msg("session executor job handoff starting")
	ok, err = d.Jobs.HandoffToSessionExecutorWithLease(ctx, session.OrgID, jobID, lockToken, executorID)
	if err != nil {
		dispatchErr := fmt.Errorf("job handoff failed: %w", err)
		logger.Error().Err(dispatchErr).Msg("session executor job handoff failed")
		return uuid.Nil, errors.Join(
			dispatchErr,
			d.cleanupLaunchedExecutor(ctx, spec),
			d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
		)
	}
	if !ok {
		dispatchErr := fmt.Errorf("job handoff lost fencing race")
		logger.Warn().Msg("session executor job handoff lost fencing race")
		return uuid.Nil, errors.Join(
			dispatchErr,
			d.cleanupLaunchedExecutor(ctx, spec),
			d.markExecutorDispatchFailed(ctx, session.OrgID, executorID, lockToken, dispatchErr),
		)
	}
	logger.Info().Msg("session executor job handoff completed")
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
	logger := d.logger().With().
		Str("org_id", spec.OrgID.String()).
		Str("session_id", spec.SessionID.String()).
		Str("job_id", spec.JobID.String()).
		Str("executor_id", spec.ExecutorID.String()).
		Str("host_node_id", spec.NodeID).
		Logger()
	logger.Info().Msg("cleaning up launched session executor container")
	if err := d.Launcher.Cleanup(cleanupCtx, spec); err != nil {
		logger.Warn().Err(err).Msg("session executor container cleanup failed")
		return fmt.Errorf("cleanup launched executor: %w", err)
	}
	logger.Info().Msg("session executor container cleanup completed")
	return nil
}

func (d *DurableSessionExecutorDispatcher) logger() zerolog.Logger {
	if d.Logger.GetLevel() == zerolog.Disabled {
		return zerolog.Nop()
	}
	return d.Logger
}

func logSessionExecutorHostResourceSnapshot(ctx context.Context, logger zerolog.Logger, phase string) {
	sample, err := (procHostResourceReader{}).ReadHostResourceSample(ctx)
	if err != nil {
		logger.Debug().Err(err).Str("resource_phase", phase).Msg("session executor host resource snapshot unavailable")
		return
	}
	event := logger.Info().
		Str("resource_phase", phase).
		Float64("host_memory_util", sample.memoryUtil)
	if load1, load5, load15, ok := readProcLoadAvg(); ok {
		cpus := runtime.NumCPU()
		event.
			Float64("host_load1", load1).
			Float64("host_load5", load5).
			Float64("host_load15", load15)
		if cpus > 0 {
			event.
				Float64("host_load1_per_cpu", load1/float64(cpus)).
				Float64("host_load5_per_cpu", load5/float64(cpus)).
				Int("host_cpu_count", cpus)
		}
	}
	event.Msg("session executor host resource snapshot")
}

func readProcLoadAvg() (float64, float64, float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	load1, err1 := strconv.ParseFloat(fields[0], 64)
	load5, err5 := strconv.ParseFloat(fields[1], 64)
	load15, err15 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err5 != nil || err15 != nil {
		return 0, 0, 0, false
	}
	return load1, load5, load15, true
}
