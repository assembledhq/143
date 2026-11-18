package worker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/models"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type executorContainerGCClient interface {
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	ContainerRemove(context.Context, string, container.RemoveOptions) error
}

type executorContainerRetentionStore interface {
	ListTerminalContainersByIdentity(context.Context, []models.SessionExecutorIdentity, time.Time) ([]models.SessionExecutorContainerRetention, error)
}

type ExecutorContainerGCConfig struct {
	Interval  time.Duration
	Retention time.Duration
	BatchSize int
	Timeout   time.Duration
}

// ExecutorContainerGC removes only old, stopped executor containers with
// matching durable terminal evidence. It never removes volumes or images.
// Deployment image pruning must preserve images independently of container refs.
type ExecutorContainerGC struct {
	client executorContainerGCClient
	store  executorContainerRetentionStore
	cfg    ExecutorContainerGCConfig
	logger zerolog.Logger
	mu     sync.Mutex
	cursor string
}

func NewExecutorContainerGC(client executorContainerGCClient, store executorContainerRetentionStore, cfg ExecutorContainerGCConfig, logger zerolog.Logger) (*ExecutorContainerGC, error) {
	if client == nil || store == nil {
		return nil, fmt.Errorf("executor container retention requires Docker and database clients")
	}
	if cfg.Interval <= 0 || cfg.Retention <= 0 || cfg.BatchSize <= 0 || cfg.BatchSize > 1000 || cfg.Timeout <= 0 {
		return nil, fmt.Errorf("executor container retention requires positive durations and a batch size between 1 and 1000")
	}
	return &ExecutorContainerGC{client: client, store: store, cfg: cfg, logger: logger}, nil
}

func (g *ExecutorContainerGC) Run(ctx context.Context) {
	ticker := time.NewTicker(g.cfg.Interval)
	defer ticker.Stop()
	g.run(ctx, ticker.C)
}

func (g *ExecutorContainerGC) run(ctx context.Context, ticks <-chan time.Time) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := g.Reap(ctx, time.Now()); err != nil {
			g.logger.Warn().Err(err).Msg("executor container retention pass failed; later passes will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// Reap makes bounded progress through local Docker inventory, including old
// worker generations. A rotating immutable-ID cursor prevents ambiguous rows
// from starving eligible containers on subsequent passes.
func (g *ExecutorContainerGC) Reap(ctx context.Context, now time.Time) error {
	if !g.mu.TryLock() {
		return nil
	}
	defer g.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Leave time for ownership checks and removals even when inspection of one
	// candidate stalls. Discarding a fully inspected batch at every deadline
	// would prevent cleanup from making progress under daemon pressure.
	inventoryCtx, inventoryCancel := context.WithTimeout(ctx, max(g.cfg.Timeout/2, time.Nanosecond))
	defer inventoryCancel()
	listed, err := g.client.ContainerList(inventoryCtx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "com.143.role=session-executor"),
			filters.Arg("status", "exited"),
		),
	})
	if err != nil {
		return fmt.Errorf("list exited executor containers: %w", err)
	}
	slices.SortFunc(listed, func(a, b container.Summary) int { return strings.Compare(a.ID, b.ID) })
	start, _ := slices.BinarySearchFunc(listed, g.cursor, func(c container.Summary, id string) int { return strings.Compare(c.ID, id) })
	for start < len(listed) && listed[start].ID <= g.cursor {
		start++
	}
	cutoff := now.Add(-g.cfg.Retention)
	candidates := make(map[models.SessionExecutorIdentity]container.InspectResponse)
	identities := make([]models.SessionExecutorIdentity, 0, min(g.cfg.BatchSize, len(listed)))
	var failures []error
	for i := range min(g.cfg.BatchSize, len(listed)) {
		if err := inventoryCtx.Err(); err != nil {
			failures = append(failures, fmt.Errorf("executor retention inventory budget: %w", err))
			break
		}
		item := listed[(start+i)%len(listed)]
		g.cursor = item.ID
		if item.State != "exited" || !fullContainerID(item.ID) || item.Labels["com.143.role"] != "session-executor" {
			continue
		}
		inspected, inspectErr := g.client.ContainerInspect(inventoryCtx, item.ID)
		if cerrdefs.IsNotFound(inspectErr) {
			continue
		}
		if inspectErr != nil {
			failures = append(failures, fmt.Errorf("inspect executor container %s: %w", item.ID, inspectErr))
			continue
		}
		identity, ok := eligibleExecutorContainer(inspected, item.ID, cutoff)
		if !ok {
			continue
		}
		if _, duplicate := candidates[identity]; duplicate {
			// Duplicate identity is ambiguous, even if one container is terminal.
			candidates[identity] = container.InspectResponse{}
			continue
		}
		candidates[identity] = inspected
		identities = append(identities, identity)
	}
	inventoryCancel()
	if len(identities) == 0 {
		return errors.Join(failures...)
	}
	// A retained Compose worker is the durable signal used by deployment to
	// suppress generic image pruning on this daemon. Never remove the final
	// executor reference on an executor-only or unknown daemon.
	workerID, err := g.retainedWorker(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	if workerID == "" {
		g.logger.Warn().Msg("executor container retention skipped: no persistent worker generation protects local images")
		return errors.Join(failures...)
	}
	rows, err := g.store.ListTerminalContainersByIdentity(ctx, identities, cutoff)
	if err != nil {
		return errors.Join(append(failures, fmt.Errorf("verify executor container retention ownership: %w", err))...)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		identity := models.SessionExecutorIdentity{OrgID: row.OrgID, ID: row.ID}
		candidate, ok := candidates[identity]
		if !ok || !executorRetentionMatches(row, candidate, cutoff, now) {
			continue
		}
		// Reinspect the immutable ID immediately before non-force removal. A
		// restarted container must remain running even if it races this check.
		current, inspectErr := g.client.ContainerInspect(ctx, candidate.ID)
		if cerrdefs.IsNotFound(inspectErr) {
			continue
		}
		if inspectErr != nil {
			failures = append(failures, fmt.Errorf("reinspect executor container %s: %w", candidate.ID, inspectErr))
			continue
		}
		if _, ok := eligibleExecutorContainer(current, candidate.ID, cutoff); !ok || !executorRetentionMatches(row, current, cutoff, now) {
			continue
		}
		retained, workerErr := g.workerStillRetained(ctx, workerID)
		if workerErr != nil {
			return errors.Join(append(failures, workerErr)...)
		}
		if !retained {
			g.logger.Warn().Msg("executor container retention stopped: protecting worker generation disappeared")
			return errors.Join(failures...)
		}
		if err := g.client.ContainerRemove(ctx, candidate.ID, container.RemoveOptions{}); err != nil {
			if !cerrdefs.IsNotFound(err) {
				failures = append(failures, fmt.Errorf("remove terminal executor container %s: %w", candidate.ID, err))
			}
			continue
		}
		g.logger.Info().Str("org_id", row.OrgID.String()).Str("executor_id", row.ID.String()).Str("container_id", candidate.ID).
			Msg("removed retained terminal executor container")
	}
	return errors.Join(failures...)
}

