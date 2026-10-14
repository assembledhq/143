package db

import (
	"context"
	"encoding/json"
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
			priorJSON, err := json.Marshal(models.PullRequestHealthSummary{GitHubSyncVersion: tt.version})
			require.NoError(t, err, "encode prior authoritative reservation separately from projection state")
			want.SummaryJSON = priorJSON
			want.SummaryPreviewJSON = priorJSON
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

func TestHealthSummaryJSONEqualIgnoresOnlyAuthoritativeMetadata(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, first, second string
		equal               bool
	}{
		{name: "new reservation only", first: `{"merge_state":"clean","github_sync_version":1}`, second: `{"github_sync_version":2,"merge_state":"clean"}`, equal: true},
		{name: "legacy missing reservation", first: `{"merge_state":"clean"}`, second: `{"merge_state":"clean","github_sync_version":2}`, equal: true},
		{name: "meaningful health change", first: `{"merge_state":"blocked","github_sync_version":1}`, second: `{"merge_state":"clean","github_sync_version":2}`},
		{name: "unknown legacy field preserved", first: `{"merge_state":"clean","legacy":{"mode":"first"}}`, second: `{"merge_state":"clean","legacy":{"mode":"second"}}`},
		{name: "unknown large numbers remain exact", first: `{"legacy":9007199254740992}`, second: `{"legacy":9007199254740993}`},
		{name: "nested marker is ordinary data", first: `{"legacy":{"github_sync_version":1}}`, second: `{"legacy":{"github_sync_version":2}}`},
		{name: "malformed summary differs", first: `{`, second: `{"merge_state":"clean"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.equal, healthSummaryJSONEqual([]byte(tt.first), []byte(tt.second)), "only top-level authoritative metadata may be excluded from health version comparison")
		})
	}
}
