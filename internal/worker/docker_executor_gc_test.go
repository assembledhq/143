package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type executorGCRemoval struct {
	id      string
	options container.RemoveOptions
}

type fakeExecutorGCClient struct {
	listed            []container.Summary
	inspections       map[string][]container.InspectResponse
	inspectIDs        []string
	removals          []executorGCRemoval
	listOptions       container.ListOptions
	listErr           error
	inspectErr        error
	removeErr         error
	listHook          func(context.Context)
	inspectHook       func(context.Context, string) error
	noWorker          bool
	workerListErr     error
	workerInspectErr  error
	workerInspections []container.InspectResponse
}

func (c *fakeExecutorGCClient) ContainerList(ctx context.Context, opts container.ListOptions) ([]container.Summary, error) {
	if values := opts.Filters.Get("label"); len(values) == 1 && values[0] == "com.docker.compose.service=worker" {
		if c.noWorker {
			return nil, c.workerListErr
		}
		return []container.Summary{{ID: strings.Repeat("f", 64), Labels: map[string]string{"com.docker.compose.service": "worker"}}}, c.workerListErr
	}
	c.listOptions = opts
	if c.listHook != nil {
		c.listHook(ctx)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return append([]container.Summary(nil), c.listed...), c.listErr
}

func (c *fakeExecutorGCClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if id == strings.Repeat("f", 64) {
		if len(c.workerInspections) > 0 {
			item := c.workerInspections[0]
			if len(c.workerInspections) > 1 {
				c.workerInspections = c.workerInspections[1:]
			}
			return item, c.workerInspectErr
		}
		return retainedWorkerFixture(), c.workerInspectErr
	}
	c.inspectIDs = append(c.inspectIDs, id)
	if c.inspectHook != nil {
		if err := c.inspectHook(ctx, id); err != nil {
			return container.InspectResponse{}, err
		}
	}
	items := c.inspections[id]
	if len(items) == 0 {
		return container.InspectResponse{}, c.inspectErr
	}
	item := items[0]
	if len(items) > 1 {
		c.inspections[id] = items[1:]
	}
	return item, c.inspectErr
}

func retainedWorkerFixture() container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: strings.Repeat("f", 64), HostConfig: &container.HostConfig{}},
		Config:            &container.Config{Labels: map[string]string{"com.docker.compose.service": "worker"}},
	}
}

func (c *fakeExecutorGCClient) ContainerRemove(_ context.Context, id string, opts container.RemoveOptions) error {
	c.removals = append(c.removals, executorGCRemoval{id: id, options: opts})
	return c.removeErr
}

type fakeExecutorRetentionStore struct {
	rows    []models.SessionExecutorContainerRetention
	err     error
	batches [][]models.SessionExecutorIdentity
	cutoffs []time.Time
	onRead  func()
}

func (s *fakeExecutorRetentionStore) ListTerminalContainersByIdentity(_ context.Context, identities []models.SessionExecutorIdentity, cutoff time.Time) ([]models.SessionExecutorContainerRetention, error) {
	s.batches = append(s.batches, append([]models.SessionExecutorIdentity(nil), identities...))
	s.cutoffs = append(s.cutoffs, cutoff)
	if s.onRead != nil {
		s.onRead()
	}
	return append([]models.SessionExecutorContainerRetention(nil), s.rows...), s.err
}

func executorGCFixture(now time.Time, digit string) (container.Summary, container.InspectResponse, models.SessionExecutorContainerRetention) {
	id := strings.Repeat(digit, 64)
	lease := now.Add(-2 * time.Hour)
	row := models.SessionExecutorContainerRetention{
		ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), JobID: uuid.New(), JobType: "run_agent",
		HostNodeID: "worker-previous-generation", ContainerID: id, Image: "example/server:previous",
		BuildSHA: "previous-build", Status: models.SessionExecutorStatusCompleted,
		CompletedAt: now.Add(-48 * time.Hour), LeaseExpiresAt: &lease,
	}
	labels := map[string]string{
		"com.143.role": "session-executor", "com.143.org_id": row.OrgID.String(),
		"com.143.executor_id": row.ID.String(), "com.143.session_id": row.SessionID.String(),
		"com.143.job_id": row.JobID.String(), "com.143.job_type": row.JobType,
		"com.143.host_node_id": row.HostNodeID, "com.143.build_sha": row.BuildSHA,
	}
	return container.Summary{ID: id, State: "exited", Labels: labels}, container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: &container.State{Status: "exited", FinishedAt: now.Add(-47 * time.Hour).Format(time.RFC3339Nano)}},
		Config:            &container.Config{Image: row.Image, Labels: labels},
	}, row
}

