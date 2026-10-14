package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newWorkerPreparationIntent() models.CodeReviewPreparationIntent {
	return models.CodeReviewPreparationIntent{Version: 1, OrgID: uuid.New(), JobID: uuid.New(), MetadataID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(), HeadSHA: "aabbcc", WorkspaceGeneration: 4, Turn: 1, TurnScope: "session", FailureClass: models.CodeReviewPreparationFailureSetup, Stage: "dependency_setup"}
}

func TestTerminalCodeReviewPreparationIntentClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		makeError func() error
		class     models.CodeReviewPreparationFailureClass
		terminal  bool
	}{
		{name: "deterministic setup joined with failed result write", makeError: func() error {
			return errors.Join(agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("token=synthetic-secret")), errors.New("persist thread result: database unavailable"))
		}, class: models.CodeReviewPreparationFailureSetup, terminal: true},
		{name: "declared setup failure cannot hide behind readiness", makeError: func() error {
			return errors.Join(agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("declared command failed")), agent.ErrSandboxWorkspaceNotReady)
		}, class: models.CodeReviewPreparationFailureSetup, terminal: true},
		{name: "cleanup dominates joined readiness", makeError: func() error {
			return errors.Join(agent.ErrSandboxWorkspaceNotReady, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, agent.ErrCodeReviewPreparationCleanup))
		}, class: models.CodeReviewPreparationFailureCleanup, terminal: true},
		{name: "integrity dominates capacity", makeError: func() error {
			return errors.Join(agent.ErrSandboxCapacity, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, agent.ErrCodeReviewSourceIntegrity))
		}, class: models.CodeReviewPreparationFailureIntegrity, terminal: true},
		{name: "own preparation deadline dominates readiness", makeError: func() error {
			return errors.Join(agent.ErrSandboxWorkspaceNotReady, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, context.DeadlineExceeded))
		}, class: models.CodeReviewPreparationFailureDeadline, terminal: true},
		{name: "cleanup dominates user cancellation", makeError: func() error {
			return errors.Join(agent.ErrSessionCancelled, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, agent.ErrCodeReviewPreparationCleanup))
		}, class: models.CodeReviewPreparationFailureCleanup, terminal: true},
		{name: "pure typed readiness remains retryable", makeError: func() error {
			return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, agent.ErrSandboxWorkspaceNotReady)
		}},
		{name: "pure capacity remains retryable", makeError: func() error { return agent.ErrSandboxCapacity }},
		{name: "moved head keeps supersession", makeError: func() error {
			return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageCheckout, agent.ErrStalePullRequestHead)
		}},
		{name: "user cancellation keeps cancellation authority", makeError: func() error {
			return agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageReadiness, context.Canceled)
		}},
		{name: "ordinary persistence failure has no preparation authority", makeError: func() error { return errors.New("database unavailable") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			captured := newWorkerPreparationIntent()
			before := captured
			actual := terminalCodeReviewPreparationIntent(&captured, tt.makeError())
			if !tt.terminal {
				require.NoError(t, actual, "retry and ownership causes must retain their existing continuation path")
				return
			}
			var fatal *FatalError
			var intentErr *codeReviewPreparationIntentError
			require.ErrorAs(t, actual, &fatal, "unsafe preparation must retire the continuation rather than replay setup")
			require.ErrorAs(t, actual, &intentErr, "terminal writer must receive immutable preparation provenance")
			expected := before
			expected.FailureClass = tt.class
			if tt.class == models.CodeReviewPreparationFailureCleanup && tt.name == "cleanup dominates joined readiness" || tt.class == models.CodeReviewPreparationFailureIntegrity {
				expected.Stage = "hydration"
			} else if tt.class == models.CodeReviewPreparationFailureDeadline || tt.name == "cleanup dominates user cancellation" {
				expected.Stage = "readiness"
			}
			require.Equal(t, expected, intentErr.Intent, "classification must preserve exact pre-call generation and nullable thread identity")
			require.Equal(t, before, captured, "classifying a terminal result must not mutate captured admission provenance")
			require.NotContains(t, actual.Error(), "synthetic-secret", "raw command diagnostics must not enter durable terminal failure text")
			require.NotContains(t, actual.Error(), "persist thread", "joined database diagnostics must not become preparation evidence")
		})
	}
}

