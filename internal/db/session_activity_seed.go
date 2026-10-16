package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// StartSeedPhase atomically applies durable provider seeds and starts their
// provider phase. Failed admission leaves seeds and watermarks replayable;
// already applied durable seeds are rejected on retry.
func (s *SessionActivityPhaseStore) StartSeedPhase(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runtimeID, leaseToken uuid.UUID, messageIDs []int64, startedAt time.Time) (models.SessionActivityPhase, *models.ThreadInboxDeliveryBatch, error) {
	startedAt = postgresTimestamp(startedAt)
	if turnNumber < 0 || startedAt.IsZero() || len(messageIDs) == 0 {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("start seed phase: invalid turn, time, or seed")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("begin seed activity phase: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Match phase-start and inbox-append lock order before locking the lease.
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3
		FOR UPDATE`, orgID, sessionID, threadID).Scan(&locked); err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("lock seed activity thread: %w", err)
	}
	var previous int64
	if err := tx.QueryRow(ctx, `
		SELECT last_acked_sequence FROM thread_runtimes
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
		  AND lease_token = $5 AND status IN ('starting', 'live', 'paused')
		  AND lease_expires_at > GREATEST($6, clock_timestamp())
		FOR UPDATE`, orgID, sessionID, threadID, runtimeID, leaseToken, startedAt).Scan(&previous); err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("lock seed runtime lease: %w", err)
	}
	// Legacy messages may predate the durable inbox. Materialize their exact
	// scoped identity under the same thread lock instead of bypassing seed
	// acknowledgment. Threadless legacy input belongs only to the primary tab.
	_, err = tx.Exec(ctx, `
		WITH missing AS (
			SELECT m.id, m.content, row_number() OVER (ORDER BY m.id) AS offset_no
			FROM session_messages m
			WHERE m.org_id = $1 AND m.session_id = $2 AND m.role = 'user'
			  AND m.id = ANY($4)
			  AND (m.thread_id = $3 OR (m.thread_id IS NULL AND $3 = (
				SELECT t.id FROM session_threads t
				WHERE t.org_id = $1 AND t.session_id = $2 AND t.archived_at IS NULL
				ORDER BY t.created_at, t.id LIMIT 1
			  )))
			  AND NOT EXISTS (
				SELECT 1 FROM thread_inbox_entries e WHERE e.org_id = $1 AND e.message_id = m.id
			  )
		), watermark AS (
			SELECT COALESCE(max(sequence_no), 0) AS sequence_no
			FROM thread_inbox_entries WHERE org_id = $1 AND session_id = $2 AND thread_id = $3
		)
		INSERT INTO thread_inbox_entries (
			org_id, session_id, thread_id, sequence_no, message_id, client_message_id, entry_type, payload
		)
		SELECT $1, $2, $3, w.sequence_no + m.offset_no, m.id, 'message:' || m.id,
			'user_message', jsonb_build_object('content', m.content)
		FROM missing m CROSS JOIN watermark w`, orgID, sessionID, threadID, messageIDs)
	if err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("backfill legacy seed inbox: %w", err)
	}
	// Reconciliation locks a batch before its entries; follow that order to
	// avoid racing its abandonment while recovering an old pre-provider seed.
	lockedBatches, err := tx.Query(ctx, `
		SELECT b.id FROM thread_inbox_delivery_batches b
		WHERE b.org_id = $1 AND b.session_id = $2 AND b.thread_id = $3
		  AND EXISTS (
			SELECT 1 FROM thread_inbox_entries e
			WHERE e.org_id = $1 AND e.session_id = $2 AND e.thread_id = $3
			  AND e.message_id = ANY($4) AND e.runtime_id = b.runtime_id
			  AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
		  )
		ORDER BY b.sequence_start, b.id FOR UPDATE OF b`, orgID, sessionID, threadID, messageIDs)
	if err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("lock historical seed batches: %w", err)
	}
	if _, err := pgx.CollectRows(lockedBatches, pgx.RowTo[uuid.UUID]); err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("collect locked seed batches: %w", err)
	}
	// Historical pre-provider failures could leave acknowledged, unapplied
	// batches. A terminal owner with no provider handle or activity phase proves
	// the unstarted batch never crossed the provider-launch boundary. Live
	// steering acknowledgments do not qualify for this automatic recovery.
	_, err = tx.Exec(ctx, `
		UPDATE thread_inbox_entries e
		SET delivery_state = 'delivering', runtime_id = $4,
			owner_node_id = r.owner_node_id, updated_at = now()
		FROM thread_runtimes r
		WHERE e.org_id = $1 AND e.session_id = $2 AND e.thread_id = $3
		  AND e.message_id = ANY($5) AND e.applied_at IS NULL
		  AND r.org_id = $1 AND r.session_id = $2 AND r.thread_id = $3 AND r.id = $4
		  AND (
			(e.delivery_state = 'pending' AND e.runtime_id IS NULL)
			OR (e.delivery_state = 'delivering' AND (e.runtime_id = $4 OR EXISTS (
				SELECT 1 FROM thread_runtimes old
				WHERE old.org_id = $1 AND old.session_id = $2 AND old.thread_id = $3
				  AND old.id = e.runtime_id AND old.status IN ('closed', 'failed', 'lost') AND old.runtime_handle_id = ''
				  AND NOT EXISTS (SELECT 1 FROM session_activity_phases p WHERE p.org_id = $1 AND p.runtime_id = old.id)
			)))
			OR (e.delivery_state = 'acked' AND EXISTS (
				SELECT 1 FROM thread_inbox_delivery_batches b
				JOIN thread_runtimes old ON old.org_id = b.org_id AND old.session_id = b.session_id
				  AND old.thread_id = b.thread_id AND old.id = b.runtime_id
				WHERE b.org_id = $1 AND b.session_id = $2 AND b.thread_id = $3
				  AND b.runtime_id = e.runtime_id
				  AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
				  AND b.status = 'acknowledged' AND b.started_at IS NULL
				  AND old.status IN ('closed', 'failed', 'lost') AND old.runtime_handle_id = ''
				  AND (SELECT count(*) FROM thread_inbox_entries prior
					WHERE prior.org_id = $1 AND prior.session_id = $2 AND prior.thread_id = $3
					  AND prior.runtime_id = old.id AND prior.message_id = ANY($5)
					  AND prior.sequence_no BETWEEN b.sequence_start AND b.sequence_end
					  AND prior.delivery_state = 'acked' AND prior.applied_at IS NULL
				  ) = b.sequence_end - b.sequence_start + 1
				  AND NOT EXISTS (
					SELECT 1 FROM session_activity_phases p
					WHERE p.org_id = $1 AND (p.runtime_id = old.id OR p.trigger_batch_id = b.id)
				  )
			))
		  )`, orgID, sessionID, threadID, runtimeID, messageIDs)
	if err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("claim resumed seed entries: %w", err)
	}
	var total, owned, first, last int64
	err = tx.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE runtime_id = $4 AND delivery_state = 'delivering' AND applied_at IS NULL),
			COALESCE(min(sequence_no), 0), COALESCE(max(sequence_no), 0)
		FROM thread_inbox_entries
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND message_id = ANY($5)`, orgID, sessionID, threadID, runtimeID, messageIDs).Scan(&total, &owned, &first, &last)
	if err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("validate resumed seed entries: %w", err)
	}
	uniqueIDs := make(map[int64]struct{}, len(messageIDs))
	for _, id := range messageIDs {
		uniqueIDs[id] = struct{}{}
	}
	if total != int64(len(uniqueIDs)) {
		return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
	}
	trigger := models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}
	var batch *models.ThreadInboxDeliveryBatch
	if total > 0 {
		if owned != total || first != previous+1 || last-first+1 != total {
			return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
		}
		rows, queryErr := tx.Query(ctx, `
			INSERT INTO thread_inbox_delivery_batches AS b (
				org_id, session_id, thread_id, runtime_id, sequence_start, sequence_end,
				status, acknowledged_at
			) VALUES ($1, $2, $3, $4, $5, $6, 'acknowledged', $7)
			ON CONFLICT (org_id, thread_id, sequence_start, sequence_end) DO UPDATE
			SET runtime_id = EXCLUDED.runtime_id, acknowledged_at = EXCLUDED.acknowledged_at, updated_at = now()
			WHERE b.session_id = $2 AND b.status = 'acknowledged' AND b.started_at IS NULL
			  AND EXISTS (
				SELECT 1 FROM thread_runtimes old
				WHERE old.org_id = $1 AND old.session_id = $2 AND old.thread_id = $3
				  AND old.id = b.runtime_id AND old.status IN ('closed', 'failed', 'lost')
				  AND old.runtime_handle_id = ''
				  AND NOT EXISTS (
					SELECT 1 FROM session_activity_phases p
					WHERE p.org_id = $1 AND p.runtime_id = old.id
				  )
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM session_activity_phases p WHERE p.org_id = $1 AND p.trigger_batch_id = b.id
			  )
			RETURNING `+inboxBatchColumns, orgID, sessionID, threadID, runtimeID, first, last, startedAt)
		if queryErr != nil {
			return models.SessionActivityPhase{}, nil, fmt.Errorf("allocate resumed seed batch: %w", queryErr)
		}
		value, queryErr := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
		if queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
			}
			return models.SessionActivityPhase{}, nil, fmt.Errorf("collect resumed seed batch: %w", queryErr)
		}
		batch = &value
		tag, updateErr := tx.Exec(ctx, `
			UPDATE thread_inbox_entries
			SET delivery_state = 'acked', delivered_at = COALESCE(delivered_at, $7),
				acked_at = COALESCE(acked_at, $7), updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND runtime_id = $4
			  AND sequence_no BETWEEN $5 AND $6 AND delivery_state = 'delivering' AND applied_at IS NULL`, orgID, sessionID, threadID, runtimeID, first, last, startedAt)
		if updateErr != nil {
			return models.SessionActivityPhase{}, nil, fmt.Errorf("acknowledge resumed seed entries: %w", updateErr)
		}
		if tag.RowsAffected() != total {
			return models.SessionActivityPhase{}, nil, ErrInboxBatchConflict
		}
		trigger = models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInboxBatch, BatchID: &batch.ID}
	}
	phase, err := startActivityPhaseTx(ctx, tx, orgID, sessionID, threadID, turnNumber, &runtimeID, &leaseToken, trigger, startedAt)
	if err != nil {
		return models.SessionActivityPhase{}, nil, err
	}
	if batch != nil {
		_, err = tx.Exec(ctx, `
			UPDATE thread_runtimes
			SET last_acked_sequence = $5, last_delivered_sequence = GREATEST(last_delivered_sequence, $5), updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`, orgID, sessionID, threadID, runtimeID, last)
		if err != nil {
			return models.SessionActivityPhase{}, nil, fmt.Errorf("advance resumed seed watermark: %w", err)
		}
		batch.Status = models.InboxDeliveryBatchStarted
		batch.StartedAt = &startedAt
	}
	if err := tx.Commit(ctx); err != nil {
		return models.SessionActivityPhase{}, nil, fmt.Errorf("commit resumed seed activity: %w", err)
	}
	return phase, batch, nil
}
