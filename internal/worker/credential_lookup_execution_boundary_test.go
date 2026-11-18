package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type credentialBoundarySessions struct {
	agent.SessionStore
	mu      sync.Mutex
	session models.Session
	results []models.SessionStatus
}

func (s *credentialBoundarySessions) CountExecutingByOrg(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}
func (s *credentialBoundarySessions) BeginRuntime(context.Context, uuid.UUID, uuid.UUID, models.CheckpointCapability, time.Time, time.Time, time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.Status = models.SessionStatusRunning
	return nil
}
func (s *credentialBoundarySessions) UpdateStatus(_ context.Context, _, _ uuid.UUID, status models.SessionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.Status = status
	return nil
}
func (s *credentialBoundarySessions) UpdateSandboxState(context.Context, uuid.UUID, uuid.UUID, models.SandboxState) error {
	return nil
}
func (s *credentialBoundarySessions) AcquireTurnHold(_ context.Context, _, _ uuid.UUID, containerID string) (string, error) {
	return containerID, nil
}
func (s *credentialBoundarySessions) ReleaseTurnHold(context.Context, uuid.UUID, uuid.UUID) (bool, string, error) {
	return false, "", nil
}
func (s *credentialBoundarySessions) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session, nil
}
func (s *credentialBoundarySessions) UpdateResult(_ context.Context, _, _ uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.Status = status
	s.session.Error = result.Error
	s.results = append(s.results, status)
	return nil
}
func (s *credentialBoundarySessions) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	return nil
}
func (s *credentialBoundarySessions) ConsumeCancelRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (s *credentialBoundarySessions) RecordRuntimeProgress(context.Context, uuid.UUID, uuid.UUID, models.RuntimeProgressType, models.RuntimeProgressStrength, time.Time) error {
	return nil
}

type credentialBoundaryMessages struct{ messages []models.SessionMessage }

func (m *credentialBoundaryMessages) ListBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionMessage, error) {
	return append([]models.SessionMessage(nil), m.messages...), nil
}
func (m *credentialBoundaryMessages) Create(_ context.Context, message *models.SessionMessage) error {
	m.messages = append(m.messages, *message)
	return nil
}

type credentialBoundaryInbox struct {
	agent.ThreadInboxStore
	mu      sync.Mutex
	applied map[int64]bool
}

func (i *credentialBoundaryInbox) ListAppliedMessageIDs(_ context.Context, _, _, _ uuid.UUID, ids []int64) ([]int64, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	var applied []int64
	for _, id := range ids {
		if i.applied[id] {
			applied = append(applied, id)
		}
	}
	return applied, nil
}
func (i *credentialBoundaryInbox) ListUnsettledAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return nil, nil
}
func (i *credentialBoundaryInbox) LastAcknowledgedSequence(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (i *credentialBoundaryInbox) MarkDeliveringForMessages(_ context.Context, _, _, _, _ uuid.UUID, _ string, ids []int64) (int64, error) {
	return int64(len(ids)), nil
}
func (i *credentialBoundaryInbox) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, ids []int64) (int64, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, id := range ids {
		i.applied[id] = true
	}
	return int64(len(ids)), nil
}
func (i *credentialBoundaryInbox) CountPendingByThread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}

type credentialBoundaryRuntimes struct {
	agent.ThreadRuntimeStore
	terminal []models.ThreadRuntimeStatus
}

func (r *credentialBoundaryRuntimes) CreateStarting(_ context.Context, orgID uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	return models.ThreadRuntime{ID: uuid.New(), OrgID: orgID, SessionID: params.SessionID, ThreadID: params.ThreadID, LeaseToken: params.LeaseToken}, nil
}
func (r *credentialBoundaryRuntimes) MarkTerminalWithLease(_ context.Context, _, _, _ uuid.UUID, status models.ThreadRuntimeStatus, _, _ string) (bool, error) {
	r.terminal = append(r.terminal, status)
	return true, nil
}
func (r *credentialBoundaryRuntimes) HeartbeatWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return true, nil
}

type credentialBoundaryJobs struct{ agent.JobStore }

func (credentialBoundaryJobs) Enqueue(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (credentialBoundaryJobs) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return 0, false, nil
}

type credentialBoundaryProvider struct{ agent.SandboxProvider }

func (credentialBoundaryProvider) Name() string { return "synthetic" }
func (credentialBoundaryProvider) Create(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{ID: "synthetic-container", Provider: "synthetic", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
}
func (credentialBoundaryProvider) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}
func (credentialBoundaryProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	return nil
}
func (credentialBoundaryProvider) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}
func (credentialBoundaryProvider) Destroy(context.Context, *agent.Sandbox) error { return nil }

type credentialBoundaryCoding struct {
	mu     sync.Mutex
	rows   []models.DecryptedCodingCredential
	index  int
	failed bool
}

