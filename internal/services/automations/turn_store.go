package automations

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// TurnStore adapts the db stores to agent.AutomationTurnStore, the surface
// the orchestrator's per-target turn path needs (design doc 125). It lives
// here because the agent package does not import db.
type TurnStore struct {
	pool     db.TxStarter
	runs     *db.AutomationRunStore
	targets  *db.AutomationTargetStore
	results  *db.AutomationRunResultStore
	sessions *db.SessionStore
	messages *db.SessionMessageStore
}

// NewTurnStore wires the turn store over one pool.
func NewTurnStore(pool db.TxStarter, sessions *db.SessionStore, runs *db.AutomationRunStore, targets *db.AutomationTargetStore, results *db.AutomationRunResultStore, messages *db.SessionMessageStore) *TurnStore {
	return &TurnStore{pool: pool, runs: runs, targets: targets, results: results, sessions: sessions, messages: messages}
}

var _ agent.AutomationTurnStore = (*TurnStore)(nil)

func (s *TurnStore) LoadRun(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error) {
	return s.runs.GetByRunID(ctx, orgID, runID)
}

func (s *TurnStore) LoadTarget(ctx context.Context, orgID, targetID uuid.UUID) (models.AutomationTarget, error) {
	return s.targets.GetByID(ctx, orgID, targetID)
}

func (s *TurnStore) AttemptOwned(ctx context.Context, tx pgx.Tx, orgID, runID, lockToken uuid.UUID) (bool, error) {
	return s.runs.AttemptOwned(ctx, tx, orgID, runID, lockToken)
}

func (s *TurnStore) RecordContinuationFallback(ctx context.Context, orgID, runID, lockToken uuid.UUID, reason models.AutomationRunContinuationReason) (bool, error) {
	return s.runs.RecordContinuationFallback(ctx, orgID, runID, lockToken, reason)
}

func (s *TurnStore) LoadGeneration(ctx context.Context, orgID, targetID uuid.UUID, generation int) (models.AutomationTargetSession, error) {
	return s.targets.GetGenerationByNumber(ctx, orgID, targetID, generation)
}

func (s *TurnStore) ListCompletedTurnSummaries(ctx context.Context, orgID, targetID uuid.UUID, generation, limit int) ([]models.AutomationTurnSummary, error) {
	return s.runs.ListCompletedTurnSummaries(ctx, orgID, targetID, generation, limit)
}

func (s *TurnStore) RecordTurnWorkspace(ctx context.Context, orgID, runID, lockToken uuid.UUID, ws models.AutomationTurnWorkspace) (bool, error) {
	return s.runs.RecordTurnWorkspace(ctx, orgID, runID, lockToken, ws)
}

func (s *TurnStore) UpdateTurnPrompt(ctx context.Context, orgID, runID uuid.UUID, content string) (bool, error) {
	return s.messages.UpdateAutomationTurnPrompt(ctx, orgID, runID, content)
}

func (s *TurnStore) TagAssistantMessage(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runID uuid.UUID) error {
	return s.messages.TagAutomationTurnAssistantMessage(ctx, orgID, sessionID, threadID, turnNumber, runID)
}

func (s *TurnStore) PublishCheckpointWithProvenance(ctx context.Context, orgID, sessionID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, stopReason models.RuntimeStopReason, provenance models.CheckpointProvenance) (bool, error) {
	return s.sessions.PublishCheckpointWithProvenance(ctx, orgID, sessionID, lockToken, agentSessionID, snapshotKey, kind, capability, sizeBytes, checkpointedAt, stopReason, provenance)
}

// EndAttempt runs fn in one transaction with a transaction-bound session
// store, so the attempt's status write and its result marker commit
// together.
func (s *TurnStore) EndAttempt(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx, sessions agent.SessionStore) error) error {
	return s.sessions.InTransaction(ctx, func(ctx context.Context, tx pgx.Tx, store *db.SessionStore) error {
		return fn(ctx, tx, store)
	})
}

func (s *TurnStore) WriteResult(ctx context.Context, tx pgx.Tx, orgID, jobID uuid.UUID, result *models.AutomationRunResult) (bool, error) {
	return s.results.Write(ctx, tx, orgID, jobID, result)
}

func (s *TurnStore) RecordTurnDuration(ctx context.Context, tx pgx.Tx, orgID, runID, lockToken uuid.UUID, durationMS int) (bool, error) {
	return s.runs.RecordTurnDuration(ctx, tx, orgID, runID, lockToken, durationMS)
}

// CompletePreflight ends a reserved run that could not start, in its own
// transaction.
func (s *TurnStore) CompletePreflight(ctx context.Context, orgID, runID, lockToken uuid.UUID, outcome models.AutomationRunOutcomeReason, summary string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	done, err := s.runs.CompleteExecutingPreflight(ctx, tx, orgID, runID, lockToken, outcome, summary)
	if err != nil {
		return false, err
	}
	if !done {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RetireGeneration retires a generation in its own transaction.
func (s *TurnStore) RetireGeneration(ctx context.Context, orgID, generationID uuid.UUID, reason models.AutomationTargetRetiredReason) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := s.targets.RetireGeneration(ctx, tx, orgID, generationID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
