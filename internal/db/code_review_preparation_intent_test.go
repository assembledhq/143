package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func preparationIntentTestValue() models.CodeReviewPreparationIntent {
	threadID := uuid.New()
	return models.CodeReviewPreparationIntent{
		Version: 1, OrgID: uuid.New(), JobID: uuid.New(), MetadataID: uuid.New(),
		SessionID: uuid.New(), PullRequestID: uuid.New(), HeadSHA: "head123", WorkspaceGeneration: 3,
		ThreadID: &threadID, Turn: 5, TurnScope: "thread", Recheck: false,
		FailureClass: models.CodeReviewPreparationFailureSetup, Stage: "dependency_setup",
	}
}

func TestCodeReviewPreparationIntentTerminalLease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rows    int64
		failure error
	}{
		{name: "owned lease commits one terminal checkpoint", rows: 1},
		{name: "rotated lease writes neither terminal status nor intent"},
		{name: "database failure remains retryable only before terminal checkpoint", failure: errors.New("terminal update unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated terminal checkpoint database mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			token := uuid.New()
			raw, err := json.Marshal(i)
			require.NoError(t, err, "encode the exact bounded preparation identity")
			expect := mock.ExpectExec(`(?s)UPDATE jobs SET status='dead_letter'.*payload=payload \|\| jsonb_build_object\('terminal_preparation_intent',\$4::jsonb\).*lock_token=NULL.*WHERE org_id=\$1 AND id=\$2 AND status='running' AND lock_token=\$3 AND job_type='continue_session'.*payload->>'session_id'=\$5.*COALESCE\(payload->>'thread_id',''\)=\$6.*COALESCE\(payload->>'code_review_assessment_id',''\)=''.*NOT \(payload \? 'terminal_preparation_intent'\)`).
				WithArgs(i.OrgID, i.JobID, token, raw, i.SessionID.String(), i.ThreadID.String())
			if tt.failure != nil {
				expect.WillReturnError(tt.failure)
			} else {
				expect.WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			}
			owned, err := NewJobStore(mock).DeadLetterContinueSessionWithPreparationIntentWithLease(context.Background(), i.OrgID, i.JobID, token, i)
			if tt.failure != nil {
				require.ErrorIs(t, err, tt.failure, "failed terminal writes must preserve their storage cause")
			} else {
				require.NoError(t, err, "lease ownership must be expressed by affected rows")
			}
			require.Equal(t, tt.rows == 1 && tt.failure == nil, owned, "only the exact owned continuation may gain a terminal checkpoint")
			require.NoError(t, mock.ExpectationsWereMet(), "terminal intent and lease retirement must share one tenant-scoped update")
		})
	}
}

func TestCodeReviewPreparationIntentInvalidTerminalAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		mutate                 func(*models.CodeReviewPreparationIntent)
		foreignOrg, foreignJob bool
	}{
		{name: "missing metadata", mutate: func(i *models.CodeReviewPreparationIntent) { i.MetadataID = uuid.Nil }},
		{name: "invalid stage", mutate: func(i *models.CodeReviewPreparationIntent) { i.Stage = "raw stderr" }},
		{name: "recheck retains assessment authority", mutate: func(i *models.CodeReviewPreparationIntent) { i.Recheck = true }},
		{name: "foreign tenant cannot persist intent", foreignOrg: true},
		{name: "different containing job cannot persist intent", foreignJob: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated invalid authority database mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			if tt.mutate != nil {
				tt.mutate(&i)
			}
			orgID, jobID := i.OrgID, i.JobID
			if tt.foreignOrg {
				orgID = uuid.New()
			}
			if tt.foreignJob {
				jobID = uuid.New()
			}
			owned, err := NewJobStore(mock).DeadLetterContinueSessionWithPreparationIntentWithLease(context.Background(), orgID, jobID, uuid.New(), i)
			require.Error(t, err, "invalid provenance must fail before any database mutation")
			require.False(t, owned, "invalid intent must never claim durable terminal ownership")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid provenance must not issue SQL")
		})
	}
}

func TestCodeReviewPreparationIntentPendingRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create an isolated pending-intent database mock")
	defer mock.Close()
	i := preparationIntentTestValue()
	raw, err := json.Marshal(i)
	require.NoError(t, err, "encode valid preparation provenance")
	inferred := i
	inferred.JobID, inferred.ThreadID, inferred.TurnScope = uuid.New(), nil, "session"
	inferredRaw, err := json.Marshal(inferred)
	require.NoError(t, err, "encode explicit null inferred-thread provenance")
	recheck := i
	recheck.JobID, recheck.Recheck = uuid.New(), true
	recheckRaw, err := json.Marshal(recheck)
	require.NoError(t, err, "encode an ineligible recheck record")
	foreign := i
	foreign.JobID, foreign.OrgID = uuid.New(), uuid.New()
	foreignRaw, err := json.Marshal(foreign)
	require.NoError(t, err, "encode a record whose containing tenant does not match")
	var missing map[string]any
	require.NoError(t, json.Unmarshal(raw, &missing), "decode provenance for a missing-field corruption fixture")
	delete(missing, "workspace_generation")
	missingRaw, err := json.Marshal(missing)
	require.NoError(t, err, "encode corrupted provenance without an observed generation")
	type record struct {
		id    uuid.UUID
		raw   []byte
		valid bool
	}
	records := []record{{i.JobID, raw, true}, {uuid.New(), []byte(`{"version":1}`), false}, {uuid.New(), missingRaw, false}, {foreign.JobID, foreignRaw, false}, {uuid.New(), raw, false}, {recheck.JobID, recheckRaw, false}, {inferred.JobID, inferredRaw, true}}
	rows := pgxmock.NewRows([]string{"id", "intent", "oversized"})
	for _, r := range records {
		rows.AddRow(r.id, r.raw, false)
	}
	mock.ExpectQuery(`(?s)SELECT id, CASE WHEN octet_length.*<=2048.*ELSE NULL END.*AS oversized FROM jobs.*org_id=\$1.*job_type='continue_session' AND status='dead_letter'.*payload->>'session_id'=\$2.*metadata_id'=\$3.*NOT \(payload \? 'terminal_preparation_intent_consumed'\).*ORDER BY updated_at DESC,id LIMIT 32`).
		WithArgs(i.OrgID, i.SessionID.String(), i.MetadataID.String()).WillReturnRows(rows).RowsWillBeClosed()
	for _, r := range records {
		if r.valid {
			continue
		}
		mock.ExpectExec(`(?s)UPDATE jobs SET payload=payload \|\| jsonb_build_object\('terminal_preparation_intent_consumed',true\).*WHERE org_id=\$1 AND id=\$2 AND job_type='continue_session' AND status='dead_letter'.*session_id'=\$3.*metadata_id'=\$4.*terminal_preparation_intent'=\$5::jsonb.*NOT\(payload \? 'terminal_preparation_intent_consumed'\)`).
			WithArgs(i.OrgID, r.id, i.SessionID.String(), i.MetadataID.String(), r.raw).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	actual, err := NewJobStore(mock).PendingCodeReviewPreparationIntents(context.Background(), i.OrgID, i.MetadataID, i.SessionID)
	require.NoError(t, err, "malformed and legacy records must be consumed without text-based review authority")
	require.Equal(t, CodeReviewPreparationIntentBatch{Intents: []models.CodeReviewPreparationIntent{i, inferred}}, actual, "only exact containing identities and explicit null inference may authorize projection")
	require.NoError(t, mock.ExpectationsWereMet(), "pending recovery must close rows before bounded tenant-scoped raw-value CAS discards")
}