func TestCapturePreparationIntentInactiveAndRecheckHaveNoNewAdmissionDependency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		origin   models.SessionOrigin
		recheck  bool
		services *Services
	}{
		{name: "inactive code review", origin: models.SessionOriginCodeReview, services: &Services{}},
		{name: "recheck retains assessment authority", origin: models.SessionOriginCodeReview, recheck: true, services: &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true)}},
		{name: "ordinary session", services: &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), uuid.New()), uuid.New())
			actual, err := captureCodeReviewPreparationIntent(ctx, stores, tt.services, models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: tt.origin}, nil, tt.recheck)
			require.NoError(t, err, "inactive and recheck paths must preserve their existing admission contract")
			require.Nil(t, actual, "only activated baseline recipes may acquire a terminal projection intent")
			require.NoError(t, mock.ExpectationsWereMet(), "inactive paths must not perform new metadata or provider eligibility queries")
		})
	}
}

type workerPreparationIntentStore struct {
	terminalLeaseStoreStub
	intentCalls, plainCalls int
	owned                   bool
	writeErr                error
	actual                  models.CodeReviewPreparationIntent
	token                   uuid.UUID
}

func (s *workerPreparationIntentStore) DeadLetterContinueSessionWithPreparationIntentWithLease(_ context.Context, org, job, token uuid.UUID, intent models.CodeReviewPreparationIntent) (bool, error) {
	s.intentCalls++
	s.actual = intent
	s.token = token
	if org != intent.OrgID || job != intent.JobID {
		return false, errors.New("unexpected intent identity")
	}
	return s.owned, s.writeErr
}

type executorPreparationIntentStore struct {
	executorRuntimeJobStoreStub
	intentCalls int
	owned       bool
	writeErr    error
	actual      models.CodeReviewPreparationIntent
	token       uuid.UUID
}

func (s *executorPreparationIntentStore) DeadLetterContinueSessionWithPreparationIntentWithLease(_ context.Context, org, job, token uuid.UUID, intent models.CodeReviewPreparationIntent) (bool, error) {
	s.intentCalls++
	s.actual = intent
	s.token = token
	if org != intent.OrgID || job != intent.JobID {
		return false, errors.New("unexpected intent identity")
	}
	return s.owned, s.writeErr
}

func TestPreparationIntentTerminalWritersRequireCommittedOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		executor, owned, unsupported bool
		writeErr                     error
		wantHooks                    int
	}{
		{name: "worker commits atomic intent", owned: true, wantHooks: 1},
		{name: "worker lost lease"}, {name: "worker DB outage", writeErr: errors.New("DB unavailable")}, {name: "worker unsupported capability", unsupported: true},
		{name: "executor commits atomic intent", executor: true, owned: true, wantHooks: 1},
		{name: "executor lost lease", executor: true}, {name: "executor DB outage", executor: true, writeErr: errors.New("DB unavailable")}, {name: "executor unsupported capability", executor: true, unsupported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			intent := newWorkerPreparationIntent()
			token := uuid.New()
			job := &models.Job{ID: intent.JobID, OrgID: intent.OrgID, JobType: "continue_session", LockToken: &token}
			terminal := terminalCodeReviewPreparationIntent(&intent, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("secret diagnostics")))
			hooks := 0
			ctx := jobctx.WithDeadLetterHooks(context.Background())
			jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) { hooks++ })
			if tt.executor {
				store := &executorPreparationIntentStore{owned: tt.owned, writeErr: tt.writeErr}
				runtime := &SessionExecutorRuntime{Jobs: store, Logger: zerolog.Nop()}
				if tt.unsupported {
					runtime.Jobs = &store.executorRuntimeJobStoreStub
				}
				runtime.markJobDeadLetter(ctx, ctx, models.SessionExecutor{LockToken: token}, job, terminal)
				wantCalls := 1
				if tt.unsupported {
					wantCalls = 0
				}
				require.Equal(t, wantCalls, store.intentCalls, "executor terminal path must use the atomic preparation capability")
				require.Zero(t, store.deadLetterCalls, "executor must not fall back to lossy plain deadletter")
				if !tt.unsupported {
					require.Equal(t, intent, store.actual, "executor checkpoint must preserve immutable original failure provenance")
					require.Equal(t, token, store.token, "executor checkpoint must use the executor attempt fence")
				}
			} else {
				store := &workerPreparationIntentStore{owned: tt.owned, writeErr: tt.writeErr}
				store.deadLetterFn = func(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
					store.plainCalls++
					return true, nil
				}
				w := &Worker{jobs: store, logger: zerolog.Nop()}
				if tt.unsupported {
					w.jobs = &store.terminalLeaseStoreStub
				}
				w.deadLetterJob(ctx, ctx, job, terminal)
				wantCalls := 1
				if tt.unsupported {
					wantCalls = 0
				}
				require.Equal(t, wantCalls, store.intentCalls, "worker terminal path must use the atomic preparation capability")
				require.Zero(t, store.plainCalls, "worker must not fall back to lossy plain deadletter")
				if !tt.unsupported {
					require.Equal(t, intent, store.actual, "worker checkpoint must preserve immutable original failure provenance")
					require.Equal(t, token, store.token, "worker checkpoint must use the claimed attempt fence")
				}
			}
			require.Equal(t, tt.wantHooks, hooks, "cleanup hooks may run only after the owned terminal intent commits")
		})
	}
}