func newExecutorGCTest(t *testing.T, client *fakeExecutorGCClient, store *fakeExecutorRetentionStore, batchSize int) *ExecutorContainerGC {
	t.Helper()
	g, err := NewExecutorContainerGC(client, store, ExecutorContainerGCConfig{Interval: time.Minute, Retention: 24 * time.Hour, BatchSize: batchSize, Timeout: time.Second}, zerolog.Nop())
	require.NoError(t, err, "valid bounded executor retention configuration should construct")
	return g
}

func TestExecutorContainerGC_ReapOwnershipAndRetention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		change func(*container.Summary, *container.InspectResponse, *models.SessionExecutorContainerRetention)
		remove bool
	}{
		{name: "completed executor", remove: true},
		{name: "requeued executor", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Status = models.SessionExecutorStatusRequeued
		}, remove: true},
		{name: "failed executor", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Status = models.SessionExecutorStatusFailed
		}, remove: true},
		{name: "lost executor", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Status = models.SessionExecutorStatusLost
		}, remove: true},
		{name: "running database row", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Status = models.SessionExecutorStatusRunning
		}},
		{name: "unknown database status", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Status = "unknown"
		}},
		{name: "other tenant", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.OrgID = uuid.New()
		}},
		{name: "other executor", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.ID = uuid.New()
		}},
		{name: "other session", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.SessionID = uuid.New()
		}},
		{name: "other job", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.JobID = uuid.New()
		}},
		{name: "other job type", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.JobType = "continue_session"
		}},
		{name: "other host generation", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.HostNodeID = "worker-other"
		}},
		{name: "other build", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.BuildSHA = "other-build"
		}},
		{name: "other image", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.Image = "example/server:other"
		}},
		{name: "other container", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.ContainerID = strings.Repeat("b", 64)
		}},
		{name: "missing thread label", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			id := uuid.New()
			r.ThreadID = &id
		}},
		{name: "matching thread", change: func(_ *container.Summary, c *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			id := uuid.New()
			r.ThreadID = &id
			c.Config.Labels["com.143.thread_id"] = id.String()
		}, remove: true},
		{name: "recent terminal record", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.CompletedAt = now.Add(-time.Hour)
		}},
		{name: "terminal record at cutoff", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.CompletedAt = now.Add(-24 * time.Hour)
		}},
		{name: "missing terminal time", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			r.CompletedAt = time.Time{}
		}},
		{name: "live executor lease", change: func(_ *container.Summary, _ *container.InspectResponse, r *models.SessionExecutorContainerRetention) {
			v := now.Add(time.Hour)
			r.LeaseExpiresAt = &v
		}},
		{name: "worker generation", change: func(s *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			s.Labels["com.143.role"] = "worker"
			c.Config.Labels["com.143.role"] = "worker"
		}},
		{name: "sandbox container", change: func(s *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			s.Labels["com.143.role"] = "sandbox"
			c.Config.Labels["com.143.role"] = "sandbox"
		}},
		{name: "preview container", change: func(s *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			s.Labels["com.143.role"] = "preview"
			c.Config.Labels["com.143.role"] = "preview"
		}},
		{name: "running inventory", change: func(s *container.Summary, _ *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			s.State = "running"
		}},
		{name: "running inspection", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.Running = true
		}},
		{name: "restarting inspection", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.Restarting = true
		}},
		{name: "paused inspection", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.Paused = true
		}},
		{name: "dead inspection", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.Dead = true
		}},
		{name: "live process", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.Pid = 1
		}},
		{name: "recent Docker finish", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.FinishedAt = now.Add(-time.Hour).Format(time.RFC3339)
		}},
		{name: "Docker finish at cutoff", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.FinishedAt = now.Add(-24 * time.Hour).Format(time.RFC3339)
		}},
		{name: "unparseable Docker finish", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.FinishedAt = "unknown"
		}},
		{name: "missing Docker finish", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State.FinishedAt = "0001-01-01T00:00:00Z"
		}},
		{name: "partial Docker ID", change: func(s *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			s.ID = "aaaa"
			c.ID = "aaaa"
		}},
		{name: "mismatched inspected ID", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.ID = strings.Repeat("b", 64)
		}},
		{name: "nil Docker config", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.Config = nil
		}},
		{name: "nil Docker state", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			c.State = nil
		}},
		{name: "missing ownership labels", change: func(_ *container.Summary, c *container.InspectResponse, _ *models.SessionExecutorContainerRetention) {
			delete(c.Config.Labels, "com.143.executor_id")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			listed, inspected, row := executorGCFixture(now, "a")
			if tt.change != nil {
				tt.change(&listed, &inspected, &row)
			}
			client := &fakeExecutorGCClient{listed: []container.Summary{listed}, inspections: map[string][]container.InspectResponse{listed.ID: {inspected}}}
			store := &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{row}}
			g := newExecutorGCTest(t, client, store, 100)
			err := g.Reap(context.Background(), now)
			require.NoError(t, err, "ambiguous ownership should preserve containers without failing the pass")
			var expected []executorGCRemoval
			if tt.remove {
				expected = []executorGCRemoval{{id: listed.ID, options: container.RemoveOptions{}}}
			}
			require.Equal(t, expected, client.removals, "only old exact terminal executor identities may be removed, without force or volumes")
			require.Equal(t, []string{"com.143.role=session-executor"}, client.listOptions.Filters.Get("label"), "inventory must select only explicitly labeled executors")
			require.Equal(t, []string{"exited"}, client.listOptions.Filters.Get("status"), "inventory must not select running containers")
		})
	}
}

