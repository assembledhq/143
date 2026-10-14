package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func workspacePreparationTestPolicy(enabled bool) *reviewpreparation.Policy {
	policy, err := reviewpreparation.NewPolicy(enabled, []string{"test/preparation", "test/saved-review"})
	if err != nil {
		panic(err)
	}
	return policy
}

func expectPreparationRepository(mock pgxmock.PgxPoolIface, orgID, repositoryID uuid.UUID) {
	mock.ExpectQuery("FROM repositories").WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}).AddRow(repositoryID, orgID, uuid.New(), int64(1), "test/preparation", "main", false, nil, nil, "https://github.com/test/preparation.git", int64(1), "active", nil, nil, json.RawMessage(`{}`), time.Now(), time.Now()))
}

func expectPreparationSession(mock pgxmock.PgxPoolIface, orgID, sessionID, repositoryID uuid.UUID) {
	row := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusRunning, 0, nil, nil)
	setWorkerSessionColumn(row, "origin", models.SessionOriginCodeReview)
	setWorkerSessionColumn(row, "repository_id", &repositoryID)
	setWorkerSessionColumn(row, "revision_context", json.RawMessage(`{"github_repo":"test/preparation"}`))
	mock.ExpectQuery("FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(row...))
	expectPreparationRepository(mock, orgID, repositoryID)
}

func TestPreparationDisabledCreatesNoWorkerCheckpointOrDeadline(t *testing.T) {
	t.Parallel()
	empty, err := reviewpreparation.NewPolicy(true, nil)
	require.NoError(t, err, "empty configuration must construct")
	tests := []struct {
		name   string
		policy *reviewpreparation.Policy
	}{{name: "default nil"}, {name: "global disabled", policy: workspacePreparationTestPolicy(false)}, {name: "empty allowlist", policy: empty}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			services := &Services{ReviewPreparationPolicy: tt.policy}
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
			started, err := codeReviewWorkspacePreparationStarted(context.Background(), nil, services, job)
			require.NoError(t, err, "inactive policy must not access checkpoint stores")
			require.False(t, started, "inactive policy must have no preparation checkpoint")
			require.NoError(t, ensureCodeReviewWorkspaceReady(context.Background(), nil, services, zerolog.Nop(), job), "inactive policy must not inspect or enqueue preparation work")
			require.NoError(t, newPrepareCodeReviewWorkspaceHandler(nil, services, zerolog.Nop())(context.Background(), "", nil), "inactive queued preparation must drain without accessing durable jobs")
			deadline, err := initialCodeReviewPreparationDeadline(context.Background(), nil, services, zerolog.Nop(), models.Session{Origin: models.SessionOriginCodeReview}, &models.SessionThread{ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly}, false)
			require.NoError(t, err, "inactive reviewer must not consult durable budget stores")
			require.Nil(t, deadline, "inactive reviewer must not inherit a preparation deadline")
		})
	}
}

func TestPreparationIdentityFailureDoesNotFallThrough(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation func(context.Context, *Stores, *Services, runCodeReviewPayload) error
	}{
		{name: "checkpoint", operation: func(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload) error {
			_, err := codeReviewWorkspacePreparationStarted(ctx, stores, services, job)
			return err
		}},
		{name: "controller", operation: func(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload) error {
			return ensureCodeReviewWorkspaceReady(ctx, stores, services, zerolog.Nop(), job)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "identity failure fixture must initialize")
			defer mock.Close()
			orgID, sessionID := uuid.New(), uuid.New()
			mock.ExpectQuery("FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).WillReturnError(pgx.ErrNoRows)
			stores := &Stores{Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock), CodeReviews: db.NewCodeReviewStore(mock), CodeReviewWorkspaces: db.NewCodeReviewWorkspaceStore(mock)}
			err = tt.operation(context.Background(), stores, &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true)}, runCodeReviewPayload{OrgID: orgID, SessionID: sessionID})
			require.Error(t, err, "configured lookup failure must stop controller work")
			require.NoError(t, mock.ExpectationsWereMet(), "lookup failure must perform no readiness or job access")
		})
	}
}

func TestOutsideRepositoryCreatesNoPreparationJobsOrDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation func(context.Context, *Stores, *Services, runCodeReviewPayload, models.Session) error
	}{
		{name: "checkpoint", operation: func(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, _ models.Session) error {
			started, err := codeReviewWorkspacePreparationStarted(ctx, stores, services, job)
			if started {
				return fmt.Errorf("outside repository unexpectedly has a preparation checkpoint")
			}
			return err
		}},
		{name: "controller", operation: func(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, _ models.Session) error {
			return ensureCodeReviewWorkspaceReady(ctx, stores, services, zerolog.Nop(), job)
		}},
		{name: "queued dedicated preparation", operation: func(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, _ models.Session) error {
			raw, err := json.Marshal(prepareCodeReviewWorkspacePayload{OrgID: job.OrgID, SessionID: job.SessionID, MetadataID: job.MetadataID})
			if err != nil {
				return err
			}
			return newPrepareCodeReviewWorkspaceHandler(stores, services, zerolog.Nop())(ctx, "", raw)
		}},
		{name: "initial deadline", operation: func(ctx context.Context, stores *Stores, services *Services, _ runCodeReviewPayload, session models.Session) error {
			deadline, err := initialCodeReviewPreparationDeadline(ctx, stores, services, zerolog.Nop(), session, &models.SessionThread{ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly}, false)
			if deadline != nil {
				return fmt.Errorf("outside repository unexpectedly inherited preparation budget")
			}
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "outside repository fixture must initialize")
			defer mock.Close()
			orgID, repoID, sessionID := uuid.New(), uuid.New(), uuid.New()
			if tt.name == "initial deadline" {
				expectPreparationRepository(mock, orgID, repoID)
			} else {
				expectPreparationSession(mock, orgID, sessionID, repoID)
			}
			policy, err := reviewpreparation.NewPolicy(true, []string{"other/repository"})
			require.NoError(t, err, "other repository policy must construct")
			stores := &Stores{Sessions: db.NewSessionStore(mock), Repositories: db.NewRepositoryStore(mock)}
			session := models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID, Origin: models.SessionOriginCodeReview, RevisionContext: json.RawMessage(`{"head_sha":"legacy-head"}`)}
			err = tt.operation(context.Background(), stores, &Services{ReviewPreparationPolicy: policy}, runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, MetadataID: uuid.New()}, session)
			require.NoError(t, err, "outside repository must bypass every preparation-specific store and dependency")
			require.NoError(t, mock.ExpectationsWereMet(), "outside repository must perform only tenant-scoped identity reads")
		})
	}
}
