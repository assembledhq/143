package worker

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestPromptedWithoutCreated covers the dispatcher race: Linear can
// deliver `prompted` before its companion `created` event lands. The
// dispatcher already 200'd Linear so Linear will not redeliver, which
// means only the worker job's retry path can keep the follow-up message
// alive. The handler's contract is therefore to return a RetryableError
// with a short fixed wait so the created handler has time to attach the
// session_id; returning nil would silently drop the user's comment.
func TestPromptedWithoutCreated(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()

	// Lookup returns a row with session_id = NULL (created hasn't
	// completed). The handler must short-circuit without invoking any
	// other store, but it must surface a retryable error.
	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_pending",
			"iss_1", "ACS-1",
			"app_1", "user_1",
			nil /* session_id NULL */, "pending", &now,
			now, now,
		))

	deps := LinearAgentEventHandlerDeps{
		// Stores left intentionally empty — the handler must NOT reach
		// SessionMessages.Create or Sessions.ClaimIdle when session_id
		// is nil. A Stores=nil deref would explode if the short-circuit
		// regressed; this is the cheapest way to assert the contract.
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	payload := linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_pending",
		LinearCommentID:      "comment_1",
	}

	err = handleLinearAgentPrompted(context.Background(), deps, store, payload, zerolog.Nop())
	var retryable *RetryableError
	require.ErrorAs(t, err, &retryable,
		"prompted-without-created must return a RetryableError so the worker re-runs the job; "+
			"Linear already received 200 for the webhook and will not redeliver")
	require.NotNil(t, retryable.RetryAfter,
		"the retry must use a fixed short wait, not fall through to exponential backoff that would delay the follow-up comment for minutes")
	require.NoError(t, mock.ExpectationsWereMet(),
		"only the Lookup query should fire — no claims, no message inserts, no continue_session enqueue")
}

// TestPromptedInvalidOrgID covers the malformed-payload path. The
// dispatcher pre-validates this, but defense in depth at the worker
// layer surfaces the failure as a clean error rather than a panic.
func TestPromptedInvalidOrgID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)

	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                "not-a-uuid",
		LinearAgentSessionID: "as_1",
	}, zerolog.Nop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid org_id")
	// No DB calls — the handler must reject malformed payloads before
	// reaching the store.
	require.NoError(t, mock.ExpectationsWereMet())
}