func TestCodeReviewPreparationIntentCaptureOriginalGeneration(t *testing.T) {
	t.Parallel()
	for _, scoped := range []bool{false, true} {
		name := "inferred session scope"
		if scoped {
			name = "explicit thread scope"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated admission database mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			session := models.Session{ID: i.SessionID, OrgID: i.OrgID, WorkspaceGeneration: i.WorkspaceGeneration, CurrentTurn: 2}
			var thread *models.SessionThread
			if scoped {
				thread = &models.SessionThread{ID: *i.ThreadID, SessionID: i.SessionID, OrgID: i.OrgID, CurrentTurn: i.Turn - 1}
			} else {
				i.ThreadID, i.TurnScope, i.Turn = nil, "session", session.CurrentTurn+1
			}
			i.Stage = "readiness"
			token := uuid.New()
			mock.ExpectQuery(`SELECT pull_request_id FROM code_review_session_metadata WHERE org_id=\$1 AND session_id=\$2`).WithArgs(i.OrgID, i.SessionID).WillReturnRows(pgxmock.NewRows([]string{"pull_request_id"}).AddRow(i.PullRequestID))
			expectPublicationTransaction(mock)
			mock.ExpectQuery(`(?s)WITH locked_pr AS MATERIALIZED \(\s*SELECT org_id,id,head_sha FROM pull_requests WHERE org_id=@org_id AND id=@pr_id FOR NO KEY UPDATE\s*\).*SELECT m.id,m.pull_request_id,m.head_sha.*JOIN locked_pr p.*s.workspace_generation=@generation.*j.lock_token=@token.*j.lease_expires_at>clock_timestamp\(\).*code_review_assessment_id.*FOR NO KEY UPDATE OF s,m,j\s*$`).
				WithArgs(pgx.NamedArgs{"org_id": i.OrgID, "job_id": i.JobID, "session_id": i.SessionID, "pr_id": i.PullRequestID, "generation": i.WorkspaceGeneration, "token": token, "thread_id": i.ThreadID, "before_turn": i.Turn - 1}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "head_sha"}).AddRow(i.MetadataID, i.PullRequestID, i.HeadSHA))
			mock.ExpectCommit()
			actual, err := NewCodeReviewStore(mock).CaptureCodeReviewPreparationIntent(context.Background(), i.OrgID, i.JobID, token, session, thread)
			require.NoError(t, err, "admission must capture the same generation passed into preparation")
			require.Equal(t, i, actual, "inference must preserve null thread and original session turn rather than choosing a sibling")
			require.NoError(t, mock.ExpectationsWereMet(), "capture must hold publication/session/metadata/job fences before setup")
		})
	}
}

func TestCodeReviewPreparationIntentProjectionStorageFailureRollsBack(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, stage string }{
		{name: "cannot read terminal checkpoint", stage: "read"},
		{name: "cannot consume superseded checkpoint", stage: "consume"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated projection database mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			raw, err := json.Marshal(i)
			require.NoError(t, err, "encode the exact terminal checkpoint")
			failure := errors.New("projection storage unavailable")
			expectPublicationTransaction(mock)
			mock.ExpectQuery(`SELECT COALESCE\(head_sha,''\)=\$3 FROM pull_requests WHERE org_id=\$1 AND id=\$2 FOR NO KEY UPDATE`).WithArgs(i.OrgID, i.PullRequestID, i.HeadSHA).WillReturnRows(pgxmock.NewRows([]string{"head_matches"}).AddRow(true))
			read := mock.ExpectQuery(`(?s)SELECT payload->'terminal_preparation_intent' FROM jobs WHERE org_id=\$1 AND id=\$2 AND status='dead_letter'.*FOR UPDATE`).WithArgs(i.OrgID, i.JobID, i.SessionID.String())
			if tt.stage == "read" {
				read.WillReturnError(failure)
			} else {
				read.WillReturnRows(pgxmock.NewRows([]string{"intent"}).AddRow(raw))
				mock.ExpectQuery(`(?s)SELECT s.workspace_generation,s.current_turn.*s.org_id=\$1 AND s.id=\$2 AND m.id=\$3 FOR UPDATE OF s,m`).WithArgs(i.OrgID, i.SessionID, i.MetadataID).
					WillReturnRows(pgxmock.NewRows([]string{"generation", "turn", "cancelled"}).AddRow(i.WorkspaceGeneration+1, 2, false))
				mock.ExpectQuery(`(?s)SELECT current_turn,cancel_requested_at.*FROM session_threads WHERE org_id=\$1 AND session_id=\$2 AND id=\$3 FOR UPDATE`).WithArgs(i.OrgID, i.SessionID, *i.ThreadID).
					WillReturnRows(pgxmock.NewRows([]string{"turn", "stopped"}).AddRow(i.Turn-1, false))
				mock.ExpectExec(`(?s)UPDATE jobs SET payload=payload \|\| jsonb_build_object\('terminal_preparation_intent_consumed',true\).*WHERE org_id=\$1 AND id=\$2 AND status='dead_letter'`).WithArgs(i.OrgID, i.JobID).WillReturnError(failure)
			}
			mock.ExpectRollback()
			actual, err := NewCodeReviewStore(mock).ProjectCodeReviewPreparationIntent(context.Background(), i.OrgID, i, "bounded operational failure")
			require.ErrorIs(t, err, failure, "storage failure must leave the checkpoint available for controller-only retry")
			require.Equal(t, CodeReviewPreparationProjection{}, actual, "an uncommitted transaction cannot report successful durable projection")
			require.NoError(t, mock.ExpectationsWereMet(), "storage errors must roll back rather than half-consume an intent")
		})
	}
}