func (c *credentialBoundaryCoding) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return nil, errors.New("synthetic credential lookup failure")
	}
	if provider != models.ProviderAmp {
		return nil, nil
	}
	return append([]models.DecryptedCodingCredential(nil), c.rows...), nil
}
func (c *credentialBoundaryCoding) PickRunnable(context.Context, models.Scope, models.ProviderName) (*models.DecryptedCodingCredential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rows[c.index]
	c.index++
	return &row, nil
}
func (c *credentialBoundaryCoding) PickRunnableMulti(ctx context.Context, scope models.Scope, _ []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	return c.PickRunnable(ctx, scope, models.ProviderAmp)
}
func (c *credentialBoundaryCoding) MarkRateLimited(uuid.UUID)  {}
func (c *credentialBoundaryCoding) MarkAuthRejected(uuid.UUID) {}

type credentialBoundaryAdapter struct {
	coding    *credentialBoundaryCoding
	failAfter int
	calls     int
}

func (*credentialBoundaryAdapter) Name() models.AgentType { return models.AgentTypeAmp }
func (*credentialBoundaryAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeUnsupported
}
func (*credentialBoundaryAdapter) PreparePrompt(context.Context, *agent.AgentInput) (*agent.AgentPrompt, error) {
	return &agent.AgentPrompt{}, nil
}
func (a *credentialBoundaryAdapter) Execute(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
	a.calls++
	if a.calls == a.failAfter {
		a.coding.mu.Lock()
		a.coding.failed = true
		a.coding.mu.Unlock()
	}
	label := "a"
	if a.calls > 1 {
		label = "b"
	}
	executionError := "rate limit exceeded for synthetic account " + label
	return &agent.AgentResult{ExitCode: 1, Error: executionError + " retry-after=60", Summary: "synthetic-result-" + label, Diff: "synthetic-diff-" + label}, errors.New(executionError)
}

type credentialBoundaryOrchestrator struct {
	orchestratorService
	runs, recoveries int
	freshReplayErr   error
}

func (o *credentialBoundaryOrchestrator) RunAgent(ctx context.Context, session *models.Session) error {
	o.runs++
	if o.runs > 1 && o.freshReplayErr != nil {
		return o.freshReplayErr
	}
	return o.orchestratorService.RunAgent(ctx, session)
}
func (o *credentialBoundaryOrchestrator) RecoverSession(ctx context.Context, session *models.Session) error {
	o.recoveries++
	return o.orchestratorService.RecoverSession(ctx, session)
}

func credentialBoundaryRow(session models.Session) []any {
	row := workerSessionRow(session.ID, uuid.New(), session.OrgID, session.Status, session.CurrentTurn, nil, nil)
	for index, column := range workerSessionColumns {
		switch column {
		case "agent_type":
			row[index] = models.AgentTypeAmp
		case "origin":
			row[index] = models.SessionOriginManual
		case "primary_issue_id":
			row[index] = nil
		}
	}
	return row
}