func TestPreparationIntentAbsentDoesNotPromoteGenericErrors(t *testing.T) {
	t.Parallel()
	require.NoError(t, terminalCodeReviewPreparationIntent(nil, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("failed"))), "uncaptured errors must not acquire invented review provenance")
	raw, err := json.Marshal(newWorkerPreparationIntent())
	require.NoError(t, err, "finite worker intent should serialize")
	require.NotContains(t, string(raw), "lock_token", "durable failure intent must not store lease credentials")
}

func TestContinueHandlerCheckpointsFailedPreparationWithoutReplayingJoinedResultWriteFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		explicit bool
	}{{name: "explicit thread retains its turn", explicit: true}, {name: "inferred continuation keeps nullable session scope"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			stores.Repositories = db.NewRepositoryStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			org, sessionID, repository, threadID, metadata, pr, jobID, token := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			row := workerSessionRow(sessionID, uuid.Nil, org, models.SessionStatusRunning, 2, nil, nil)
			setWorkerSessionColumn(row, "origin", models.SessionOriginCodeReview)
			setWorkerSessionColumn(row, "repository_id", &repository)
			setWorkerSessionColumn(row, "workspace_generation", int64(4))
			setWorkerSessionColumn(row, "revision_context", json.RawMessage(`{"github_repo":"test/preparation"}`))
			mock.ExpectQuery("FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": org}).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(row...))
			input := continueSessionJobInput{OrgID: org.String(), SessionID: sessionID.String()}
			if tt.explicit {
				input.ThreadID = threadID.String()
				thread := workerSessionThreadRow(threadID, sessionID, org, models.AgentTypeCodex, nil, models.ThreadStatusRunning)
				setWorkerSessionThreadColumn(thread, "current_turn", 1)
				mock.ExpectQuery("FROM session_threads").WithArgs(pgx.NamedArgs{"id": threadID, "org_id": org}).WillReturnRows(pgxmock.NewRows(workerSessionThreadColumns).AddRow(thread...))
			}
			expectPreparationRepository(mock, org, repository) // Existing preparation deadline policy.
			expectPreparationRepository(mock, org, repository) // Activated baseline admission only.
			mock.ExpectQuery("SELECT pull_request_id FROM code_review_session_metadata").WithArgs(org, sessionID).WillReturnRows(pgxmock.NewRows([]string{"pull_request_id"}).AddRow(pr))
			mock.ExpectBegin()
			for _, setting := range []string{"SET LOCAL lock_timeout", "SET LOCAL statement_timeout", "SET LOCAL idle_in_transaction_session_timeout"} {
				mock.ExpectExec(setting).WillReturnResult(pgxmock.NewResult("SET", 0))
			}
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			var expectedThread *uuid.UUID
			beforeTurn := 2
			if tt.explicit {
				expectedThread = &threadID
				beforeTurn = 1
			}
			mock.ExpectQuery("(?s)SELECT m.id,m.pull_request_id,m.head_sha.*FOR NO KEY UPDATE OF s,m,j").WithArgs(pgx.NamedArgs{"org_id": org, "job_id": jobID, "session_id": sessionID, "pr_id": pr, "generation": int64(4), "token": token, "thread_id": expectedThread, "before_turn": beforeTurn}).WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "head_sha"}).AddRow(metadata, pr, "pinnedhead"))
			mock.ExpectCommit()
			orchestrator := &orchestratorServiceStub{continueSessionFn: func(_ context.Context, session *models.Session, options *agent.ContinueSessionOptions) error {
				require.Equal(t, int64(4), session.WorkspaceGeneration, "orchestrator must receive the originally admitted generation")
				if tt.explicit {
					require.Equal(t, &threadID, options.ThreadID, "explicit setup must retain its exact thread")
				} else {
					require.Nil(t, options, "unscoped setup must not guess a sibling before ContinueSession")
				}
				session.WorkspaceGeneration = 99
				return errors.Join(agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("token=synthetic-secret")), errors.New("UpdateResult persistence unavailable"))
			}}
			raw, err := json.Marshal(input)
			require.NoError(t, err, "continuation payload should encode")
			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), token)
			result := newContinueSessionHandler(stores, &Services{Orchestrator: orchestrator, ReviewPreparationPolicy: workspacePreparationTestPolicy(true)}, zerolog.Nop())(ctx, "continue_session", raw)
			var intentError *codeReviewPreparationIntentError
			require.ErrorAs(t, result, &intentError, "joined result persistence failure must still carry a durable terminal preparation intent")
			expected := models.CodeReviewPreparationIntent{Version: 1, OrgID: org, JobID: jobID, MetadataID: metadata, SessionID: sessionID, PullRequestID: pr, HeadSHA: "pinnedhead", WorkspaceGeneration: 4, Turn: 3, TurnScope: "session", FailureClass: models.CodeReviewPreparationFailureSetup, Stage: "dependency_setup"}
			if tt.explicit {
				expected.ThreadID = &threadID
				expected.Turn = 2
				expected.TurnScope = "thread"
			}
			require.Equal(t, expected, intentError.Intent, "post-call mutable session state must not replace observed generation or turn scope")
			require.Equal(t, 1, orchestrator.continueSessionCalls, "failed preparation must never replay to recover a thread result write")
			encoded, err := json.Marshal(expected)
			require.NoError(t, err, "expected checkpoint should encode")
			thread := ""
			if tt.explicit {
				thread = threadID.String()
			}
			mock.ExpectExec("(?s)UPDATE jobs SET status='dead_letter'.*terminal_preparation_intent.*lock_token=\\$3").WithArgs(org, jobID, token, encoded, sessionID.String(), thread).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			worker := &Worker{jobs: stores.Jobs, logger: zerolog.Nop()}
			worker.deadLetterJob(ctx, ctx, &models.Job{ID: jobID, OrgID: org, JobType: "continue_session", LockToken: &token}, result)
			require.NoError(t, mock.ExpectationsWereMet(), "failed result persistence must retire the owned job and intent atomically without more provider or thread-result calls")
		})
	}
}

