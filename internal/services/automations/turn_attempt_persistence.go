package automations

import (
	"context"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// turnAttemptPersistenceEffect is owner-local notification data, not a commit
// receipt, retry/outbox mechanism or lifecycle authority. EndAttempt discards
// the entire collection on any callback or commit error, including uncertainty.
type turnAttemptPersistenceEffect struct {
	orgID     uuid.UUID
	status    *models.Session
	workspace db.SessionPersistenceEffects
}

// turnAttemptSessions keeps the existing agent callback interface local to its
// actual transaction owner. Only the three reviewed status/result/turn methods
// collect effects. The remaining inherited store has no streams, so it cannot
// publish from inside the transaction; a source-shape test pins the actual
// callback method set so newly added publishing calls require explicit review.
type turnAttemptSessions struct {
	*db.SessionStore
	tx      pgx.Tx
	effects []turnAttemptPersistenceEffect
}

var _ agent.SessionStore = (*turnAttemptSessions)(nil)

func newTurnAttemptSessions(tx pgx.Tx, bound *db.SessionStore) *turnAttemptSessions {
	// InTransaction supplies a new WithTx copy. Clearing only that copy's
	// streams cannot mutate the original usable post-commit publisher.
	bound.SetStreams(nil)
	return &turnAttemptSessions{SessionStore: bound, tx: tx}
}

func (s *turnAttemptSessions) UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error {
	session, err := s.SessionStore.UpdateStatusInTx(ctx, orgID, s.tx, sessionID, status)
	if err != nil {
		return err
	}
	s.effects = append(s.effects, turnAttemptPersistenceEffect{orgID: orgID, status: &session})
	return nil
}

func (s *turnAttemptSessions) UpdateResult(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	session, workspace, err := s.SessionStore.UpdateResultInTx(ctx, orgID, s.tx, sessionID, status, result)
	if err != nil {
		return err
	}
	s.effects = append(s.effects, turnAttemptPersistenceEffect{orgID: orgID, status: &session, workspace: workspace})
	return nil
}

func (s *turnAttemptSessions) UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error {
	workspace, err := s.SessionStore.UpdateTurnCompleteInTx(ctx, orgID, s.tx, sessionID, turn, result, agentSessionID, snapshotKey)
	if err != nil {
		return err
	}
	s.effects = append(s.effects, turnAttemptPersistenceEffect{orgID: orgID, workspace: workspace})
	return nil
}
