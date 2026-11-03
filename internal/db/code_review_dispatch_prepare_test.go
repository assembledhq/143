package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newCodeReviewDispatchPreparation(t *testing.T) (uuid.UUID, CodeReviewDispatchPreparation) {
	t.Helper()
	stamp := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
	model, output := "synthetic-model", "Synthetic retained original output"
	in := CodeReviewDispatchPreparation{ResultID: stamp.Scope.OwnerID, SessionID: stamp.Scope.SessionID,
		MetadataID: uuid.MustParse("55555555-5555-4555-8555-555555555555"), ThreadID: stamp.Scope.ThreadID,
		AgentProvider: string(models.DefaultDefaultAgentType), AgentModel: &model, RawOutput: &output,
		StructuredResult: json.RawMessage(`{"reviewer_key":"synthetic-reviewer","nested":{"retain":true}}`),
		Version:          1, Key: stamp.Scope.ClientMessageID, Digest: stamp.Digest, ReviewOutputKey: "synthetic-original-output"}
	codeReviewDispatchPreparationJSON(t, stamp.OrgID, &in)
	return stamp.OrgID, in
}

func codeReviewDispatchPreparationJSON(t *testing.T, orgID uuid.UUID, in *CodeReviewDispatchPreparation) {
	t.Helper()
	var err error
	// This is a storage/scope fixture, not a claim of semantic codec validation.
	in.Input, err = json.Marshal(map[string]any{"version": 1, "purpose": CallerSendStampCodeReviewDispatch,
		"org_id": orgID, "session_id": in.SessionID, "thread_id": in.ThreadID, "client_message_id": in.Key,
		"message": "Synthetic frozen original command", "images": []string{}, "plan_mode": false})
	require.NoError(t, err, "encode a synthetic scoped command without rendering or minting")
	in.Wake, err = json.Marshal(map[string]any{"job_type": "run_code_review", "queue": "agent",
		"dedupe_key": "code_review:" + in.ReviewOutputKey, "payload": map[string]any{
			"org_id": orgID, "session_id": in.SessionID, "metadata_id": in.MetadataID,
			"review_output_key": in.ReviewOutputKey, "head_sha": "synthetic-original-head",
			"policy_version": 7, "request_context": map[string]any{"retain": "original nested payload"},
			"previous_review_body": "Synthetic original retained review"}})
	require.NoError(t, err, "freeze the entire original wake payload including non-routing controller fields")
}

func codeReviewDispatchPreparationReplace(t *testing.T, raw json.RawMessage, key string, value any) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode only this case's synthetic envelope")
	encoded, err := json.Marshal(value)
	require.NoError(t, err, "encode this case's replacement without changing other fields")
	fields[key] = encoded
	result, err := json.Marshal(fields)
	require.NoError(t, err, "retain all unmodified fields in the synthetic negative fixture")
	return result
}

func TestCodeReviewDispatchPreparationValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*testing.T, *uuid.UUID, *CodeReviewDispatchPreparation)
		want error
	}{
		{name: "zero organization", edit: func(_ *testing.T, o *uuid.UUID, _ *CodeReviewDispatchPreparation) { *o = uuid.Nil }},
		{name: "zero result", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.ResultID = uuid.Nil }},
		{name: "zero session", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.SessionID = uuid.Nil }},
		{name: "zero metadata", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.MetadataID = uuid.Nil }},
		{name: "zero lane", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.ThreadID = uuid.Nil }},
		{name: "unsupported codec", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.Version = 2 }},
		{name: "wrong key", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.Key = "synthetic-wrong" }},
		{name: "zero digest", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Digest = strings.Repeat("0", 64)
		}},
		{name: "uppercase digest", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Digest = strings.Repeat("A", 64)
		}},
		{name: "unknown provider", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.AgentProvider = "synthetic-unknown-provider"
		}},
		{name: "missing output key", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) { in.ReviewOutputKey = "" }},
		{name: "foreign command organization", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "org_id", uuid.New())
		}},
		{name: "foreign command lane", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "thread_id", uuid.New())
		}},
		{name: "foreign command session", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "session_id", uuid.New())
		}},
		{name: "wrong command key", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "client_message_id", "synthetic-other-key")
		}},
		{name: "wrong command version", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "version", 2)
		}},
		{name: "wrong command purpose", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = codeReviewDispatchPreparationReplace(t, in.Input, "purpose", CallerSendStampReviewLoopStartup)
		}},
		{name: "wrong wake queue", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "queue", "default")
		}},
		{name: "wrong wake type", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "job_type", "run_agent")
		}},
		{name: "wrong wake dedupe", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "dedupe_key", "synthetic-other")
		}},
		{name: "missing payload scope", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "payload", map[string]any{})
		}},
		{name: "foreign wake metadata", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			var wake map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(in.Wake, &wake), "decode synthetic wake before replacing only its metadata identity")
			payload := codeReviewDispatchPreparationReplace(t, wake["payload"], "metadata_id", uuid.New())
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "payload", payload)
		}},
		{name: "unknown wake envelope field", edit: func(t *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = codeReviewDispatchPreparationReplace(t, in.Wake, "extra", true)
		}},
		{name: "duplicate command field", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = append([]byte(`{"version":1,`), in.Input[1:]...)
		}},
		{name: "trailing command", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = append(in.Input, []byte(` {}`)...)
		}},
		{name: "malformed business JSON", edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.StructuredResult = json.RawMessage(`{"broken"`)
		}},
		{name: "raw command bound", want: ErrCodeReviewDispatchPreparationTooLarge, edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Input = make([]byte, codeReviewDispatchPreparationMaxBytes+1)
		}},
		{name: "raw wake bound", want: ErrCodeReviewDispatchPreparationTooLarge, edit: func(_ *testing.T, _ *uuid.UUID, in *CodeReviewDispatchPreparation) {
			in.Wake = make([]byte, codeReviewDispatchPreparationMaxBytes+1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, in := newCodeReviewDispatchPreparation(t)
			tt.edit(t, &org, &in)
			mock := newCallerSendStampMock(t)
			actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
			want := tt.want
			if want == nil {
				want = ErrCodeReviewDispatchPreparationInvalid
			}
			require.ErrorIs(t, err, want, "invalid preparation must be a safe pre-query refusal")
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "pre-query refusal must not expose provisional business or wake data")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid scope must not issue SQL, transaction control, send or notification")
		})
	}
}

