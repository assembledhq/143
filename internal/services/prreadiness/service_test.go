package prreadiness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestService_EnqueueRun(t *testing.T) {
	t.Parallel()

	errLatest := errors.New("latest failed")
	errEnqueue := errors.New("enqueue failed")
	orgID := uuid.New()
	sessionID := uuid.New()
	repositoryID := uuid.New()
	userID := uuid.New()
	primaryChangesetID := uuid.New()
	snapshotKey := "snapshot.tar.zst"

	tests := []struct {
		name           string
		latest         *models.PRReadinessRun
		latestErr      error
		enqueueErr     error
		expectErr      error
		expectExisting bool
		expectEnqueue  bool
	}{
		{
			name: "returns existing queued run",
			latest: &models.PRReadinessRun{
				ID:        uuid.New(),
				OrgID:     orgID,
				SessionID: sessionID,
				Status:    models.PRReadinessRunStatusQueued,
			},
			expectExisting: true,
		},
		{
			name: "returns existing running run",
			latest: &models.PRReadinessRun{
				ID:        uuid.New(),
				OrgID:     orgID,
				SessionID: sessionID,
				Status:    models.PRReadinessRunStatusRunning,
			},
			expectExisting: true,
		},
		{
			name:          "creates run and enqueues job when no run exists",
			latestErr:     pgx.ErrNoRows,
			expectEnqueue: true,
		},
		{
			name: "creates run and enqueues job after terminal run",
			latest: &models.PRReadinessRun{
				ID:        uuid.New(),
				OrgID:     orgID,
				SessionID: sessionID,
				Status:    models.PRReadinessRunStatusPassed,
			},
			expectEnqueue: true,
		},
		{
			name:      "returns latest lookup error",
			latestErr: errLatest,
			expectErr: errLatest,
		},
		{
			name:          "returns enqueue error after creating run",
			latestErr:     pgx.ErrNoRows,
			enqueueErr:    errEnqueue,
			expectErr:     errEnqueue,
			expectEnqueue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &fakeStore{latest: tt.latest, latestErr: tt.latestErr, nextID: uuid.New(), primaryChangesetID: primaryChangesetID}
			jobs := &fakeJobStore{err: tt.enqueueErr}
			svc := NewService(store, jobs)
			session := models.Session{
				ID:                  sessionID,
				OrgID:               orgID,
				RepositoryID:        &repositoryID,
				WorkspaceGeneration: 7,
				WorkspaceRevision:   42,
				SnapshotKey:         &snapshotKey,
			}

			run, err := svc.EnqueueRun(context.Background(), EnqueueRunRequest{
				OrgID:             orgID,
				Session:           session,
				TriggeredByUserID: &userID,
			})
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "EnqueueRun should return the expected error")
				if tt.expectEnqueue {
					require.Len(t, store.created, 1, "EnqueueRun should create the readiness run before returning an enqueue error")
					require.Len(t, jobs.enqueued, 1, "EnqueueRun should attempt to enqueue the worker job before returning an enqueue error")
				}
				return
			}
			require.NoError(t, err, "EnqueueRun should succeed")
			require.NotNil(t, run, "EnqueueRun should return a run")

			if tt.expectExisting {
				require.Same(t, tt.latest, run, "EnqueueRun should return the existing queued or running run")
				require.Empty(t, store.created, "EnqueueRun should not create another run")
				require.Empty(t, jobs.enqueued, "EnqueueRun should not enqueue another job")
				return
			}

			require.Len(t, store.created, 1, "EnqueueRun should create exactly one readiness run")
			created := store.created[0]
			require.Equal(t, store.nextID, created.ID, "EnqueueRun should return the persisted run ID")
			require.Equal(t, orgID, created.OrgID, "created run should be scoped to the org")
			require.Equal(t, sessionID, created.SessionID, "created run should target the requested session")
			require.Equal(t, &repositoryID, created.RepositoryID, "created run should retain repository scope")
			require.Equal(t, models.PRReadinessRunStatusQueued, created.Status, "created run should start queued")
			require.Equal(t, int64(42), created.EvaluatedWorkspaceRevision, "created run should capture workspace revision")
			require.Equal(t, &snapshotKey, created.EvaluatedSnapshotKey, "created run should capture snapshot key")
			require.Equal(t, "Queued", created.Summary, "created run should use the queued summary")
			require.Equal(t, &userID, created.TriggeredByUserID, "created run should preserve nullable user attribution")
			if tt.expectEnqueue {
				require.Len(t, jobs.enqueued, 1, "EnqueueRun should enqueue exactly one worker job")
				enqueued := jobs.enqueued[0]
				require.Equal(t, orgID, enqueued.orgID, "job should be scoped to the org")
				require.Equal(t, runJobQueue, enqueued.queue, "job should use the readiness queue")
				require.Equal(t, runJobType, enqueued.jobType, "job should use the readiness job type")
				require.Equal(t, runJobPriority, enqueued.priority, "job should use the readiness priority")
				require.Equal(t, primaryChangesetID, created.ChangesetID, "created run should adopt the trigger-assigned primary changeset")
				require.NotNil(t, enqueued.dedupeKey, "job should include a dedupe key")
				// A session-scoped enqueue must land on the same dedupe key as a manual
				// run against the primary changeset, otherwise the two paths race and
				// enqueue duplicate readiness runs for the same primary changeset.
				require.Equal(t, DedupeKey(primaryChangesetID), *enqueued.dedupeKey, "job should dedupe by the primary changeset")
				require.Equal(t, map[string]string{
					"org_id":       orgID.String(),
					"session_id":   sessionID.String(),
					"readiness_id": created.ID.String(),
					"changeset_id": primaryChangesetID.String(),
				}, enqueued.payload, "job payload should identify the readiness run and its changeset")
			}
		})
	}
}