func TestTerminalPreparationProjectionOutageStopsControllerBeforeProviderWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		duringProjection bool
	}{{name: "journal read outage"}, {name: "projection transaction outage", duringProjection: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			intent := newWorkerPreparationIntent()
			job := runCodeReviewPayload{OrgID: intent.OrgID, MetadataID: intent.MetadataID, SessionID: intent.SessionID}
			query := mock.ExpectQuery("(?s)SELECT id, CASE WHEN octet_length").WithArgs(intent.OrgID, intent.SessionID.String(), intent.MetadataID.String())
			if tt.duringProjection {
				raw, err := json.Marshal(intent)
				require.NoError(t, err, "finite intent should encode")
				query.WillReturnRows(pgxmock.NewRows([]string{"id", "intent", "oversized"}).AddRow(intent.JobID, raw, false))
				mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))
			} else {
				query.WillReturnError(errors.New("database unavailable"))
			}
			orchestrator := &orchestratorServiceStub{continueSessionFn: func(context.Context, *models.Session, *agent.ContinueSessionOptions) error {
				t.Fatal("projection must not invoke ContinueSession")
				return nil
			}}
			stopped, err := reconcileTerminalCodeReviewPreparationIntents(context.Background(), stores, &Services{Orchestrator: orchestrator}, zerolog.Nop(), job)
			require.True(t, stopped, "controller must stop phase transitions when terminal projection storage is unavailable")
			require.ErrorContains(t, err, "database unavailable", "controller retry must retain the storage cause for diagnosis")
			require.Zero(t, orchestrator.continueSessionCalls, "projection recovery must perform no provider or recipe setup")
			require.NoError(t, mock.ExpectationsWereMet(), "projection retry must not issue unrelated phase or preparation queries")
		})
	}
}

