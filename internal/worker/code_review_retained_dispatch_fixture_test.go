package worker

import (
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/db"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Legacy caller fixtures still execute the actual supplied-Tx deciding reads.
// This is explicit test-only composition, not a no-op Run, a production nil
// fallback or proof of PostgreSQL commits. The global constructor-only fixture
// remains strict; each caller opts in and expects its exact SQL separately.
func wireCodeReviewControllerCPUFixture(t *testing.T, stores *Stores, mock pgxmock.PgxPoolIface) *retainedDispatchCPUTransactions {
	t.Helper()
	dependencies, err := threadsvc.NewSendDependencies(&pgxpool.Pool{}, db.NewSessionStore(mock), db.NewSessionThreadStore(mock), db.NewJobStore(mock))
	require.NoError(t, err, "legacy controller fixtures must have explicit mock-backed send persistence and effects dependencies")
	transactions := &retainedDispatchCPUTransactions{t: t, mock: mock}
	dependencies.Transactions = transactions
	stores.ThreadSendDependencies = dependencies
	return transactions
}

func legacyCodeReviewCPUJob(orgID, sessionID uuid.UUID) runCodeReviewPayload {
	return runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, MetadataID: uuid.New(), OutputKey: "synthetic-legacy-review:" + sessionID.String()}
}

const codeReviewCPUFixtureRetainedIDsSQL = `SELECT r.id FROM code_review_agent_results r JOIN code_review_session_metadata m ON m.org_id=r.org_id AND m.session_id=r.session_id
 WHERE r.org_id=$1 AND r.session_id=$2 AND m.id=$3 AND m.review_output_key=$4 AND r.role='reviewer' AND r.dispatch_version IS NOT NULL ORDER BY r.id LIMIT 101`

func expectCodeReviewNoRetainedReviewers(mock pgxmock.PgxPoolIface, job runCodeReviewPayload) {
	mock.ExpectQuery(regexp.QuoteMeta(codeReviewCPUFixtureRetainedIDsSQL)).
		WithArgs(job.OrgID, job.SessionID, job.MetadataID, job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"id"})).RowsWillBeClosed()
}

// The aware wrapper preserves the old legacy outcome writer only after its
// metadata-first/result-second locks and a real all-NULL retained-shape read.
// The caller retains its original final UPDATE and output assertions.
func expectCodeReviewLegacyReviewerOutcome(mock pgxmock.PgxPoolIface, job runCodeReviewPayload, resultID uuid.UUID) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR SHARE`)).
		WithArgs(job.OrgID, job.SessionID, job.MetadataID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(job.MetadataID))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 AND role='reviewer' FOR UPDATE`)).
		WithArgs(job.OrgID, job.SessionID, resultID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(resultID))
	mock.ExpectQuery(`(?s)^SELECT id,org_id,session_id,dispatch_version,.* FROM code_review_agent_results WHERE org_id=\$1 AND session_id=\$2 AND id=\$3$`).
		WithArgs(job.OrgID, job.SessionID, resultID, 8<<20).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org", "session", "version", "key", "digest", "thread", "raw", "raw_bytes", "nonnull", "state", "attempt", "seq", "writer", "ordinary_thread", "pass", "reviewer", "valid"}).
			AddRow(resultID, job.OrgID, job.SessionID, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil, nil, nil, nil, true, false))
}

func expectCodeReviewFreshLegacyReviewerOwner(mock pgxmock.PgxPoolIface, job runCodeReviewPayload, reviewerKey string) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3
 AND review_output_key=$4 AND status IN ('queued','running') AND NOT stale FOR UPDATE`)).
		WithArgs(job.OrgID, job.SessionID, job.MetadataID, job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(job.MetadataID))
	mock.ExpectQuery(`(?s)^SELECT id, org_id, session_id, agent_provider, agent_model, role, status,.* FROM code_review_agent_results\s+WHERE org_id=\$1 AND session_id=\$2 AND role='reviewer' AND structured_result->>'reviewer_key'=\$3 ORDER BY id LIMIT 2$`).
		WithArgs(job.OrgID, job.SessionID, reviewerKey).
		WillReturnRows(newCodeReviewAgentResultRows()).RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT schema_ready AND dispatch_status_ready AND intents_enabled FROM caller_send_intent_control WHERE singleton FOR SHARE`)).
		WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(false))
}