func TestService_EnqueueRunRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	_, err := (*Service)(nil).EnqueueRun(context.Background(), EnqueueRunRequest{})
	require.Error(t, err, "nil service should reject enqueue requests")

	svc := NewService(nil, nil)
	_, err = svc.EnqueueRun(context.Background(), EnqueueRunRequest{})
	require.Error(t, err, "service without stores should reject enqueue requests")
}

func TestServiceEnqueueRunScopesToChangesetHead(t *testing.T) {
	t.Parallel()
	orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
	headSHA := "head-123"
	store := &fakeStore{latestErr: pgx.ErrNoRows, nextID: uuid.New()}
	jobs := &fakeJobStore{}
	run, err := NewService(store, jobs).EnqueueRun(context.Background(), EnqueueRunRequest{
		OrgID: orgID, Session: models.Session{ID: sessionID}, ChangesetID: &changesetID, ChangesetHeadSHA: &headSHA,
	})
	require.NoError(t, err, "changeset readiness should enqueue against a materialized branch head")
	require.Equal(t, changesetID, run.ChangesetID, "readiness run should retain its changeset scope")
	require.Equal(t, &headSHA, run.EvaluatedHeadSHA, "readiness run should pin the evaluated branch head")
	require.Equal(t, "pr_readiness:"+changesetID.String(), *jobs.enqueued[0].dedupeKey, "readiness jobs should deduplicate per changeset")
	require.Equal(t, changesetID.String(), jobs.enqueued[0].payload.(map[string]string)["changeset_id"], "worker payload should carry the selected changeset")
}

type fakeStore struct {
	latest             *models.PRReadinessRun
	latestErr          error
	nextID             uuid.UUID
	primaryChangesetID uuid.UUID
	created            []*models.PRReadinessRun
}

func (s *fakeStore) GetLatestBySession(context.Context, uuid.UUID, uuid.UUID) (*models.PRReadinessRun, error) {
	return s.latest, s.latestErr
}

func (s *fakeStore) GetLatestByChangeset(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PRReadinessRun, error) {
	return s.latest, s.latestErr
}

func (s *fakeStore) CreateRun(_ context.Context, run *models.PRReadinessRun) error {
	run.ID = s.nextID
	// Mirror the pr_readiness_runs BEFORE INSERT trigger, which defaults an
	// unset changeset_id to the session's primary changeset.
	if run.ChangesetID == uuid.Nil {
		run.ChangesetID = s.primaryChangesetID
	}
	now := time.Now()
	run.StartedAt = now
	run.CreatedAt = now
	run.UpdatedAt = now
	copyRun := *run
	s.created = append(s.created, &copyRun)
	return nil
}

type enqueuedJob struct {
	orgID     uuid.UUID
	queue     string
	jobType   string
	payload   any
	priority  int
	dedupeKey *string
}

type fakeJobStore struct {
	err      error
	enqueued []enqueuedJob
}

func (s *fakeJobStore) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	var dedupeCopy *string
	if dedupeKey != nil {
		value := *dedupeKey
		dedupeCopy = &value
	}
	s.enqueued = append(s.enqueued, enqueuedJob{
		orgID:     orgID,
		queue:     queue,
		jobType:   jobType,
		payload:   payload,
		priority:  priority,
		dedupeKey: dedupeCopy,
	})
	return uuid.New(), s.err
}