func (g *ExecutorContainerGC) retainedWorker(ctx context.Context) (string, error) {
	workers, err := g.client.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.service=worker"))})
	if err != nil {
		return "", fmt.Errorf("list retained worker generations: %w", err)
	}
	for _, worker := range workers[:min(len(workers), g.cfg.BatchSize)] {
		if !fullContainerID(worker.ID) || worker.Labels["com.docker.compose.service"] != "worker" {
			continue
		}
		retained, err := g.workerStillRetained(ctx, worker.ID)
		if err != nil {
			return "", err
		}
		if retained {
			return worker.ID, nil
		}
	}
	return "", nil
}

func (g *ExecutorContainerGC) workerStillRetained(ctx context.Context, id string) (bool, error) {
	worker, err := g.client.ContainerInspect(ctx, id)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect protecting worker generation: %w", err)
	}
	return worker.ContainerJSONBase != nil && worker.ID == id && worker.Config != nil &&
		worker.Config.Labels["com.docker.compose.service"] == "worker" &&
		worker.HostConfig != nil && !worker.HostConfig.AutoRemove, nil
}

func fullContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func eligibleExecutorContainer(c container.InspectResponse, expectedID string, cutoff time.Time) (models.SessionExecutorIdentity, bool) {
	if c.ContainerJSONBase == nil || c.ID != expectedID || !fullContainerID(c.ID) || c.Config == nil || c.State == nil ||
		c.State.Status != "exited" || c.State.Running || c.State.Restarting || c.State.Paused || c.State.Dead || c.State.Pid != 0 {
		return models.SessionExecutorIdentity{}, false
	}
	finished, err := time.Parse(time.RFC3339Nano, c.State.FinishedAt)
	if err != nil || finished.IsZero() || !finished.Before(cutoff) {
		return models.SessionExecutorIdentity{}, false
	}
	labels := c.Config.Labels
	if labels["com.143.role"] != "session-executor" || labels["com.143.host_node_id"] == "" || labels["com.143.build_sha"] == "" ||
		labels["com.143.job_type"] == "" || c.Config.Image == "" {
		return models.SessionExecutorIdentity{}, false
	}
	ids := make([]uuid.UUID, 0, 4)
	for _, key := range []string{"org_id", "executor_id", "session_id", "job_id"} {
		id, err := uuid.Parse(labels["com.143."+key])
		if err != nil || id == uuid.Nil {
			return models.SessionExecutorIdentity{}, false
		}
		ids = append(ids, id)
	}
	return models.SessionExecutorIdentity{OrgID: ids[0], ID: ids[1]}, true
}

func executorRetentionMatches(row models.SessionExecutorContainerRetention, c container.InspectResponse, cutoff, now time.Time) bool {
	if c.ContainerJSONBase == nil || c.Config == nil || c.ID == "" || row.ContainerID != c.ID || row.Image != c.Config.Image ||
		row.CompletedAt.IsZero() || !row.CompletedAt.Before(cutoff) || (row.LeaseExpiresAt != nil && row.LeaseExpiresAt.After(now)) {
		return false
	}
	switch row.Status {
	case models.SessionExecutorStatusCompleted, models.SessionExecutorStatusRequeued, models.SessionExecutorStatusFailed, models.SessionExecutorStatusLost:
	default:
		return false
	}
	labels := c.Config.Labels
	threadID := ""
	if row.ThreadID != nil {
		threadID = row.ThreadID.String()
	}
	return labels["com.143.org_id"] == row.OrgID.String() && labels["com.143.executor_id"] == row.ID.String() &&
		labels["com.143.session_id"] == row.SessionID.String() && labels["com.143.job_id"] == row.JobID.String() &&
		labels["com.143.job_type"] == row.JobType && labels["com.143.thread_id"] == threadID &&
		labels["com.143.host_node_id"] == row.HostNodeID && labels["com.143.build_sha"] == row.BuildSHA
}