func TestCodeReviewPreparationIntentOversizedDiscardCAS(t *testing.T) {
	t.Parallel()
	for _, corrected := range []bool{false, true} {
		name := "oversized record is discarded without materialization"
		if corrected {
			name = "concurrent corrected replacement retains authority"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated oversized record database mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			mock.ExpectQuery(`(?s)SELECT id, CASE WHEN octet_length.*<=2048.*ELSE NULL END.*AS oversized FROM jobs.*org_id=\$1.*session_id'=\$2.*metadata_id'=\$3.*ORDER BY updated_at DESC,id LIMIT 32`).WithArgs(i.OrgID, i.SessionID.String(), i.MetadataID.String()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "intent", "oversized"}).AddRow(i.JobID, nil, true)).RowsWillBeClosed()
			rows := int64(1)
			if corrected {
				rows = 0
			}
			mock.ExpectExec(`(?s)UPDATE jobs SET payload=payload \|\| jsonb_build_object\('terminal_preparation_intent_consumed',true\).*WHERE org_id=\$1 AND id=\$2.*session_id'=\$3.*metadata_id'=\$4.*octet_length.*>2048 AND NOT\(payload \? 'terminal_preparation_intent_consumed'\)`).WithArgs(i.OrgID, i.JobID, i.SessionID.String(), i.MetadataID.String()).WillReturnResult(pgxmock.NewResult("UPDATE", rows))
			actual, err := NewJobStore(mock).PendingCodeReviewPreparationIntents(context.Background(), i.OrgID, i.MetadataID, i.SessionID)
			require.NoError(t, err, "oversized discard must grant no metadata authority")
			require.Equal(t, CodeReviewPreparationIntentBatch{More: corrected}, actual, "a corrected replacement must force another bounded read before any fanout")
			require.NoError(t, mock.ExpectationsWereMet(), "oversized JSON must remain inside PostgreSQL and discard must retain its size CAS")
		})
	}
}