func codeReviewDispatchPreparationExpected(in CodeReviewDispatchPreparation, org uuid.UUID) models.CodeReviewAgentResult {
	return models.CodeReviewAgentResult{ID: in.ResultID, OrgID: org, SessionID: in.SessionID, AgentProvider: in.AgentProvider,
		AgentModel: in.AgentModel, Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusQueued,
		RawOutput: in.RawOutput, StructuredResult: in.StructuredResult, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func expectCodeReviewDispatchPreparationPrelock(mock pgxmock.PgxPoolIface, org uuid.UUID, in CodeReviewDispatchPreparation, found bool) {
	rows := pgxmock.NewRows([]string{"id"})
	if found {
		rows.AddRow(in.MetadataID)
	}
	mock.ExpectQuery(codeReviewDispatchPreparationMetadataPrelockQuery).WithArgs(org, in.SessionID, in.MetadataID, in.ReviewOutputKey).
		WillReturnRows(rows).RowsWillBeClosed()
}

func expectCodeReviewDispatchPreparation(mock pgxmock.PgxPoolIface, org uuid.UUID, in CodeReviewDispatchPreparation, prelocked bool, observations ...codeReviewConfirmedCompletionObservation) *pgxmock.ExpectedQuery {
	var observation codeReviewConfirmedCompletionObservation
	if len(observations) > 0 {
		observation = observations[0]
	}
	if prelocked {
		var id *uuid.UUID
		if observation.found {
			id = &observation.assessmentID
		}
		mock.ExpectQuery(codeReviewConfirmedCompletionObservationQuery).
			WithArgs(org, in.MetadataID, codeReviewConfirmedCompletionObservationMaxBytes).
			WillReturnRows(codeReviewConfirmedCompletionTestRows(id, observation.pending, true, codeReviewConfirmedCompletionTestReceipt(observation.assessmentID)))
	}
	return mock.ExpectQuery(codeReviewDispatchPreparationQuery).WithArgs(org, in.SessionID, in.MetadataID, in.ResultID, in.ThreadID,
		in.AgentProvider, in.AgentModel, in.Key, in.Digest, []byte(in.Input), []byte(in.Wake), in.ReviewOutputKey,
		codeReviewDispatchPreparationMaxBytes, in.RawOutput, []byte(in.StructuredResult), prelocked,
		observation.found, observation.assessmentID, observation.version, observation.pending)
}

func TestPrepareCodeReviewDispatchInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                            string
		owner, enabled, fits, prelocked bool
		job                             bool
		want                            error
	}{
		{name: "fresh result and wake", owner: true, enabled: true, fits: true, prelocked: true, job: true},
		{name: "ordinary wake dedupe no-op", owner: true, enabled: true, fits: true, prelocked: true},
		{name: "missing or inactive owner", fits: true, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "prelock miss cannot adopt a newly eligible later owner", owner: true, enabled: true, fits: true, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "confirmed publication is a known ineligible owner", prelocked: true, fits: true, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "disabled or absent control", owner: true, prelocked: true, fits: true, want: ErrCodeReviewDispatchPreparationDisabled},
		{name: "actual JSONB bound", owner: true, prelocked: true, enabled: true, want: ErrCodeReviewDispatchPreparationTooLarge},
		{name: "prelock miss still measures actual JSONB bound first", want: ErrCodeReviewDispatchPreparationTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, in := newCodeReviewDispatchPreparation(t)
			mock := newCallerSendStampMock(t)
			var raw json.RawMessage
			var jobID uuid.UUID
			expected := codeReviewDispatchPreparationExpected(in, org)
			if tt.want == nil {
				var err error
				raw, err = json.Marshal(expected)
				require.NoError(t, err, "encode only the existing public business projection")
			}
			if tt.job {
				jobID = uuid.New()
			}
			expectCodeReviewDispatchPreparationPrelock(mock, org, in, tt.prelocked)
			expectCodeReviewDispatchPreparation(mock, org, in, tt.prelocked).WillReturnRows(pgxmock.NewRows([]string{"owner", "enabled", "fits", "business", "job_id"}).AddRow(tt.owner, tt.enabled, tt.fits, raw, jobID))
			actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want, "known in-statement refusals must retain their finite outcome")
				require.Equal(t, PreparedCodeReviewDispatch{}, actual, "refusal must discard staged data")
			} else {
				require.NoError(t, err, "a supplied transaction may atomically prepare an eligible fresh dispatch")
				require.Equal(t, expected, actual.Result(), "the complete original business result must survive preparation")
				require.Equal(t, jobID, actual.WakeJobID(), "only the actual inserted wake identity may be returned")
				require.Equal(t, tt.job, actual.WakeInserted(), "a dedupe no-op must not claim an existing consumer")
				copy := actual.Result()
				copy.StructuredResult[0] = 'x'
				*copy.AgentModel = "changed"
				*copy.RawOutput = "changed"
				require.Equal(t, expected, actual.Result(), "returned business data must not mutate the sealed provisional result")
				encoded, marshalErr := json.Marshal(actual) //nolint:staticcheck // Intentionally verify sealed persistence capabilities serialize without private fields.
				require.NoError(t, marshalErr, "encode the sealed provisional wrapper")
				require.Equal(t, `{}`, string(encoded), "the wrapper must not implicitly expose private command or wake fields")
				encoded, marshalErr = json.Marshal(in)
				require.NoError(t, marshalErr, "encode the private preparation carrier without exposing its contents")
				require.Equal(t, `{}`, string(encoded), "the preparation carrier must not implicitly expose any frozen input or correlation")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "preparation must consume the exact metadata SHARE prelock before its one atomic intent/wake INSERT statement without settlement or notifications")
		})
	}
}

