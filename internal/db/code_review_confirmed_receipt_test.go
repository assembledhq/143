package db

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func codeReviewConfirmedCompletionTestRows(id *uuid.UUID, structural, fits bool, receipt []byte) *pgxmock.Rows {
	version := ""
	if id != nil {
		version = "719"
	}
	return codeReviewConfirmedCompletionVersionTestRows(id, version, structural, fits, receipt)
}

func codeReviewConfirmedCompletionVersionTestRows(id *uuid.UUID, version string, structural, fits bool, receipt []byte) *pgxmock.Rows {
	var reviewID *int64
	digest, head := "", ""
	if id != nil {
		value := int64(731)
		reviewID, digest, head = &value, "synthetic-digest", "synthetic-head"
	}
	return pgxmock.NewRows([]string{"id", "version", "structural", "fits", "receipt", "digest", "head", "review_id"}).
		AddRow(id, version, structural, fits, receipt, digest, head, reviewID)
}

func codeReviewConfirmedCompletionTestReceipt(id uuid.UUID) []byte {
	return []byte(`{"ASSESSMENT_ID":"` + id.String() + `","INPUT_DIGEST":"synthetic-digest","SUBMITTED_COMMIT_SHA":"synthetic-head","GITHUB_REVIEW_ID":731}`)
}

func TestObserveConfirmedFullAssessmentCompletionPending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		absent     bool
		missing    bool
		structural bool
		fits       bool
		badReceipt bool
		queryError bool
		want       bool
		wantError  bool
		badID      bool
		noVersion  bool
		badAbsence bool
		tooLarge   bool
	}{
		{name: "exact folded receipt", structural: true, fits: true, want: true},
		{name: "candidate version retained for negative structural proof", fits: true},
		{name: "oversized nonconfirmed candidate retains its identity and version"},
		{name: "wrong typed receipt alias is negative", structural: true, fits: true, badReceipt: true},
		{name: "candidate absence retained", absent: true, fits: true},
		{name: "missing metadata is negative", missing: true},
		{name: "oversized confirmed candidate refuses with no observation", structural: true, wantError: true, tooLarge: true},
		{name: "zero candidate id refuses even when structurally ineligible and oversized", badID: true, wantError: true},
		{name: "empty candidate version refuses even when structurally ineligible and oversized", noVersion: true, wantError: true},
		{name: "absent candidate with a version refuses even when oversized", absent: true, badAbsence: true, wantError: true},
		{name: "absent candidate with structural confirmation refuses even when oversized", absent: true, structural: true, wantError: true},
		{name: "storage error refuses with no observation", queryError: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := codeReviewTerminalTestMock(t)
			orgID, metadataID, id := uuid.New(), uuid.New(), uuid.New()
			cause := errors.New("synthetic private observation failure")
			query := mock.ExpectQuery(regexp.QuoteMeta(codeReviewConfirmedCompletionObservationQuery)).
				WithArgs(orgID, metadataID, codeReviewConfirmedCompletionObservationMaxBytes)
			if tt.queryError {
				query.WillReturnError(cause)
			} else if tt.missing {
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}))
			} else {
				receipt := codeReviewConfirmedCompletionTestReceipt(id)
				if tt.badReceipt {
					receipt = []byte(`{"assessment_id":"` + id.String() + `","ASSESSMENT_ID":17,"input_digest":"synthetic-digest","submitted_commit_sha":"synthetic-head","github_review_id":731}`)
				}
				var candidate *uuid.UUID
				version := ""
				if !tt.absent {
					candidate = &id
					version = "719"
				}
				if tt.badID {
					candidate = new(uuid.Nil)
				}
				if tt.noVersion {
					version = ""
				}
				if tt.badAbsence {
					version = "719"
				}
				query.WillReturnRows(codeReviewConfirmedCompletionVersionTestRows(candidate, version, tt.structural, tt.fits, receipt))
			}
			actual, err := observeConfirmedFullAssessmentCompletionPending(context.Background(), mock, orgID, metadataID)
			if tt.wantError {
				require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservation, "observation failure must not authorize negative receipt or liveness inference")
				require.Equal(t, codeReviewConfirmedCompletionObservation{}, actual, "failed observation must erase every candidate, version, and pending result")
				if tt.queryError {
					require.ErrorIs(t, err, cause, "storage classification retains its private cause")
				} else if tt.tooLarge {
					require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservationTooLarge, "oversize must remain an explicit observation refusal")
				} else {
					require.NotErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservationTooLarge, "identity and version inconsistencies must remain invalid observations, not be hidden by receipt size")
				}
			} else {
				require.NoError(t, err, "a bounded supplied-DBTX observation may distinguish matching, nonmatching, and absent candidates")
				expected := codeReviewConfirmedCompletionObservation{}
				if !tt.absent && !tt.missing {
					expected = codeReviewConfirmedCompletionObservation{found: true, assessmentID: id, version: "719", pending: tt.want}
				}
				require.Equal(t, expected, actual, "negative candidates must retain their identity/version so later positive changes cannot evade the fence")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "observation must issue only its exact bounded tenant/metadata projection without locks, writes, settlement, or fallback")
		})
	}
}