func TestTerminalPreparationJournalFullMalformedBatchBlocksPhaseTransitions(t *testing.T) {
	t.Parallel()
	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.CodeReviews = db.NewCodeReviewStore(mock)
	org, metadata, session := uuid.New(), uuid.New(), uuid.New()
	job := runCodeReviewPayload{OrgID: org, MetadataID: metadata, SessionID: session}
	rows := pgxmock.NewRows([]string{"id", "intent", "oversized"})
	ids := make([]uuid.UUID, 32)
	raw := json.RawMessage(`{"metadata_id":"` + metadata.String() + `"}`)
	for i := range ids {
		ids[i] = uuid.New()
		rows.AddRow(ids[i], raw, false)
	}
	mock.ExpectQuery("(?s)SELECT id, CASE WHEN octet_length").WithArgs(org, session.String(), metadata.String()).WillReturnRows(rows)
	for _, id := range ids {
		mock.ExpectExec("(?s)UPDATE jobs SET payload=.*terminal_preparation_intent_consumed.*org_id=\\$1 AND id=\\$2.*payload->'terminal_preparation_intent'=\\$5::jsonb").WithArgs(org, id, session.String(), metadata.String(), []byte(raw)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	orchestrator := &orchestratorServiceStub{continueSessionFn: func(context.Context, *models.Session, *agent.ContinueSessionOptions) error {
		t.Fatal("full journal must not enter provider setup")
		return nil
	}}
	stopped, err := reconcileTerminalCodeReviewPreparationIntents(context.Background(), stores, &Services{Orchestrator: orchestrator}, zerolog.Nop(), job)
	require.True(t, stopped, "a full rejected batch may hide a later valid failure and must stop this phase pass")
	var retry *RetryableError
	require.ErrorAs(t, err, &retry, "controller should poll the next bounded journal batch before harvesting or fanout")
	require.Zero(t, orchestrator.continueSessionCalls, "rejecting malformed records must never grant preparation authority")
	require.NoError(t, mock.ExpectationsWereMet(), "malformed journal consumption must be tenant/job/raw-value fenced without metadata writes")
}

func TestActivatedPreparationRejectsMissingDurableJobProvenance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		makeContext func() context.Context
	}{
		{name: "missing job and lease", makeContext: context.Background},
		{name: "missing lease", makeContext: func() context.Context { return jobctx.WithJobID(context.Background(), uuid.New()) }},
		{name: "missing job", makeContext: func() context.Context { return jobctx.WithLockToken(context.Background(), uuid.New()) }},
		{name: "nil job identity", makeContext: func() context.Context {
			return jobctx.WithLockToken(jobctx.WithJobID(context.Background(), uuid.Nil), uuid.New())
		}},
		{name: "nil lease identity", makeContext: func() context.Context {
			return jobctx.WithLockToken(jobctx.WithJobID(context.Background(), uuid.New()), uuid.Nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.Repositories = db.NewRepositoryStore(mock)
			org, repo := uuid.New(), uuid.New()
			expectPreparationRepository(mock, org, repo)
			session := models.Session{ID: uuid.New(), OrgID: org, Origin: models.SessionOriginCodeReview, RepositoryID: &repo, RevisionContext: json.RawMessage(`{"github_repo":"test/preparation"}`)}
			actual, err := captureCodeReviewPreparationIntent(tt.makeContext(), stores, &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true)}, session, nil, false)
			require.Nil(t, actual, "missing job provenance must never acquire failure projection authority")
			var fatal *FatalError
			require.ErrorAs(t, err, &fatal, "activated recipe execution must stop before provider setup without a recoverable job checkpoint")
			require.NoError(t, mock.ExpectationsWereMet(), "missing durable provenance must not be reconstructed from mutable metadata or provider state")
		})
	}
}
