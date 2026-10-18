package github

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// capturedTimeArg records the time argument the store received so the test can
// bound it by wall-clock readings taken around the call.
type capturedTimeArg struct{ got *time.Time }

func (a capturedTimeArg) Match(v any) bool {
	got, ok := v.(time.Time)
	if ok {
		*a.got = got
	}
	return ok
}

func TestReconcilePullRequestStateUsesConfiguredSweepCutoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		newService    func(mock pgxmock.PgxPoolIface) *PRService
		wantThreshold time.Duration
	}{
		{
			name: "zero-value service keeps the default threshold",
			newService: func(mock pgxmock.PgxPoolIface) *PRService {
				return &PRService{
					pullRequests: db.NewPullRequestStore(mock), sessions: db.NewSessionStore(mock), repos: db.NewRepositoryStore(mock),
					publications: db.NewSessionPublicationStore(mock), changesets: db.NewSessionChangesetStore(mock), logger: zerolog.New(io.Discard),
				}
			},
			wantThreshold: 10 * time.Minute,
		},
		{
			name: "constructed service keeps the default threshold",
			newService: func(mock pgxmock.PgxPoolIface) *PRService {
				service := NewPRService(nil, db.NewPullRequestStore(mock), db.NewSessionStore(mock), nil, nil, db.NewRepositoryStore(mock), nil, zerolog.New(io.Discard))
				service.SetPublicationStore(db.NewSessionPublicationStore(mock))
				service.SetChangesetStore(db.NewSessionChangesetStore(mock))
				return service
			},
			wantThreshold: 10 * time.Minute,
		},
		{
			name: "configured threshold widens only the sweep cutoff",
			newService: func(mock pgxmock.PgxPoolIface) *PRService {
				service := NewPRService(nil, db.NewPullRequestStore(mock), db.NewSessionStore(mock), nil, nil, db.NewRepositoryStore(mock), nil, zerolog.New(io.Discard))
				service.SetPublicationStore(db.NewSessionPublicationStore(mock))
				service.SetChangesetStore(db.NewSessionChangesetStore(mock))
				service.SetHealthSweepStaleAfter(3 * time.Hour)
				return service
			},
			wantThreshold: 3 * time.Hour,
		},
		{
			name: "non-positive threshold keeps the default",
			newService: func(mock pgxmock.PgxPoolIface) *PRService {
				service := NewPRService(nil, db.NewPullRequestStore(mock), db.NewSessionStore(mock), nil, nil, db.NewRepositoryStore(mock), nil, zerolog.New(io.Discard))
				service.SetPublicationStore(db.NewSessionPublicationStore(mock))
				service.SetChangesetStore(db.NewSessionChangesetStore(mock))
				service.SetHealthSweepStaleAfter(-time.Minute)
				return service
			},
			wantThreshold: 10 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "sweep cutoff mock should initialize")
			t.Cleanup(mock.Close)
			orgID := uuid.New()
			var cutoff time.Time
			expectHealthPopulation(mock, orgID, 0)
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "before": capturedTimeArg{got: &cutoff}, "limit": 50}).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			// No health candidates: the independent recovery steps must still run
			// with the requested limit.
			mock.ExpectQuery("SELECT s.id[\\s\\S]*FROM session_publish_state").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "stuck_before": pgxmock.AnyArg(), "limit": 50}).
				WillReturnRows(pgxmock.NewRows([]string{"id"}))
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 50}).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			mock.ExpectQuery("SELECT .+ FROM session_publications").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "updated_before": pgxmock.AnyArg(), "limit": 50}).
				WillReturnRows(pgxmock.NewRows(publicationHealthTestColumns))

			service := tt.newService(mock)
			startedAt := time.Now()
			err = service.ReconcilePullRequestState(context.Background(), orgID, 50)
			finishedAt := time.Now()

			require.NoError(t, err, "a sweep with no stale candidates should succeed")
			require.NoError(t, mock.ExpectationsWereMet(), "the sweep should list candidates and still run every independent recovery step")
			earliest, latest := startedAt.Add(-tt.wantThreshold), finishedAt.Add(-tt.wantThreshold)
			require.False(t, cutoff.Before(earliest) || cutoff.After(latest), "sweep cutoff %s should be the configured threshold before the sweep, between %s and %s", cutoff, earliest, latest)
		})
	}
}

// A wider sweep threshold must not leak into the per-read freshness budgets:
// interactive reads keep two minutes and the automatic repair coordinator
// keeps ten.
func TestPRHealthSweepThresholdLeavesReadBudgetsUnchanged(t *testing.T) {
	t.Parallel()

	require.Equal(t, 2*time.Minute, healthBuildOptions{}.staleAfter(), "interactive reads should keep the two-minute budget")
	require.Equal(t, 10*time.Minute, autoRepairHealthBuildOptions().staleAfter(), "the automatic repair coordinator should keep the ten-minute budget")

	summaryJSON, err := json.Marshal(models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean})
	require.NoError(t, err, "should marshal health summary")

	tests := []struct {
		name        string
		opts        healthBuildOptions
		snapshotAge time.Duration
		wantEnqueue bool
	}{
		{name: "interactive read refreshes past two minutes", opts: healthBuildOptions{}, snapshotAge: 5 * time.Minute, wantEnqueue: true},
		{name: "interactive read within two minutes stays cached", opts: healthBuildOptions{}, snapshotAge: time.Minute},
		{name: "automatic repair refreshes past ten minutes", opts: autoRepairHealthBuildOptions(), snapshotAge: 15 * time.Minute, wantEnqueue: true},
		{name: "automatic repair within ten minutes stays cached", opts: autoRepairHealthBuildOptions(), snapshotAge: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			t.Cleanup(mock.Close)

			orgID := uuid.New()
			pullRequestID := uuid.New()
			now := time.Now().UTC()
			syncedAt := now.Add(-tt.snapshotAge)

			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
					pullRequestID, nil, orgID, 42, "https://github.com/example/app/pull/42", "example/app",
					"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
					models.PullRequestMergeStateUnknown, false, 0, false, &syncedAt, int64(2), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
				))
			if tt.wantEnqueue {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgx.NamedArgs{
						"org_id":     orgID,
						"queue":      prHealthSyncQueue,
						"job_type":   prHealthSyncJobType,
						"payload":    pgxmock.AnyArg(),
						"priority":   6,
						"dedupe_key": pgxmock.AnyArg(),
					}).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			}
			mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
				WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
					pullRequestID, orgID, int64(3), "head", "base", summaryJSON, summaryJSON,
					models.PullRequestHealthEnrichmentStatusReady, &now, int64(0), now, now,
				))

			service := NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, db.NewJobStore(mock), zerolog.New(io.Discard))
			service.SetHealthSweepStaleAfter(3 * time.Hour)

			_, err = service.getPullRequestHealth(context.Background(), orgID, pullRequestID, tt.opts)
			require.NoError(t, err, "health build should succeed")
			require.NoError(t, mock.ExpectationsWereMet(), "a wider sweep threshold should not change when per-read budgets enqueue a readback")
		})
	}
}