func TestCredentialLookupAfterExecutionSettlesBeforeWorkerReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		failAfter int
		initial   bool
	}{
		{name: "continuation B credential lookup fails after A executes", failAfter: 1},
		{name: "continuation C availability probe fails after B executes", failAfter: 2},
		{name: "initial B credential lookup fails after A executes", failAfter: 1, initial: true},
		{name: "initial C availability probe fails after B executes", failAfter: 2, initial: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
			session := models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeAmp, Origin: models.SessionOriginManual, Status: models.SessionStatusIdle, CurrentTurn: 1, PrimaryThreadID: &threadID}
			if tt.initial {
				session.Status, session.CurrentTurn = models.SessionStatusPending, 0
			}
			sessions := &credentialBoundarySessions{session: session}
			messages := &credentialBoundaryMessages{messages: []models.SessionMessage{{ID: 7, OrgID: orgID, SessionID: sessionID, ThreadID: &threadID, TurnNumber: session.CurrentTurn + 1, Role: models.MessageRoleUser, Content: "Synthetic continuation task"}}}
			inbox := &credentialBoundaryInbox{applied: make(map[int64]bool)}
			runtimes := &credentialBoundaryRuntimes{}
			coding := &credentialBoundaryCoding{rows: []models.DecryptedCodingCredential{{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAmp, Status: models.CodingCredentialStatusActive, Config: models.AmpConfig{APIKey: "synthetic-a"}}, {ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAmp, Status: models.CodingCredentialStatusActive, Config: models.AmpConfig{APIKey: "synthetic-b"}}}}
			adapter := &credentialBoundaryAdapter{coding: coding, failAfter: tt.failAfter}
			realOrchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{Provider: credentialBoundaryProvider{}, Adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeAmp: adapter}, Sessions: sessions, SessionMessages: messages, CodingCredentials: coding, ThreadInbox: inbox, ThreadRuntimes: runtimes, Jobs: credentialBoundaryJobs{}, Logger: zerolog.Nop()})
			orchestrator := &credentialBoundaryOrchestrator{orchestratorService: realOrchestrator}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create worker fetch store")
			defer mock.Close()
			stores := &Stores{Sessions: db.NewSessionStore(mock)}
			if !tt.initial {
				stores.SessionThreads = db.NewSessionThreadStore(mock)
			}
			handler := newContinueSessionHandler(stores, &Services{Orchestrator: orchestrator}, zerolog.Nop())
			jobType := "continue_session"
			jobPayload := map[string]string{"session_id": sessionID.String(), "org_id": orgID.String(), "queued_message_id": "7", "thread_id": threadID.String()}
			if tt.initial {
				handler = newRunAgentHandler(stores, &Services{Orchestrator: orchestrator}, zerolog.Nop())
				jobType = "run_agent"
				delete(jobPayload, "queued_message_id")
			}
			payload, err := json.Marshal(jobPayload)
			require.NoError(t, err, "marshal concrete queued continuation job")
			mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(sessionID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(credentialBoundaryRow(session)...))
			if !tt.initial {
				mock.ExpectQuery("SELECT .* FROM session_threads").WithArgs(threadID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionThreadColumns).AddRow(workerSessionThreadRow(threadID, sessionID, orgID, models.AgentTypeAmp, nil, models.ThreadStatusRunning)...))
				mock.ExpectExec("UPDATE session_threads SET status").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			err = handler(context.Background(), jobType, payload)
			require.Error(t, err, "the executed provider's rate limit should remain the worker-visible turn outcome")
			expectedError := "rate limit exceeded for synthetic account a"
			if tt.failAfter > 1 {
				expectedError = "rate limit exceeded for synthetic account b"
			}
			require.Contains(t, err.Error(), expectedError, "the failed fallback lookup must preserve the last executed provider's error")
			require.NotContains(t, err.Error(), "synthetic credential lookup failure", "post-execution lookup failures must pass through normal provider settlement")
			require.Equal(t, []models.SessionStatus{models.SessionStatusFailed}, sessions.results, "the executed turn should have exactly one durable terminal session result before a worker retry")
			require.Equal(t, []models.ThreadRuntimeStatus{models.ThreadRuntimeStatusFailed}, runtimes.terminal, "the accepted input's runtime must settle exactly once")
			require.Equal(t, map[int64]bool{7: true}, inbox.applied, "real orchestration must acknowledge the queued seed before executing the provider")
			settled, err := sessions.GetByID(context.Background(), orgID, sessionID)
			require.NoError(t, err, "read the settled session used by the worker replay")
			require.Equal(t, models.SessionStatusFailed, settled.Status, "the accepted turn must not be left running after the lookup failure")
			expectedStoredError := expectedError
			if !tt.initial {
				expectedStoredError += " retry-after=60"
			}
			require.Equal(t, &expectedStoredError, settled.Error, "terminal persistence should retain the original last-execution error rather than the auxiliary lookup failure")
			if !tt.initial {
				mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(sessionID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(credentialBoundaryRow(settled)...))
				mock.ExpectQuery("SELECT .* FROM session_threads").WithArgs(threadID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionThreadColumns).AddRow(workerSessionThreadRow(threadID, sessionID, orgID, models.AgentTypeAmp, nil, models.ThreadStatusIdle)...))
				err = handler(context.Background(), jobType, payload)
				require.NoError(t, err, "worker replay may finish an already-applied input only after the original turn was durably settled")
				require.Equal(t, tt.failAfter, adapter.calls, "worker replay must not execute the CLI again for acknowledged input")
				require.Equal(t, []models.SessionStatus{models.SessionStatusFailed}, sessions.results, "worker replay must not duplicate terminal session results")
				require.Equal(t, []models.ThreadRuntimeStatus{models.ThreadRuntimeStatusFailed}, runtimes.terminal, "worker replay must not recreate an executing runtime")
			} else {
				require.Equal(t, 1, orchestrator.runs, "the initial job should enter RunAgent exactly once")
				require.Equal(t, tt.failAfter, adapter.calls, "fallback lookup failure must not start another CLI execution")
				// Ordinary provider errors already use bounded generic job retries.
				// Intercept that baseline fresh retry after testing the actual first
				// execution; the regression is a lookup error leaving status=running
				// and diverting the worker into uncheckpointed RecoverSession.
				orchestrator.freshReplayErr = errors.New("synthetic interception of baseline fresh retry")
				mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(sessionID, orgID).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(credentialBoundaryRow(settled)...))
				err = handler(context.Background(), jobType, payload)
				require.ErrorIs(t, err, orchestrator.freshReplayErr, "a settled ordinary provider failure should retain its existing fresh retry route")
				require.Equal(t, 2, orchestrator.runs, "initial worker replay should observe terminal state rather than recover a lost running turn")
				require.Equal(t, 0, orchestrator.recoveries, "post-execution lookup failure must not divert worker replay into uncheckpointed recovery")
			}

			require.NoError(t, mock.ExpectationsWereMet(), "worker retries should only reload the scoped session")
		})
	}
}
