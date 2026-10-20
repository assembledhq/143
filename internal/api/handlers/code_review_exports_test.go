package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type exportHandlerStub struct {
	org     uuid.UUID
	filters models.CodeReviewExportFilters
	cursor  string
	limit   int
	calls   int
	err     error
}

func (s *exportHandlerStub) Export(_ context.Context, org uuid.UUID, f models.CodeReviewExportFilters, cursor string, limit int) (models.CodeReviewExportPage, error) {
	s.org, s.filters, s.cursor, s.limit = org, f, cursor, limit
	s.calls++
	return models.CodeReviewExportPage{Data: []models.CodeReviewExportRecord{}, Meta: models.CodeReviewExportPageMeta{Complete: true}}, s.err
}
func TestCodeReviewExportHandlerValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, change, value string
		noOrg               bool
		status              int
	}{
		{name: "valid explicit cohort", status: 200}, {name: "missing organization", noOrg: true, status: 403},
		{name: "missing repository", change: "repository_id", status: 400}, {name: "invalid time", change: "from", value: "tomorrow", status: 400},
		{name: "empty interval", change: "until", value: "2026-07-15T00:00:00Z", status: 400},
		{name: "unbounded page", change: "limit", value: "101", status: 400}, {name: "negative page", change: "limit", value: "-1", status: 400},
		{name: "unknown time basis", change: "time_basis", value: "publication-or-completion", status: 400},
		{name: "cannot drop unresolved", change: "include_unresolved_publication_time", value: "false", status: 400},
		{name: "unknown organization selector", change: "org_id", value: uuid.NewString(), status: 400},
		{name: "duplicate parameters", change: "duplicate", status: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo := uuid.New(), uuid.New()
			stub := &exportHandlerStub{}
			h := &CodeReviewExportHandler{service: stub}
			q := url.Values{"repository_id": {repo.String()}, "from": {"2026-07-15T00:00:00Z"}, "until": {"2026-10-19T00:00:00Z"}}
			if tt.change == "duplicate" {
				q.Add("repository_id", uuid.NewString())
			} else if tt.change != "" {
				q.Set(tt.change, tt.value)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/export?"+q.Encode(), nil)
			if !tt.noOrg {
				req = req.WithContext(middleware.WithOrgID(req.Context(), org))
			}
			rr := httptest.NewRecorder()
			h.Export(rr, req)
			require.Equal(t, tt.status, rr.Code, "export validates tenant and bounded filter contract")
			if tt.status == 200 {
				require.Equal(t, org, stub.org, "organization comes from authenticated context")
				require.Equal(t, models.CodeReviewExportFilters{RepositoryID: repo, From: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), TimeBasis: models.ReviewExportCreatedOrCompleted, IncludeUnresolvedPublicationTime: true}, stub.filters, "explicit cohort and acquisition semantics reach the service")
				require.Equal(t, "no-store", rr.Header().Get("Cache-Control"), "historical evidence is never shared-cacheable")
			} else {
				require.Equal(t, 0, stub.calls, "invalid requests never acquire snapshots")
			}
		})
	}
}
func TestCodeReviewExportHandlerErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"cursor scope", codereviewsvc.ErrHistoricalExportCursor, 400, "INVALID_EXPORT_CURSOR"},
		{"foreign repository", pgx.ErrNoRows, 404, "REPOSITORY_NOT_FOUND"},
		{"expired or foreign acquisition", db.ErrReviewExportExpired, 410, "EXPORT_EXPIRED"},
		{"bounded resource limit", db.ErrReviewExportLimit, 429, "EXPORT_LIMIT"},
		{"private database error", errors.New("private database detail"), 500, "EXPORT_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &CodeReviewExportHandler{service: &exportHandlerStub{err: tt.err}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/export?repository_id="+uuid.NewString()+"&from=2026-07-15T00:00:00Z&until=2026-10-19T00:00:00Z", nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
			rr := httptest.NewRecorder()
			h.Export(rr, req)
			require.Equal(t, tt.status, rr.Code, "failure returns the documented status")
			require.Contains(t, rr.Body.String(), tt.code, "failure has a stable machine-readable code")
			require.NotContains(t, rr.Body.String(), "private database detail", "internal evidence does not leak into error responses")
		})
	}
}