func TestCodeReviewStoreConfirmedCompletionReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending bool
		failed  bool
	}{
		{name: "retained recovery ownership without queue inference", pending: true},
		{name: "ordinary original metadata"},
		{name: "observation failure is not liveness absence", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := codeReviewTerminalTestMock(t)
			orgID, metadataID, id := uuid.New(), uuid.New(), uuid.New()
			query := mock.ExpectQuery(regexp.QuoteMeta(codeReviewConfirmedCompletionObservationQuery)).
				WithArgs(orgID, metadataID, codeReviewConfirmedCompletionObservationMaxBytes)
			cause := errors.New("synthetic reader storage failure")
			if tt.failed {
				query.WillReturnError(cause)
			} else {
				query.WillReturnRows(codeReviewConfirmedCompletionTestRows(&id, tt.pending, true, codeReviewConfirmedCompletionTestReceipt(id)))
			}
			actual, err := NewCodeReviewStore(mock).HasConfirmedFullAssessmentCompletionPending(context.Background(), orgID, metadataID)
			require.Equal(t, tt.pending, actual, "only the shared positive original receipt observation grants recovery ownership")
			if tt.failed {
				require.ErrorIs(t, err, cause, "liveness callers must receive observation failure rather than a false absence")
			} else {
				require.NoError(t, err, "read-only ownership recognition needs no queue, provider, or transaction capability")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "public reader must use the same exact bounded observation as retirement and dispatch")
		})
	}
}

func TestConfirmedCompletionObservationDiscardsCancelledRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		beforeRead bool
		structural bool
		fits       bool
	}{
		{name: "already cancelled context issues no query", beforeRead: true},
		{name: "cancelled oversized ordinary candidate releases no identity or version"},
		{name: "cancelled bounded confirmed candidate releases no positive observation", structural: true, fits: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := codeReviewTerminalTestMock(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			orgID, metadataID, id := uuid.New(), uuid.New(), uuid.New()
			var q DBTX = mock
			if tt.beforeRead {
				cancel()
			} else {
				mock.ExpectQuery(regexp.QuoteMeta(codeReviewConfirmedCompletionObservationQuery)).
					WithArgs(orgID, metadataID, codeReviewConfirmedCompletionObservationMaxBytes).
					WillReturnRows(codeReviewConfirmedCompletionTestRows(&id, tt.structural, tt.fits, codeReviewConfirmedCompletionTestReceipt(id)))
				q = &lifecycleScopeCancellingTx{Tx: mock, query: codeReviewConfirmedCompletionObservationQuery, cancel: cancel}
			}
			actual, err := observeConfirmedFullAssessmentCompletionPending(ctx, q, orgID, metadataID)
			require.ErrorIs(t, err, context.Canceled, "cancellation before or synchronously after the bounded scan must preserve its exact context cause")
			require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservation, "cancellation must remain an observation failure rather than a negative completion fact")
			require.Equal(t, codeReviewConfirmedCompletionObservation{}, actual, "even a fully scanned nonconfirmed candidate identity/version must be erased on cancellation")
			require.NoError(t, mock.ExpectationsWereMet(), "cancelled observation must not retry, mutate, settle or continue reading")
		})
	}
}

func TestConfirmedCompletionObservationQueryBoundary(t *testing.T) {
	t.Parallel()
	require.Contains(t, codeReviewConfirmedCompletionObservationQuery, "ON a.org_id=m.org_id AND a.publication_key=m.review_output_key", "candidate existence includes ineligible original-key rows so changes remain fenceable")
	require.Contains(t, codeReviewConfirmedCompletionObservationQuery, "WHERE m.org_id=$1 AND m.id=$2", "the observation must bind an exact metadata identity within one tenant")
	require.Contains(t, codeReviewConfirmedCompletionObservationQuery, "a.xmin::text", "the later statement receives an opaque candidate-version observation")
	require.Contains(t, codeReviewConfirmedCompletionObservationQuery, codeReviewConfirmedCompletionProjectionFits, "receipt and comparison strings must share a transfer bound before decoding")
	require.Contains(t, codeReviewConfirmedCompletionObservationQuery, "THEN a.publication_receipt::text END", "the original JSONB serialization must reach the unchanged Go decoder")
	require.NotContains(t, codeReviewConfirmedCompletionObservationQuery, "publication_receipt->", "SQL must not implement a second alias, type, ordering, or null decoder")
	require.NotContains(t, codeReviewConfirmedCompletionObservationQuery, "SELECT *", "receipt observation cannot load a complete assessment or body")
	require.NotContains(t, codeReviewConfirmedCompletionObservationQuery, "FOR UPDATE", "observation itself grants no new row-lock authority")
	require.NotContains(t, codeReviewConfirmedFullCompletionStructure, "NOT m.stale", "stale-but-running confirmed publication remains preservation truth")
}
