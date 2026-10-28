package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This wrapper suppresses only the application acknowledgement AFTER the real
// autocommitted INSERT has completed. It does not simulate an aborted insert.
type retainedCreateLostAck struct{ DBTX }
type retainedCreateLostAckRow struct{ pgx.Row }

var errRetainedCreateLostAck = errors.New("synthetic retained creation acknowledgement lost")

func (s retainedCreateLostAck) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return retainedCreateLostAckRow{s.DBTX.QueryRow(ctx, query, args...)}
}
func (r retainedCreateLostAckRow) Scan(dest ...any) error {
	if err := r.Row.Scan(dest...); err != nil {
		return err
	}
	return errRetainedCreateLostAck
}

type retainedCreateBarrier struct {
	DBTX
	ready *sync.WaitGroup
	first bool
}

func (s *retainedCreateBarrier) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := s.DBTX.Query(ctx, query, args...)
	if !s.first && strings.Contains(query, "FROM session_threads WHERE org_id=$1 AND id=$2") {
		s.first = true
		s.ready.Done()
		s.ready.Wait()
	}
	return rows, err
}

func retainedCreationEffects(t *testing.T, ctx context.Context, q DBTX, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var result json.RawMessage
	err := q.QueryRow(ctx, `SELECT jsonb_build_object(
 'messages',(SELECT count(*) FROM session_messages WHERE org_id=$1),
 'jobs',(SELECT count(*) FROM jobs WHERE org_id=$1),
 'inbox',(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1),
 'stamps',(SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1))`, orgID).Scan(&result)
	require.NoError(t, err, "read actual effect counts independently of the thread row")
	return result
}

func TestRetainedThreadCreationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{"fresh exact identity and full capacity adoption"}, {"lost commit acknowledgement"}, {"concurrent same identity"},
		{"different tenant"}, {"different session"}, {"different agent"}, {"different model"}, {"different label"}, {"archived original"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			f := seedThreadPersistencePostgres(t, ctx, pool)
			store := NewSessionThreadStore(pool)
			original := models.SessionThread{ID: uuid.New(), OrgID: f.orgID, SessionID: f.sessionID,
				AgentType: models.AgentTypeClaudeCode, Label: "Synthetic retained review", Status: models.ThreadStatusIdle,
				CreatedBySource: models.ThreadCreatedBySourceUser, CreatedByThreadID: &f.threadID}
			before := retainedCreationEffects(t, ctx, pool, f.orgID)
			candidate := original
			switch tt.name {
			case "lost commit acknowledgement":
				err := NewSessionThreadStore(retainedCreateLostAck{pool}).CreateWithID(ctx, f.orgID, &candidate, 2)
				require.ErrorIs(t, err, errRetainedCreateLostAck, "the application must receive uncertainty after a genuinely committed row")
				require.Equal(t, original, candidate, "an unacknowledged insert must not expose a provisional successful result")
			case "concurrent same identity":
				var ready sync.WaitGroup
				ready.Add(2)
				type outcome struct {
					row models.SessionThread
					err error
				}
				results := make(chan outcome, 2)
				for range 2 {
					go func() {
						row := original
						err := NewSessionThreadStore(&retainedCreateBarrier{DBTX: pool, ready: &ready}).CreateWithID(ctx, f.orgID, &row, 2)
						results <- outcome{row, err}
					}()
				}
				for range 2 {
					result := <-results
					require.NoError(t, result.err, "two actual absent-row snapshots must converge on the same preminted identity")
					require.Equal(t, original.ID, result.row.ID, "a concurrent creator must never allocate a replacement ID")
				}
			default:
				require.NoError(t, store.CreateWithID(ctx, f.orgID, &candidate, 2), "fresh insertion must use exactly the retained thread ID")
				require.Equal(t, original.ID, candidate.ID, "the database must not substitute its ordinary UUID default")
			}
			stored, err := store.GetByIDForCreation(ctx, f.orgID, original.ID)
			require.NoError(t, err, "the exact original must exist after insertion, contention, or lost acknowledgement")
			require.Equal(t, original.CreatedByThreadID, stored.CreatedByThreadID, "fresh retained creation must preserve provenance")
			_, err = pool.Exec(ctx, `UPDATE session_threads SET status='completed',current_turn=7,result_summary='Synthetic completed review' WHERE org_id=$1 AND id=$2`, f.orgID, original.ID)
			require.NoError(t, err, "later execution progress must survive same-ID recovery")
			expected, err := store.GetByIDForCreation(ctx, f.orgID, original.ID)
			require.NoError(t, err, "capture the complete original row including progress and timestamp")
			candidate = original
			conflict := true
			switch tt.name {
			case "different tenant":
				other := seedThreadPersistencePostgres(t, ctx, pool)
				candidate.OrgID, candidate.SessionID, candidate.CreatedByThreadID = other.orgID, other.sessionID, &other.threadID
			case "different session":
				candidate.SessionID = uuid.New()
			case "different agent":
				candidate.AgentType = models.AgentTypeCodex
			case "different model":
				model := ""
				candidate.ModelOverride = &model
			case "different label":
				candidate.Label = "Different retained label"
			case "archived original":
				_, err = pool.Exec(ctx, `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, f.orgID, original.ID)
				require.NoError(t, err, "archive the actual original without removing its primary-key identity")
				expected, err = store.GetByIDForCreation(ctx, f.orgID, original.ID)
				require.NoError(t, err, "the creation lookup must include archived original identities")
			default:
				conflict = false
			}
			input := candidate
			err = store.CreateWithID(ctx, candidate.OrgID, &candidate, 2)
			if conflict {
				require.ErrorIs(t, err, ErrThreadCreationIdentityConflict, "mismatched or archived originals must refuse without leaking tenant contents")
				require.Equal(t, input, candidate, "a refused identity cannot expose another row or mutate the input")
			} else {
				require.NoError(t, err, "the committed original must be adopted before the now-full capacity predicate")
				require.Equal(t, expected, candidate, "adoption must preserve the entire progressed original row")
			}
			after, err := store.GetByIDForCreation(ctx, f.orgID, original.ID)
			require.NoError(t, err, "the retained original must remain readable after every outcome")
			require.Equal(t, expected, after, "recovery and identity refusal must not reset or replace the original")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sessionID).Scan(&count), "count actual rows after all competing creates and recovery calls")
			require.Equal(t, 2, count, "only the primary thread and one retained startup thread may exist")
			require.JSONEq(t, string(before), string(retainedCreationEffects(t, ctx, pool, f.orgID)), "row-only creation must not add a message, inbox, stamp or execution job")
		})
	}
}