func TestCodeReviewPreparationIntentLocksCurrentHeadBeforeProjection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                             string
		missing, readFailure, casFailure bool
	}{
		{name: "committed moved head supersedes old intent"},
		{name: "missing current PR cannot authorize projection", missing: true},
		{name: "PR lock read error retains checkpoint", readFailure: true},
		{name: "metadata failure after locked head rolls back", casFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated current-head projection mock")
			defer mock.Close()
			i := preparationIntentTestValue()
			i.ThreadID, i.TurnScope, i.Turn = nil, models.CodeReviewPreparationTurnSession, 3
			raw, err := json.Marshal(i)
			require.NoError(t, err, "encode the exact failed generation and session scope")
			failure := errors.New("current-head projection storage unavailable")
			expectPublicationTransaction(mock)
			head := mock.ExpectQuery(`SELECT COALESCE\(head_sha,''\)=\$3 FROM pull_requests WHERE org_id=\$1 AND id=\$2 FOR NO KEY UPDATE`).WithArgs(i.OrgID, i.PullRequestID, i.HeadSHA)
			if tt.readFailure {
				head.WillReturnError(failure)
			} else {
				rows := pgxmock.NewRows([]string{"head_matches"})
				if !tt.missing {
					rows.AddRow(tt.casFailure)
				}
				head.WillReturnRows(rows)
				mock.ExpectQuery(`(?s)SELECT payload->'terminal_preparation_intent' FROM jobs WHERE org_id=\$1 AND id=\$2 AND status='dead_letter'.*FOR UPDATE`).WithArgs(i.OrgID, i.JobID, i.SessionID.String()).WillReturnRows(pgxmock.NewRows([]string{"intent"}).AddRow(raw))
				mock.ExpectQuery(`(?s)SELECT s.workspace_generation,s.current_turn.*s.org_id=\$1 AND s.id=\$2 AND m.id=\$3 FOR UPDATE OF s,m`).WithArgs(i.OrgID, i.SessionID, i.MetadataID).WillReturnRows(pgxmock.NewRows([]string{"generation", "turn", "cancelled"}).AddRow(i.WorkspaceGeneration, i.Turn-1, false))
				if tt.casFailure {
					mock.ExpectQuery(`(?s)SELECT EXISTS\(SELECT 1 FROM jobs j.*j.org_id=\$1 AND j.id<>\$2.*j.created_at >=.*j.lock_token IS NOT NULL AND j.lease_expires_at>clock_timestamp\(\)`).WithArgs(i.OrgID, i.JobID, i.SessionID.String(), "").WillReturnRows(pgxmock.NewRows([]string{"overlap"}).AddRow(false))
					mock.ExpectQuery(`(?s)SELECT EXISTS\(SELECT 1 FROM session_executors.*org_id=\$1 AND session_id=\$2.*thread_runtimes`).WithArgs(i.OrgID, i.SessionID).WillReturnRows(pgxmock.NewRows([]string{"live"}).AddRow(false))
					mock.ExpectQuery(`(?s)WITH candidate AS.*m.org_id = @org_id.*m.head_sha = @head_sha.*s.workspace_generation = @generation.*FOR UPDATE OF s, m.*UPDATE code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": i.OrgID, "review_id": i.MetadataID, "session_id": i.SessionID, "pull_request_id": i.PullRequestID, "head_sha": i.HeadSHA, "generation": i.WorkspaceGeneration, "reason": "bounded operational reason"}).WillReturnError(failure)
				} else {
					mock.ExpectExec(`(?s)UPDATE jobs SET payload=payload \|\| jsonb_build_object\('terminal_preparation_intent_consumed',true\).*WHERE org_id=\$1 AND id=\$2 AND status='dead_letter'`).WithArgs(i.OrgID, i.JobID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
			}
			if tt.readFailure || tt.casFailure {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			actual, err := NewCodeReviewStore(mock).ProjectCodeReviewPreparationIntent(context.Background(), i.OrgID, i, "bounded operational reason")
			if tt.readFailure || tt.casFailure {
				require.ErrorIs(t, err, failure, "head/CAS storage error must preserve the original terminal checkpoint for projection retry")
			} else {
				require.NoError(t, err, "moved or missing PR head must consume the old intent as superseded")
			}
			require.Equal(t, CodeReviewPreparationProjection{}, actual, "no uncommitted or stale-head transition may claim successful projection")
			require.NoError(t, mock.ExpectationsWereMet(), "the PR row lock must precede job/session locks and remain held through CAS/consumption")
		})
	}
}