func TestPrepareCodeReviewDispatchStorageErrorIsPrivate(t *testing.T) {
	t.Parallel()
	org, in := newCodeReviewDispatchPreparation(t)
	mock := newCallerSendStampMock(t)
	cause := &pgconn.PgError{Code: "23514", Message: "synthetic private SQL payload", Detail: "synthetic private retained input"}
	expectCodeReviewDispatchPreparationPrelock(mock, org, in, true)
	expectCodeReviewDispatchPreparation(mock, org, in, true).WillReturnError(cause)
	actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
	require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationStorage, "SQL failure must remain a non-settlement storage outcome")
	require.EqualError(t, err, "reviewer dispatch preparation storage failed", "rendered errors must not include raw database payloads")
	var retained *pgconn.PgError
	require.True(t, errors.As(err, &retained), "the outer owner must retain the original typed SQL cause for classification")
	require.Equal(t, "23514", retained.Code, "safe classification must retain the original SQLSTATE")
	require.Equal(t, PreparedCodeReviewDispatch{}, actual, "SQL failures must not leak provisional results")
	require.NoError(t, mock.ExpectationsWereMet(), "a later statement failure must not start an alternate query, settlement or fallback")
}

func TestPrepareCodeReviewDispatchMetadataPrelockFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rows       func(CodeReviewDispatchPreparation) *pgxmock.Rows
		queryError bool
	}{
		{name: "query error", queryError: true},
		{name: "scan error", rows: func(CodeReviewDispatchPreparation) *pgxmock.Rows {
			return pgxmock.NewRows([]string{"id"}).AddRow("not-a-uuid")
		}},
		{name: "unexpected owner row", rows: func(CodeReviewDispatchPreparation) *pgxmock.Rows {
			return pgxmock.NewRows([]string{"id"}).AddRow(uuid.New())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, in := newCodeReviewDispatchPreparation(t)
			mock := newCallerSendStampMock(t)
			cause := &pgconn.PgError{Code: "55P03", Message: "synthetic private prelock detail"}
			prelock := mock.ExpectQuery(codeReviewDispatchPreparationMetadataPrelockQuery).WithArgs(org, in.SessionID, in.MetadataID, in.ReviewOutputKey)
			if tt.queryError {
				prelock.WillReturnError(cause)
			} else {
				prelock.WillReturnRows(tt.rows(in)).RowsWillBeClosed()
			}
			actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
			require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationStorage, "metadata prelock errors must abort without interpreting failure as an eligible or missing owner")
			require.EqualError(t, err, "reviewer dispatch preparation storage failed", "prelock errors must not expose raw private SQL or scope values")
			if tt.queryError {
				require.ErrorIs(t, err, cause, "an actual prelock query failure must preserve its exact private cause for the existing outer owner")
			}
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "prelock failure cannot release a provisional result, wake or known-refusal carrier")
			require.NoError(t, mock.ExpectationsWereMet(), "a failed or malformed exact metadata prelock must never reach the later INSERT snapshot or any fallback")
		})
	}
}

func TestPrepareCodeReviewDispatchConfirmedPublicationQueryBoundary(t *testing.T) {
	t.Parallel()
	require.Equal(t, `SELECT m.id FROM code_review_session_metadata m
 WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$3
 AND m.status IN ('queued','running') AND NOT m.stale AND m.review_output_key=$4
 FOR SHARE OF m`, codeReviewDispatchPreparationMetadataPrelockQuery, "prelock must serialize only the independently retained eligible exact metadata owner before the later snapshot")
	require.Contains(t, codeReviewDispatchPreparationQuery, "WHERE $16 AND m.org_id=$1 AND m.session_id=$2 AND m.id=$3", "an initial missing prelock cannot discover a newly eligible owner in the later INSERT statement")
	require.Contains(t, codeReviewDispatchPreparationQuery, "AND NOT $20", "new sends must refuse the bounded shared Go receipt observation's positive result")
	require.Contains(t, codeReviewDispatchPreparationQuery, "a.publication_key=m.review_output_key AND a.id=$18 AND a.xmin::text=$19", "an observed candidate must retain both its exact original identity and version in the later insertion snapshot")
	require.Contains(t, codeReviewDispatchPreparationQuery, "OR (NOT $17 AND NOT EXISTS", "a candidate appearing after initial absence must prevent both insertions")
	require.NotContains(t, codeReviewDispatchPreparationMetadataPrelockQuery, "code_review_revision_assessments", "metadata must be acquired before taking the later fresh assessment snapshot, not combined with an old statement snapshot")
	require.NotContains(t, codeReviewDispatchPreparationQuery, "FOR UPDATE OF a", "producer prerequisite must not reverse metadata/job/assessment lock order by locking an assessment")
	require.Contains(t, codeReviewDispatchPreparationQuery, "INSERT INTO code_review_agent_results", "the result remains in the actual later data-modifying statement")
	require.Contains(t, codeReviewDispatchPreparationQuery, "INSERT INTO jobs", "the original wake must remain atomic with the fresh result rather than become a separate durable command")
	require.Contains(t, codeReviewDispatchPreparationQuery, "octet_length(command::text)<=$13 AND octet_length(wake::text)<=$13 AS fits", "actual JSONB presentation limits remain independently evaluated even after a prelock miss")
}

