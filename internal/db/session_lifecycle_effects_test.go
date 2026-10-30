package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPublishCommittedLifecycleThreadEffect(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name      string
		kind      LifecycleEffectKind
		outcome   string
		wantError bool
	}
	var tests []testCase
	for _, kind := range []LifecycleEffectKind{LifecycleEffectThreadChanged, LifecycleEffectInboxChanged} {
		for _, outcome := range []string{"success", "disabled", "nil store", "nil context", "cancelled", "zero effect", "foreign effect", "query failure", "missing lane", "wrong tenant", "wrong session", "wrong lane", "publish failure"} {
			tests = append(tests, testCase{name: string(kind) + "/" + outcome, kind: kind, outcome: outcome, wantError: outcome != "success" && outcome != "disabled"})
		}
	}
	for _, kind := range []LifecycleEffectKind{LifecycleEffectSessionChanged, LifecycleEffectWakeJobs} {
		tests = append(tests, testCase{name: "wrong publisher/" + string(kind), kind: kind, outcome: "wrong kind", wantError: true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this notification's isolated read-only mock connection")
			t.Cleanup(mock.Close)
			store, redis := threadPersistenceStoreWithStreams(t, mock)
			effect, err := newLifecycleEffect(tt.kind, f.orgID, f.sessionID, &f.threadID)
			require.NoError(t, err, "construct exact notification data without an admission or commit permit")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workCtx := ctx
			row, expectedThread := threadClaimReturnedFixture(f)
			failure := errors.New("synthetic notification failure")
			switch tt.outcome {
			case "disabled":
				store.SetStreams(nil)
			case "nil store":
				store = nil
			case "nil context":
				workCtx = nil
			case "cancelled":
				cancel()
			case "zero effect":
				effect = LifecycleEffect{}
			case "foreign effect":
				effect.orgID = uuid.New()
			case "wrong kind":
				// A valid session/job effect is deliberately not a lane event.
			default:
				query := mock.ExpectQuery("SELECT.*FROM session_threads.*WHERE id = .*AND org_id = .*AND archived_at IS NULL").WithArgs(f.threadID, f.orgID)
				switch tt.outcome {
				case "query failure":
					query.WillReturnError(failure)
				case "missing lane":
					query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
				default:
					switch tt.outcome {
					case "wrong tenant":
						row[2] = uuid.New()
					case "wrong session":
						row[1] = uuid.New()
					case "wrong lane":
						row[0] = uuid.New()
					case "publish failure":
						redis.SetError(failure.Error())
					}
					query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
				}
			}
			err = store.PublishCommittedLifecycleThreadEffect(workCtx, f.orgID, effect)
			if tt.wantError {
				require.Error(t, err, "a rejected or failed notification must reach the postcommit owner's warning path")
				if tt.outcome == "query failure" {
					require.ErrorIs(t, err, failure, "retain the underlying read error without changing committed work")
				}
			} else {
				require.NoError(t, err, "successful and intentionally disabled notifications must not produce a command failure")
			}
			if tt.outcome == "success" {
				entries, streamErr := redis.Stream("143:stream:{ses:" + f.sessionID.String() + "}:events")
				require.NoError(t, streamErr, "publish to only the expected synthetic session stream")
				var actual []models.SessionStreamEvent
				for _, entry := range entries {
					var event models.SessionStreamEvent
					require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode the entire emitted notification")
					actual = append(actual, event)
				}
				var expectedData any = models.NewThreadRuntimeEvent(expectedThread)
				expectedType := models.SessionStreamEventThreadRuntimeUpdated
				if tt.kind == LifecycleEffectInboxChanged {
					expectedData = models.NewThreadInboxEvent(expectedThread)
					expectedType = models.SessionStreamEventThreadInboxQueued
				}
				encoded, encodeErr := json.Marshal(expectedData)
				require.NoError(t, encodeErr, "encode the complete expected current-row notification data")
				var normalized any
				require.NoError(t, json.Unmarshal(encoded, &normalized), "normalize only the interface-encoded event data")
				require.Equal(t, []models.SessionStreamEvent{{Type: expectedType, OrgID: f.orgID, SessionID: f.sessionID, Data: normalized}}, actual,
					"postcommit notifications describe the exact visible tenant/session/lane reload, not a fabricated claim snapshot")
			} else {
				require.Empty(t, redis.Keys(), "disabled, invalid or failed notifications must emit no event or cross-scope stream")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "a notification may only reload the explicit visible tenant lane, never write or settle a transaction")
		})
	}
}
