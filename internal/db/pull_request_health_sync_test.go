package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestApplyHealthSyncRejectsSupersededRead(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		head, base string
		status     models.PullRequestStatus
		version    int64
	}{
		{name: "newer authoritative read", head: "old-head", base: "old-base", status: models.PullRequestStatusOpen, version: 12},
		{name: "head webhook invalidated partial row", head: "new-head", base: "old-base", status: models.PullRequestStatusOpen, version: 9},
		{name: "base webhook invalidated partial row", head: "old-head", base: "new-base", status: models.PullRequestStatusOpen, version: 9},
		{name: "closed lifecycle wins", head: "old-head", base: "old-base", status: models.PullRequestStatusClosed, version: 9},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated guarded writer fixture")
			defer mock.Close()
			orgID, prID := uuid.New(), uuid.New()
			now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
			want := models.PullRequestHealthCurrent{PullRequestID: prID, OrgID: orgID, Version: 2, HeadSHA: tt.head, BaseSHA: tt.base, SummaryJSON: []byte(`{"checks":[]}`), SummaryPreviewJSON: []byte(`{"checks":[]}`), EnrichmentStatus: models.PullRequestHealthEnrichmentStatusNotRequested, CheckStateVersion: tt.version, CreatedAt: now, UpdatedAt: now}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COALESCE.*FROM pull_requests.*WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows([]string{"head_sha", "base_sha", "status"}).AddRow(tt.head, tt.base, tt.status))
			mock.ExpectQuery("SELECT .+ FROM pull_request_health_current.*WHERE org_id = @org_id AND pull_request_id = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows(prHealthCurrentColumns).AddRow(prID, orgID, want.Version, tt.head, tt.base, want.SummaryJSON, want.SummaryPreviewJSON, want.EnrichmentStatus, nil, tt.version, now, now))
			mock.ExpectRollback()
			got, applied, err := NewPullRequestStore(mock).ApplyHealthSync(context.Background(), orgID, prID, PullRequestHealthSyncSnapshot{ExpectedHeadSHA: "old-head", ExpectedBaseSHA: "old-base", HeadSHA: "old-head", BaseSHA: "old-base", CheckStateVersion: 10})
			require.NoError(t, err, "superseded reads return a skipped outcome without spending retries")
			require.False(t, applied, "superseded read cannot delete projection rows or overwrite health and CI")
			require.Equal(t, want, got, "guarded writer returns the newer current baseline intact")
			require.NoError(t, mock.ExpectationsWereMet(), "skip must occur under the tenant-scoped PR lock before any mutation")
		})
	}
}
