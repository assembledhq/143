package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type exportHandlerStub struct {
	org     uuid.UUID
	repo    uuid.UUID
	id      uuid.UUID
	filters models.CodeReviewExportFilters
	cursor  string
	limit   int
	calls   int
	err     error
}

func (s *exportHandlerStub) Status(_ context.Context, org, repositoryID, id uuid.UUID) (models.CodeReviewExportStatus, error) {
	s.org, s.repo, s.id = org, repositoryID, id
	s.calls++
	return models.CodeReviewExportStatus{}, s.err
}
func (s *exportHandlerStub) Release(_ context.Context, org, repositoryID, id uuid.UUID, proof models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error) {
	s.org, s.repo, s.id = org, repositoryID, id
	s.calls++
	return models.CodeReviewExportStatus{}, s.err
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
		{"busy lock", db.ErrReviewExportBusy, 429, "EXPORT_BUSY"},
		{"active slots", db.ErrReviewExportSlots, 429, "EXPORT_ACTIVE_SLOT_LIMIT"},
		{"row bound", db.ErrReviewExportRows, 422, "EXPORT_ROW_LIMIT"},
		{"byte bound", db.ErrReviewExportBytes, 422, "EXPORT_PAYLOAD_BYTE_LIMIT"},
		{"released acquisition", db.ErrReviewExportReleased, 410, "EXPORT_RELEASED"},
		{"archive mismatch", db.ErrReviewExportArchive, 409, "EXPORT_ARCHIVE_MISMATCH"},
		{"release conflict", db.ErrReviewExportReleaseConflict, 409, "EXPORT_RELEASE_CONFLICT"},
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

func TestCodeReviewExportReleaseAPIValidationAndRole(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, role, body, id, query string
		status                      int
		noOrg                       bool
	}{
		{name: "member release", role: "member", status: 200},
		{name: "builder release", role: "builder", status: 200},
		{name: "admin release", role: "admin", status: 200},
		{name: "viewer cannot release", role: "viewer", status: 403},
		{name: "authentication required", status: 401},
		{name: "tenant required", role: "member", noOrg: true, status: 403},
		{name: "invalid acquisition", role: "member", id: "not-a-uuid", status: 400},
		{name: "query not allowed", role: "member", query: "?org_id=foreign", status: 400},
		{name: "unknown body field", role: "member", body: `{"unknown":true}`, status: 400},
		{name: "missing attestation", role: "member", body: `{}`, status: 400},
		{name: "incomplete digest", role: "member", body: `{"repository_id":"20000000-0000-0000-0000-000000000001","archive":{"manifest_sha256":"abc","verified":true,"archived_records":0,"final_position":0}}`, status: 400},
		{name: "unverified", role: "member", body: `{"repository_id":"20000000-0000-0000-0000-000000000001","archive":{"manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verified":false,"archived_records":0,"final_position":0}}`, status: 400},
		{name: "trailing JSON", role: "member", body: `{} {}`, status: 400},
		{name: "body bound", role: "member", body: strings.Repeat(" ", 4097) + `{}`, status: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo, id := uuid.New(), uuid.New(), uuid.New()
			records := int64(2)
			proof := models.CodeReviewExportArchiveProof{ManifestSHA256: strings.Repeat("a", 64), ArchivedRecords: &records, FinalPosition: &records, Verified: true}
			body, err := json.Marshal(models.CodeReviewExportReleaseRequest{RepositoryID: repo, Archive: proof})
			require.NoError(t, err, "encode synthetic release request")
			if tt.body != "" {
				body = []byte(tt.body)
			}
			pathID := id.String()
			if tt.id != "" {
				pathID = tt.id
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/code-reviews/export/"+pathID+"/release"+tt.query, strings.NewReader(string(body)))
			ctx := req.Context()
			if tt.role != "" {
				ctx = middleware.WithUser(ctx, &models.User{})
				ctx = middleware.WithActiveRole(ctx, tt.role)
			}
			if !tt.noOrg {
				ctx = middleware.WithOrgID(ctx, org)
			}
			req = req.WithContext(ctx)
			stub := &exportHandlerStub{}
			handler := &CodeReviewExportHandler{service: stub}
			router := chi.NewRouter()
			router.With(middleware.RequireRole("admin", "builder", "member")).Post("/api/v1/code-reviews/export/{id}/release", handler.Release)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			require.Equal(t, tt.status, rr.Code, "release respects authenticated active role and bounded attestation contract")
			expectedCalls := 0
			if tt.status == 200 {
				expectedCalls = 1
				require.Equal(t, []uuid.UUID{org, repo, id}, []uuid.UUID{stub.org, stub.repo, stub.id}, "release forwards exact authenticated organization repository and acquisition")
			}
			require.Equal(t, expectedCalls, stub.calls, "invalid release never reaches storage")
		})
	}
}

func TestCodeReviewExportStatusAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
		status      int
	}{{"valid", "", 200}, {"missing repository", "?limit=1", 400}, {"duplicate repository", "?repository_id=x&repository_id=y", 400}, {"unknown selector", "?repository_id=20000000-0000-0000-0000-000000000001&org_id=foreign", 400}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo, id := uuid.New(), uuid.New(), uuid.New()
			query := tt.query
			if query == "" {
				query = "?repository_id=" + repo.String()
			}
			stub := &exportHandlerStub{}
			handler := &CodeReviewExportHandler{service: stub}
			router := chi.NewRouter()
			router.Get("/api/v1/code-reviews/export/{id}", handler.Status)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/export/"+id.String()+query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), org))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			require.Equal(t, tt.status, rr.Code, "metadata access requires exact repository scope")
			if tt.status == 200 {
				require.Equal(t, []uuid.UUID{org, repo, id}, []uuid.UUID{stub.org, stub.repo, stub.id}, "status forwards exact scope without exposing payloads")
				require.Equal(t, "no-store", rr.Header().Get("Cache-Control"), "receipts remain private-cache disabled")
			}
		})
	}
}

type pagedExportAPIStore struct {
	a    models.CodeReviewExportAcquisition
	rows []models.CodeReviewExportSource
}

func (s *pagedExportAPIStore) Acquire(context.Context, uuid.UUID, models.CodeReviewExportFilters) (models.CodeReviewExportAcquisition, error) {
	return s.a, nil
}
func (s *pagedExportAPIStore) Page(_ context.Context, _ uuid.UUID, c models.CodeReviewExportCursor, limit int) (models.CodeReviewExportAcquisition, []models.CodeReviewExportSource, error) {
	if c.Position < 0 || c.Position > int64(len(s.rows)) {
		return s.a, nil, db.ErrReviewExportPosition
	}
	return s.a, s.rows[int(c.Position):min(int(c.Position)+limit, len(s.rows))], nil
}
func (s *pagedExportAPIStore) Status(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewExportStatus, error) {
	return models.CodeReviewExportStatus{}, nil
}
func (s *pagedExportAPIStore) Release(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error) {
	return models.CodeReviewExportStatus{}, nil
}
func TestCodeReviewExportAPILosslessPagination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		count, limit int
	}{{"empty", 0, 100}, {"one", 1, 1}, {"exact page", 100, 100}, {"boundary plus one", 101, 100}, {"many pages", 105, 2}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo := uuid.New(), uuid.New()
			from := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
			filters := models.CodeReviewExportFilters{RepositoryID: repo, From: from, Until: from.AddDate(0, 0, 96), TimeBasis: models.ReviewExportCreatedOrCompleted, IncludeUnresolvedPublicationTime: true}
			store := &pagedExportAPIStore{a: models.CodeReviewExportAcquisition{ID: uuid.New(), SchemaVersion: 1, Filters: filters, Counts: models.CodeReviewExportCounts{Records: int64(tt.count)}}}
			expected := make([]string, 0, tt.count)
			for i := 0; i < tt.count; i++ {
				id := uuid.New()
				store.rows = append(store.rows, models.CodeReviewExportSource{Kind: models.ReviewExportAssessment, ID: id, Source: json.RawMessage(`{"session_id":"same-session","head_sha":"same-head"}`)})
				expected = append(expected, "assessment:"+id.String())
			}
			handler := NewCodeReviewExportHandler(codereviewsvc.NewHistoricalExportService(store))
			cursor := ""
			position := 0
			actual := make([]string, 0, tt.count)
			for {
				q := url.Values{"repository_id": {repo.String()}, "from": {filters.From.Format(time.RFC3339)}, "until": {filters.Until.Format(time.RFC3339)}, "limit": {fmt.Sprint(tt.limit)}}
				if cursor != "" {
					q.Set("cursor", cursor)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/export?"+q.Encode(), nil)
				req = req.WithContext(middleware.WithOrgID(req.Context(), org))
				rr := httptest.NewRecorder()
				handler.Export(rr, req)
				require.Equal(t, 200, rr.Code, "bounded page succeeds")
				var page models.CodeReviewExportPage
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page), "decode full synthetic API page")
				require.Equal(t, store.a, page.Acquisition, "every page retains exact acquisition metadata")
				for _, row := range page.Data {
					actual = append(actual, row.SourceIdentity)
				}
				position += len(page.Data)
				require.Equal(t, int64(position), page.Meta.Position, "cursor chain advances by exact returned identities")
				if page.Meta.Complete {
					require.Nil(t, page.Meta.NextCursor, "complete page has no cursor")
					break
				}
				require.NotEmpty(t, page.Data, "incomplete page must advance")
				require.NotNil(t, page.Meta.NextCursor, "incomplete page has opaque continuation")
				cursor = *page.Meta.NextCursor
				raw, err := base64.RawURLEncoding.DecodeString(cursor)
				require.NoError(t, err, "cursor retains acquisition binding")
				var c models.CodeReviewExportCursor
				require.NoError(t, json.Unmarshal(raw, &c), "decode synthetic cursor")
				require.Equal(t, models.CodeReviewExportCursor{AcquisitionID: store.a.ID, Position: int64(position)}, c, "cursor position identifies this acquisition only")
			}
			require.Equal(t, expected, actual, "all same-session same-head historical identities survive lossless pagination")
		})
	}
}
