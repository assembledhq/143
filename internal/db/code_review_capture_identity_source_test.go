package db

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewCaptureIdentitySource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		hint     bool
		count    int
		queryErr bool
		wantErr  error
	}{
		{name: "exact original hint", hint: true, count: 1},
		{name: "unambiguous legacy session", count: 1},
		{name: "missing or foreign hinted owner", hint: true, wantErr: pgx.ErrNoRows},
		{name: "ambiguous legacy session refuses", count: 2, wantErr: ErrCodeReviewCaptureIdentitySourceAmbiguous},
		{name: "storage error never selects owner", queryErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated exact-source projection fixture")
			defer mock.Close()
			orgID, sessionID, prID, metadataID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			var hint *uuid.UUID
			if tt.hint {
				hint = &metadataID
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(codeReviewCaptureIdentitySourceQuery)).WithArgs(orgID, sessionID, prID, hint)
			cause := errors.New("synthetic capture source query failure")
			if tt.queryErr {
				query.WillReturnError(cause)
			} else {
				rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "pull_request_id", "status", "trigger_source"})
				for i := 0; i < tt.count; i++ {
					id := metadataID
					if i != 0 {
						id = uuid.New()
					}
					rows.AddRow(id, orgID, sessionID, prID, models.CodeReviewSessionStatusRunning, models.CodeReviewTriggerSourceAppReviewer)
				}
				query.WillReturnRows(rows)
			}
			actual, err := NewCodeReviewStore(mock).ResolveCaptureIdentitySource(context.Background(), orgID, sessionID, prID, hint)
			if tt.queryErr || tt.wantErr != nil {
				want := tt.wantErr
				if tt.queryErr {
					want = cause
				}
				require.ErrorIs(t, err, want, "missing, ambiguous, or failed source resolution must remain an explicit refusal")
				require.Equal(t, CodeReviewCaptureIdentitySource{}, actual, "refused resolution must expose no arbitrary sibling identity")
			} else {
				require.NoError(t, err, "one exact scoped source may be recognized without loading its body or receipt")
				want := CodeReviewCaptureIdentitySource{MetadataID: metadataID, OrgID: orgID, SessionID: sessionID, PullRequestID: prID, Status: models.CodeReviewSessionStatusRunning, TriggerSource: models.CodeReviewTriggerSourceAppReviewer}
				require.Equal(t, want, actual, "the full bounded source projection must retain exact tenant and owner identity")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "source resolution must use only the bounded supplied DBTX read")
		})
	}
}

func TestCodeReviewCaptureIdentitySourceBoundary(t *testing.T) {
	t.Parallel()
	require.Contains(t, codeReviewCaptureIdentitySourceQuery, "org_id=$1 AND session_id=$2 AND pull_request_id=$3", "source resolution must independently bind tenant, session, and pull request")
	require.Contains(t, codeReviewCaptureIdentitySourceQuery, "($4::uuid IS NULL OR id=$4)", "a supplied hint must be validated in SQL rather than trusted as proof")
	require.Contains(t, codeReviewCaptureIdentitySourceQuery, "LIMIT 2", "legacy ambiguity detection must have bounded transfer and work")
	require.NotContains(t, codeReviewCaptureIdentitySourceQuery, "SELECT *", "source lookup cannot load provider results, prompts, or outcomes")
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create fixture with no allowed SQL")
	defer mock.Close()
	zero := uuid.Nil
	actual, err := NewCodeReviewStore(mock).ResolveCaptureIdentitySource(context.Background(), uuid.New(), uuid.New(), uuid.New(), &zero)
	require.Error(t, err, "a present zero metadata hint must refuse before querying")
	require.Equal(t, CodeReviewCaptureIdentitySource{}, actual, "invalid hint must not fall back to session-only ownership")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid hint cannot perform a fallback read")
}