func TestExecutorContainerGC_RechecksBeforeRemoval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*container.InspectResponse)
	}{
		{name: "restarted", change: func(c *container.InspectResponse) { c.State.Running = true; c.State.Status = "running" }},
		{name: "restarted and exited recently", change: func(c *container.InspectResponse) { c.State.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano) }},
		{name: "identity changed", change: func(c *container.InspectResponse) { c.Config.Labels["com.143.session_id"] = uuid.NewString() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			listed, first, row := executorGCFixture(now, "a")
			_, second, _ := executorGCFixture(now, "a")
			second.Config.Labels = make(map[string]string, len(first.Config.Labels))
			for key, value := range first.Config.Labels {
				second.Config.Labels[key] = value
			}
			tt.change(&second)
			client := &fakeExecutorGCClient{listed: []container.Summary{listed}, inspections: map[string][]container.InspectResponse{listed.ID: {first, second}}}
			g := newExecutorGCTest(t, client, &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{row}}, 100)
			require.NoError(t, g.Reap(context.Background(), now), "changed container evidence should preserve the container")
			require.Equal(t, []string{listed.ID, listed.ID}, client.inspectIDs, "collector must reinspect the exact immutable ID immediately before removal")
			require.Empty(t, client.removals, "a restarted or changed container must not be removed")
		})
	}
}

func TestExecutorContainerGC_RequiresPersistentWorkerImageProtection(t *testing.T) {
	t.Parallel()
	boom := errors.New("worker inventory unavailable")
	tests := []struct {
		name    string
		setup   func(*fakeExecutorGCClient)
		wantErr error
	}{
		{name: "executor-only daemon", setup: func(c *fakeExecutorGCClient) { c.noWorker = true }},
		{name: "worker inventory failure", setup: func(c *fakeExecutorGCClient) { c.workerListErr = boom }, wantErr: boom},
		{name: "worker inspection failure", setup: func(c *fakeExecutorGCClient) { c.workerInspectErr = boom }, wantErr: boom},
		{name: "worker no longer exists", setup: func(c *fakeExecutorGCClient) { c.workerInspectErr = cerrdefs.ErrNotFound }},
		{name: "worker may auto-remove", setup: func(c *fakeExecutorGCClient) {
			r := retainedWorkerFixture()
			r.HostConfig.AutoRemove = true
			c.workerInspections = []container.InspectResponse{r}
		}},
		{name: "worker disappears before final deletion", setup: func(c *fakeExecutorGCClient) {
			c.workerInspections = []container.InspectResponse{retainedWorkerFixture(), {}}
		}},
		{name: "worker label changes", setup: func(c *fakeExecutorGCClient) {
			r := retainedWorkerFixture()
			r.Config.Labels["com.docker.compose.service"] = "api"
			c.workerInspections = []container.InspectResponse{r}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			listed, inspected, row := executorGCFixture(now, "a")
			client := &fakeExecutorGCClient{listed: []container.Summary{listed}, inspections: map[string][]container.InspectResponse{listed.ID: {inspected}}}
			tt.setup(client)
			g := newExecutorGCTest(t, client, &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{row}}, 100)
			err := g.Reap(context.Background(), now)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "missing image-policy evidence must propagate probe failures")
			} else {
				require.NoError(t, err, "unknown daemon ownership should safely skip retention")
			}
			require.Empty(t, client.removals, "the final executor image reference must survive without a persistent worker signal")
		})
	}
}