func TestPrepareCodeReviewDispatchBoundedReceiptObservation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending bool
		owner   bool
		enabled bool
		fits    bool
		want    error
	}{
		{name: "matching confirmed candidate refuses", pending: true, fits: true, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "changed candidate refuses despite initially negative proof", fits: true, want: ErrCodeReviewDispatchPreparationIneligible},
		{name: "actual JSONB bound precedes owner refusal", pending: true, want: ErrCodeReviewDispatchPreparationTooLarge},
		{name: "unchanged negative candidate reaches disabled control", owner: true, fits: true, want: ErrCodeReviewDispatchPreparationDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, in := newCodeReviewDispatchPreparation(t)
			mock := newCallerSendStampMock(t)
			observation := codeReviewConfirmedCompletionObservation{found: true, assessmentID: uuid.New(), version: "719", pending: tt.pending}
			expectCodeReviewDispatchPreparationPrelock(mock, org, in, true)
			expectCodeReviewDispatchPreparation(mock, org, in, true, observation).
				WillReturnRows(pgxmock.NewRows([]string{"owner", "enabled", "fits", "business", "job_id"}).AddRow(tt.owner, tt.enabled, tt.fits, nil, uuid.Nil))
			actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
			require.ErrorIs(t, err, tt.want, "later SQL owns fits, exact version/absence, and enabled precedence after the shared Go observation")
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "observation or eligibility refusal must release neither a result nor a wake")
			require.NoError(t, mock.ExpectationsWereMet(), "metadata prelock, bounded observation, then exact fenced atomic statement must occur in that order")
		})
	}
}

func TestPrepareCodeReviewDispatchObservationFailureAbortsBeforeInsert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		oversize bool
	}{
		{name: "receipt observation storage error"},
		{name: "receipt observation oversize", oversize: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, in := newCodeReviewDispatchPreparation(t)
			mock := newCallerSendStampMock(t)
			expectCodeReviewDispatchPreparationPrelock(mock, org, in, true)
			query := mock.ExpectQuery(codeReviewConfirmedCompletionObservationQuery).
				WithArgs(org, in.MetadataID, codeReviewConfirmedCompletionObservationMaxBytes)
			cause := errors.New("synthetic private receipt observation error")
			if tt.oversize {
				id := uuid.New()
				query.WillReturnRows(codeReviewConfirmedCompletionTestRows(&id, true, false, nil))
			} else {
				query.WillReturnError(cause)
			}
			actual, err := PrepareCodeReviewDispatchInTx(context.Background(), org, mock, in)
			require.ErrorIs(t, err, ErrCodeReviewDispatchPreparationStorage, "failure to observe cannot authorize a negative receipt or an insertion")
			require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservation, "the original observation classification remains available to the transaction owner")
			require.EqualError(t, err, "reviewer dispatch preparation storage failed", "private receipt/query data must stay out of the rendered refusal")
			require.Equal(t, PreparedCodeReviewDispatch{}, actual, "an unobservable receipt cannot leak provisional result or wake data")
			require.NoError(t, mock.ExpectationsWereMet(), "observation failure must stop before enabled-control reads and the result/wake INSERT statement")
		})
	}
}
