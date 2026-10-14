package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Intercept the real latest-assessment read, which occurs after successful
// capture's first PR transaction commits and before its Wait route requeues.
// All SQL and commits still use PostgreSQL; no production hook is necessary.
type waitOwnerRacePool struct {
	*pgxpool.Pool
	committed atomic.Bool
	intercept func(context.Context) error
}

func (p *waitOwnerRacePool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &waitOwnerRaceTx{Tx: tx, committed: &p.committed}, nil
}

func (p *waitOwnerRacePool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if p.intercept != nil && strings.Contains(sql, "FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 ORDER BY generation DESC LIMIT 1") {
		intercept := p.intercept
		p.intercept = nil
		if !p.committed.Load() {
			return nil, errors.New("Wait race must happen after the successful-capture transaction commits")
		}
		if err := intercept(ctx); err != nil {
			return nil, err
		}
	}
	return p.Pool.Query(ctx, sql, args...)
}

type waitOwnerRaceTx struct {
	pgx.Tx
	committed *atomic.Bool
}

func (tx *waitOwnerRaceTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	tx.committed.Store(true)
	return nil
}

func TestCodeReviewWaitCaptureOwnershipPostgres(t *testing.T) {
	// PostgreSQL timestamps use the lazily initialized local location. Initialize
	// it before parallel assertion diagnostics inspect its internal fields.
	require.NotEmpty(t, time.Local.String(), "initialize the timestamp location before parallel assertion diagnostics")
	t.Parallel()
	raw := os.Getenv("CODE_REVIEW_TEST_DATABASE_URL")
	if raw == "" {
		raw = os.Getenv("TEST_DATABASE_URL")
	}
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL or CODE_REVIEW_TEST_DATABASE_URL to a disposable PostgreSQL server")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect disposable PostgreSQL server")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close Wait-race administrative connection") })
	name := "review_wait_owner_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE DATABASE `+name)
	require.NoError(t, err, "create an isolated Wait-race database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err, "drop only the isolated Wait-race database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable PostgreSQL URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve the real migration chain")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "open isolated Wait-race migrations")
	require.NoError(t, migration.Up(), "fully migrate the real Wait-race schema")
	sourceErr, dbErr := migration.Close()
	require.NoError(t, sourceErr, "close Wait-race migration source")
	require.NoError(t, dbErr, "close Wait-race migration connection")
	config, err := pgxpool.ParseConfig(parsed.String())
	require.NoError(t, err, "parse the isolated Wait-race pool configuration")
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "connect the bounded Wait-race pool")
	t.Cleanup(pool.Close)
	tests := []struct {
		name, owner string
	}{
		{name: "new explicit owner survives post-capture Wait", owner: "newer"},
		{name: "automatic revision race retains fresh intake", owner: "none"},
		{name: "same preexisting owner is not a newer request", owner: "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, integration, repo, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Wait ownership test')`, org)
			require.NoError(t, err, "seed isolated synthetic tenant")
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed synthetic integration")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,$4,'test/repo','https://github.com/test/repo.git',17)`, repo, org, integration, int64(repo.ID()))
			require.NoError(t, err, "seed tenant-scoped repository")
			_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,17,'https://github.com/test/repo/pull/17','test/repo','Scheduling test')`, pr, org)
			require.NoError(t, err, "seed tenant-scoped PR")
			snapshot := &schedulingSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: 17, State: "open", Title: "Scheduling test", HTMLURL: "https://github.com/test/repo/pull/17", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseRef: "main"}}
			testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
			store := db.NewCodeReviewStore(pool)
			schedules := db.NewCodeReviewScheduleStore(pool)
			latest, err := schedules.GetLatestAssessment(ctx, org, pr)
			require.NoError(t, err, "read the protected active assessment")
			manifest, err := decodeAssessmentManifest(latest.InputManifest)
			require.NoError(t, err, "decode synthetic captured input")
			policy, err := store.ResolvePolicy(ctx, org)
			require.NoError(t, err, "read the synthetic continuation policy")
			require.NotNil(t, policy.Policy, "capture must bind a saved policy")
			fixture := assessmentAdmissionFixture{pool: pool, manifest: manifest, policy: *policy.Policy, session: latest.SessionID, snapshot: snapshot.snapshot}
			now := time.Now().UTC().Truncate(time.Microsecond)
			newService := func(scheduleStore *db.CodeReviewScheduleStore) *Service {
				service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
				service.SetScheduling(scheduleStore, snapshot)
				service.scheduling.now = func() time.Time { return now }
				return service
			}
			intake := newService(schedules)
			previous := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck, Reason: "Preexisting explicit intent"}
			if tt.owner == "same" {
				hash, err := reviewRequestHash(pr, previous.Mode, assessmentRequestContext(previous), false)
				require.NoError(t, err, "hash the preexisting explicit owner")
				_, err = intake.queuePendingAssessmentCapture(ctx, previous, repo, assessmentRequestContext(previous), hash, "github")
				require.NoError(t, err, "install ownership before the older caller starts capture")
			}
			request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck, Reason: "Original in-flight intake"}
			var expected models.CodeReviewPRState
			type episode struct {
				Started *time.Time
				Tracked bool
			}
			var expectedEpisode episode
			newerStarted := now.Add(-3 * time.Minute)
			var newer ScheduleRequestInput
			intercepts := 0
			racePool := &waitOwnerRacePool{Pool: pool}
			racePool.intercept = func(ctx context.Context) error {
				intercepts++
				if tt.owner == "newer" {
					newer = ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck, Reason: "New explicit acceptance criteria"}
					hash, err := reviewRequestHash(pr, newer.Mode, assessmentRequestContext(newer), false)
					if err != nil {
						return err
					}
					queued, err := intake.queuePendingAssessmentCapture(ctx, newer, repo, assessmentRequestContext(newer), hash, "github")
					if err != nil {
						return err
					}
					require.NotNil(t, queued.Schedule.PendingRequestID, "newer request must own intake before the stale Wait requeue")
					// Seed a meaningful unknown legacy episode. A direct synthetic
					// queue call is not an actual failed capture of this generation.
					if _, err := pool.Exec(ctx, `UPDATE code_review_requests SET capture_failure_started_at=$3,capture_failure_clock_tracked=false WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID, newerStarted); err != nil {
						return err
					}
				}
				if err := schedules.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
					state.Generation++
					state.HeadSHA, state.BaseSHA, state.BaseRef = strings.Repeat("c", 40), strings.Repeat("d", 40), "release"
					first, retry, eligible := now.Add(-7*time.Minute), now.Add(4*time.Minute), now.Add(time.Minute)
					state.FirstPendingAt, state.RetryAt, state.EligibleAt = &first, &retry, &eligible
					state.State, state.WaitReason = models.CodeReviewScheduleWaiting, models.CodeReviewWaitContext
					return db.UpsertCodeReviewWake(ctx, tx, org, pr, retry)
				}); err != nil {
					return err
				}
				var err error
				expected, err = schedules.Get(ctx, org, pr)
				if err != nil {
					return err
				}
				if expected.PendingRequestID != nil {
					if err := pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, expected.PendingRequestID).Scan(&expectedEpisode.Started, &expectedEpisode.Tracked); err != nil {
						return err
					}
					if tt.owner == "newer" {
						require.NotNil(t, expectedEpisode.Started, "newer owner must have a real episode before the stale Wait requeue")
						require.Equal(t, newerStarted, expectedEpisode.Started.UTC(), "newer owner's existing episode must have the exact seeded start")
						require.False(t, expectedEpisode.Tracked, "newer owner's legacy timing must remain unknown before the stale Wait requeue")
					}
				}
				return nil
			}
			service := newService(db.NewCodeReviewScheduleStore(racePool))
			service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, input AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
				captured, err := fixture.CaptureAssessmentInputs(ctx, input)
				// A successful provider response can still have incomplete inputs;
				// the planner must take its real Wait route, after its first lock.
				captured.Manifest.InputDigest = ""
				return captured, err
			}), true)
			result, err := service.RequestScheduledReview(ctx, request)
			require.NoError(t, err, "post-capture Wait must reconcile the concurrent target safely")
			require.Equal(t, 1, intercepts, "the competing commit must occur exactly between the first capture lock and Wait requeue")
			actual, err := schedules.Get(ctx, org, pr)
			require.NoError(t, err, "read schedule after the stale Wait branch")
			original, err := schedules.GetRequestByIdentity(ctx, org, "github", request.RequestID.String())
			require.NoError(t, err, "original explicit intake must have a durable disposition")
			if tt.owner == "newer" {
				require.Equal(t, models.CodeReviewRequestCancelled, result.Disposition, "only a genuinely newer explicit owner may supersede the original caller")
				require.Equal(t, "superseded", original.Status, "obsolete caller must remain durably superseded")
				require.JSONEq(t, string(expected.PendingInput), string(actual.PendingInput), "Wait must preserve the complete newer intent despite JSONB serialization")
				comparison := actual
				comparison.UpdatedAt, comparison.PendingInput = expected.UpdatedAt, expected.PendingInput
				require.Equal(t, expected, comparison, "Wait must preserve the exact newer schedule, intent, revisions, and clocks")
				record, err := schedules.GetRequestByIdentity(ctx, org, "github", newer.RequestID.String())
				require.NoError(t, err, "read the protected newer request ledger")
				require.Equal(t, "pending", record.Status, "newer intake must remain pending")
				var actualEpisode episode
				require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, record.ID).Scan(&actualEpisode.Started, &actualEpisode.Tracked), "read the newer owner's exact failure episode")
				require.Equal(t, expectedEpisode, actualEpisode, "old successful capture must not clear or renew the newer failure episode")
			} else {
				require.Equal(t, models.CodeReviewRequestQueued, result.Disposition, "a revision change without a newer owner must retain original intake")
				require.Equal(t, "pending", original.Status, "original request must stay pending rather than disappear")
				hash, err := reviewRequestHash(pr, request.Mode, assessmentRequestContext(request), false)
				require.NoError(t, err, "hash the surviving original intake")
				require.Equal(t, hash, original.InputHash, "original request context must retain its exact identity")
				input := ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: request.RequestID.String(), RequestContext: assessmentRequestContext(request), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}
				pending, err := db.EncodeCodeReviewScheduleInput(scheduledReviewIntent{Input: input, Mode: models.CodeReviewRecheck})
				require.NoError(t, err, "encode the surviving original intent")
				expected.PendingInput, expected.PendingRequestID = pending, &original.ID
				require.JSONEq(t, string(expected.PendingInput), string(actual.PendingInput), "retained intake must preserve its complete intent despite JSONB serialization")
				comparison := actual
				comparison.UpdatedAt, comparison.PendingInput = expected.UpdatedAt, expected.PendingInput
				require.Equal(t, expected, comparison, "retained intake must preserve current target, active ownership, age, and later retry deadline")
				var generation int64
				require.NoError(t, pool.QueryRow(ctx, `SELECT target_generation FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, original.ID).Scan(&generation), "read the retargeted original request generation")
				require.Equal(t, expected.Generation, generation, "retained intake must target the authoritative generation")
				if tt.owner == "same" {
					record, err := schedules.GetRequestByIdentity(ctx, org, "github", previous.RequestID.String())
					require.NoError(t, err, "read the previous owner's durable disposition")
					require.Equal(t, "superseded", record.Status, "the expected preexisting owner must not be mistaken for newer intake")
				}
			}
			// The race exercises scheduling only: immutable assessment inputs and
			// active publication/session ownership must retain their exact values.
			after, err := schedules.GetLatestAssessment(ctx, org, pr)
			require.NoError(t, err, "read protected assessment after Wait reconciliation")
			require.Equal(t, latest, after, "Wait requeue must never retire or mutate active assessment evidence")
			var storedIntent scheduledReviewIntent
			require.NoError(t, json.Unmarshal(actual.PendingInput, &storedIntent), "pending intent must remain decodable")
		})
	}
}