func TestExecutorContainerGC_ErrorsFailClosed(t *testing.T) {
	t.Parallel()
	boom := errors.New("fixture dependency unavailable")
	tests := []struct {
		name          string
		setup         func(*fakeExecutorGCClient, *fakeExecutorRetentionStore)
		wantErr       error
		removeAttempt bool
	}{
		{name: "list fails", setup: func(c *fakeExecutorGCClient, _ *fakeExecutorRetentionStore) { c.listErr = boom }, wantErr: boom},
		{name: "inspect fails", setup: func(c *fakeExecutorGCClient, _ *fakeExecutorRetentionStore) { c.inspectErr = boom }, wantErr: boom},
		{name: "already absent", setup: func(c *fakeExecutorGCClient, _ *fakeExecutorRetentionStore) { c.inspectErr = cerrdefs.ErrNotFound }},
		{name: "database fails", setup: func(_ *fakeExecutorGCClient, s *fakeExecutorRetentionStore) { s.err = boom }, wantErr: boom},
		{name: "missing or live-owned record", setup: func(_ *fakeExecutorGCClient, s *fakeExecutorRetentionStore) { s.rows = nil }},
		{name: "reinspection fails", setup: func(c *fakeExecutorGCClient, s *fakeExecutorRetentionStore) {
			s.onRead = func() { c.inspectErr = boom }
		}, wantErr: boom},
		{name: "removed by other collector before reinspection", setup: func(c *fakeExecutorGCClient, s *fakeExecutorRetentionStore) {
			s.onRead = func() { c.inspectErr = cerrdefs.ErrNotFound }
		}},
		{name: "removal rejects racing restart", setup: func(c *fakeExecutorGCClient, _ *fakeExecutorRetentionStore) { c.removeErr = cerrdefs.ErrConflict }, wantErr: cerrdefs.ErrConflict, removeAttempt: true},
		{name: "other collector removed same immutable ID", setup: func(c *fakeExecutorGCClient, _ *fakeExecutorRetentionStore) { c.removeErr = cerrdefs.ErrNotFound }, removeAttempt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			listed, inspected, row := executorGCFixture(now, "a")
			client := &fakeExecutorGCClient{listed: []container.Summary{listed}, inspections: map[string][]container.InspectResponse{listed.ID: {inspected}}}
			store := &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{row}}
			tt.setup(client, store)
			err := newExecutorGCTest(t, client, store, 100).Reap(context.Background(), now)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "dependency failures should propagate for operational logging")
			} else {
				require.NoError(t, err, "absence and unavailable ownership should be safe to skip")
			}
			var expected []executorGCRemoval
			if tt.removeAttempt {
				expected = []executorGCRemoval{{id: listed.ID, options: container.RemoveOptions{}}}
			}
			require.Equal(t, expected, client.removals, "only a fully verified record may reach exact non-force removal")
		})
	}
}

func TestExecutorContainerGC_BoundedCursorProgress(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	first, firstInspect, _ := executorGCFixture(now, "a")
	second, secondInspect, secondRow := executorGCFixture(now, "b")
	third, thirdInspect, thirdRow := executorGCFixture(now, "c")
	client := &fakeExecutorGCClient{listed: []container.Summary{third, first, second}, inspections: map[string][]container.InspectResponse{first.ID: {firstInspect}, second.ID: {secondInspect}, third.ID: {thirdInspect}}}
	store := &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{secondRow, thirdRow}}
	g := newExecutorGCTest(t, client, store, 1)
	for range 3 {
		require.NoError(t, g.Reap(context.Background(), now), "bounded passes should progress beyond an unowned container")
	}
	require.Equal(t, []executorGCRemoval{{id: second.ID}, {id: third.ID}}, client.removals, "ambiguous first candidate must not starve later valid records")
	require.Equal(t, [][]models.SessionExecutorIdentity{
		{{OrgID: uuid.MustParse(first.Labels["com.143.org_id"]), ID: uuid.MustParse(first.Labels["com.143.executor_id"])}},
		{{OrgID: secondRow.OrgID, ID: secondRow.ID}},
		{{OrgID: thirdRow.OrgID, ID: thirdRow.ID}},
	}, store.batches, "every pass must use one bounded identity batch")
	client.listed = []container.Summary{first}
	require.NoError(t, g.Reap(context.Background(), now), "cursor must wrap after later containers disappear")
	require.Equal(t, store.batches[0], store.batches[3], "cursor should retry preserved records after wrapping")
}

func TestExecutorContainerGC_OverlappingPasses(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &fakeExecutorGCClient{listHook: func(context.Context) { close(entered); <-release }}
	g := newExecutorGCTest(t, client, &fakeExecutorRetentionStore{}, 100)
	done := make(chan error, 1)
	go func() { done <- g.Reap(context.Background(), time.Now()) }()
	<-entered
	err := g.Reap(context.Background(), time.Now())
	close(release)
	require.NoError(t, err, "overlapping local passes should skip without racing cursor state")
	require.NoError(t, <-done, "the owning pass should complete normally")
}

func TestExecutorContainerGC_PassDeadline(t *testing.T) {
	t.Parallel()
	client := &fakeExecutorGCClient{listHook: func(ctx context.Context) { <-ctx.Done() }}
	g := newExecutorGCTest(t, client, &fakeExecutorRetentionStore{}, 100)
	g.cfg.Timeout = 10 * time.Millisecond
	require.ErrorIs(t, g.Reap(context.Background(), time.Now()), context.DeadlineExceeded, "Docker inventory must respect the per-pass deadline")
	require.Empty(t, client.removals, "timed-out inspection cannot authorize removal")
}

func TestExecutorContainerGC_SlowInventoryStillRemovesVerifiedCandidates(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	first, firstInspect, firstRow := executorGCFixture(now, "a")
	second, secondInspect, _ := executorGCFixture(now, "b")
	client := &fakeExecutorGCClient{
		listed:      []container.Summary{first, second},
		inspections: map[string][]container.InspectResponse{first.ID: {firstInspect}, second.ID: {secondInspect}},
		inspectHook: func(ctx context.Context, id string) error {
			if id == second.ID {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	store := &fakeExecutorRetentionStore{rows: []models.SessionExecutorContainerRetention{firstRow}}
	g := newExecutorGCTest(t, client, store, 100)
	g.cfg.Timeout = 200 * time.Millisecond
	err := g.Reap(context.Background(), now)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a stalled inspection should be reported with its bounded timeout")
	require.Equal(t, []executorGCRemoval{{id: first.ID}}, client.removals, "inventory must reserve time to reclaim already verified candidates despite a later slow inspection")
	require.Equal(t, [][]models.SessionExecutorIdentity{{{OrgID: firstRow.OrgID, ID: firstRow.ID}}}, store.batches, "only successfully inspected candidates may enter the tenant-paired ownership lookup")
}

func TestExecutorContainerGC_RunRetriesAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	observed := make(chan struct{}, 2)
	client := &fakeExecutorGCClient{listErr: errors.New("temporary list error"), listHook: func(context.Context) { observed <- struct{}{} }}
	g := newExecutorGCTest(t, client, &fakeExecutorRetentionStore{}, 100)
	done := make(chan struct{})
	go func() { g.run(ctx, ticks); close(done) }()
	<-observed
	ticks <- time.Now()
	<-observed
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic collector should stop when the worker context is canceled")
	}
	require.Empty(t, client.removals, "failed periodic passes must never remove containers")
}
