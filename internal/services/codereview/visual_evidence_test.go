package codereview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type visualEvidenceDiscovererStub struct {
	mu        sync.Mutex
	discovery models.CodeReviewVisualEvidenceDiscovery
	err       error
	calls     int
}

func (s *visualEvidenceDiscovererStub) DiscoverCodeReviewVisualEvidence(context.Context, uuid.UUID, uuid.UUID, int) (models.CodeReviewVisualEvidenceDiscovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.discovery, s.err
}

func (s *visualEvidenceDiscovererStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type visualEvidencePromptStoreStub struct {
	mu      sync.Mutex
	records map[string]models.CodeReviewPromptRecord
}

func (s *visualEvidencePromptStoreStub) GetPromptRecordByKey(_ context.Context, orgID uuid.UUID, recordKey string) (models.CodeReviewPromptRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[orgID.String()+"/"+recordKey]
	if !ok {
		return models.CodeReviewPromptRecord{}, pgx.ErrNoRows
	}
	return record, nil
}

func (s *visualEvidencePromptStoreStub) CreatePromptRecordIfAbsent(_ context.Context, record *models.CodeReviewPromptRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.OrgID.String() + "/" + record.RecordKey
	if existing, ok := s.records[key]; ok {
		*record = existing
		return false, nil
	}
	copy := *record
	copy.ID = uuid.New()
	copy.CreatedAt = time.Now().UTC()
	copy.Metadata = append(json.RawMessage(nil), record.Metadata...)
	s.records[key] = copy
	*record = copy
	return true, nil
}

type visualEvidenceRepositoryStoreStub struct {
	repository models.Repository
	err        error
	calls      int
	mu         sync.Mutex
}

func (s *visualEvidenceRepositoryStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.repository, s.err
}

type visualEvidenceTokenProviderStub struct {
	token string
	err   error
	calls int
	mu    sync.Mutex
}

func (s *visualEvidenceTokenProviderStub) GetInstallationToken(context.Context, int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.token, s.err
}

type visualEvidenceUploadStoreStub struct {
	mu        sync.Mutex
	saved     map[string][]byte
	saveErr   error
	saveCalls int
}

func (s *visualEvidenceUploadStoreStub) Save(_ context.Context, key string, reader io.Reader, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if s.saveErr != nil {
		return "", s.saveErr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	s.saved[key] = data
	return "/api/v1/uploads/files/" + key, nil
}

func (s *visualEvidenceUploadStoreStub) Open(context.Context, string) (io.ReadCloser, string, error) {
	return nil, "", errors.New("not implemented")
}

func (s *visualEvidenceUploadStoreStub) URL(key string) string                            { return "/api/v1/uploads/files/" + key }
func (s *visualEvidenceUploadStoreStub) Serve(http.ResponseWriter, *http.Request, string) {}

type visualEvidenceRoundTripper struct {
	mu       sync.Mutex
	requests map[string][]http.Header
	counts   map[string]int
	png      []byte
}

func (r *visualEvidenceRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	urlString := request.URL.String()
	r.requests[urlString] = append(r.requests[urlString], request.Header.Clone())
	r.counts[urlString]++
	count := r.counts[urlString]
	r.mu.Unlock()

	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
	switch request.URL.Hostname() {
	case "github.com":
		response.StatusCode = http.StatusFound
		response.Header.Set("Location", "https://github-production-user-asset-6210df.s3.amazonaws.com/signed?token=secret")
		response.Body = io.NopCloser(strings.NewReader(""))
	case "github-production-user-asset-6210df.s3.amazonaws.com", "evidence.example.com":
		response.Body = io.NopCloser(bytes.NewReader(r.png))
		response.ContentLength = int64(len(r.png))
	case "retry.example.com":
		if count < visualEvidenceFetchAttempts {
			response.StatusCode = http.StatusServiceUnavailable
			response.Body = io.NopCloser(strings.NewReader("retry"))
		} else {
			response.Body = io.NopCloser(bytes.NewReader(r.png))
			response.ContentLength = int64(len(r.png))
		}
	default:
		response.StatusCode = http.StatusNotFound
		response.Body = io.NopCloser(strings.NewReader("missing"))
	}
	return response, nil
}

func (r *visualEvidenceRoundTripper) headers(rawURL string) []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.requests[rawURL]...)
}

func (r *visualEvidenceRoundTripper) count(rawURL string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[rawURL]
}

func TestVisualEvidenceServiceCapturePersistsAndRestoresManifest(t *testing.T) {
	t.Parallel()
	type imageMetric struct {
		surface      models.CodeReviewEvidenceSurface
		status       models.CodeReviewVisualEvidenceFetchStatus
		fetched      bool
		deduplicated bool
	}

	orgID, sessionID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	headSHA := strings.Repeat("a", 40)
	capturedAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	githubURL := "https://github.com/user-attachments/assets/private-image"
	externalURL := "https://evidence.example.com/same-image.png"
	missingURL := "https://missing.example.com/not-found.png"
	overflowURL := "https://overflow.example.com/image.png"
	discoverer := &visualEvidenceDiscovererStub{discovery: models.CodeReviewVisualEvidenceDiscovery{
		Version: 1, RepositoryID: repositoryID, Repository: "acme/web", PullRequestNumber: 42, HeadSHA: headSHA, CapturedAt: capturedAt,
		Sources: []models.CodeReviewVisualEvidenceSource{
			newVisualEvidenceSource("source-description", models.CodeReviewEvidenceSurfaceDescription, githubURL),
			newVisualEvidenceSource("source-comment-duplicate-url", models.CodeReviewEvidenceSurfaceIssueComment, githubURL),
			newVisualEvidenceSource("source-review-duplicate-bytes", models.CodeReviewEvidenceSurfaceReviewBody, externalURL),
			newVisualEvidenceSource("source-inline-missing", models.CodeReviewEvidenceSurfaceReviewComment, missingURL),
			newVisualEvidenceSource("source-overflow", models.CodeReviewEvidenceSurfaceIssueComment, overflowURL),
		},
	}}
	prompts := &visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)}
	repos := &visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repositoryID, OrgID: orgID, InstallationID: 456}}
	tokens := &visualEvidenceTokenProviderStub{token: "installation-token"}
	uploads := &visualEvidenceUploadStoreStub{saved: make(map[string][]byte)}
	roundTripper := &visualEvidenceRoundTripper{requests: make(map[string][]http.Header), counts: make(map[string]int), png: encodeVisualEvidencePNG(t, 10, 8)}
	service := NewVisualEvidenceService(discoverer, prompts, repos, tokens, uploads, zerolog.Nop())
	service.maxImages = 4
	var imageMetrics []imageMetric
	service.recordImage = func(_ context.Context, surface, status, _ string, _ int64, _ float64, fetched, deduplicated bool) {
		imageMetrics = append(imageMetrics, imageMetric{
			surface: models.CodeReviewEvidenceSurface(surface), status: models.CodeReviewVisualEvidenceFetchStatus(status),
			fetched: fetched, deduplicated: deduplicated,
		})
	}
	service.downloader.client = &http.Client{Transport: roundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	service.downloader.retryWait = func(context.Context, time.Duration) error { return nil }
	input := CaptureVisualEvidenceInput{OrgID: orgID, SessionID: sessionID, RepositoryID: repositoryID, PullRequestNumber: 42, HeadSHA: headSHA}

	snapshot, err := service.Capture(context.Background(), input)

	require.NoError(t, err, "Capture should persist a complete immutable visual-evidence manifest")
	require.Equal(t, visualEvidenceSnapshotVersion, snapshot.Version, "manifest should use the current materialization version")
	require.True(t, snapshot.Complete, "a successful four-surface discovery should produce a complete manifest")
	require.True(t, snapshot.Overflow, "sources beyond the configured image limit should be represented by aggregate overflow metadata")
	require.Equal(t, 1, snapshot.OmittedSourceCount, "manifest should count sources omitted before materialization")
	require.Equal(t, capturedAt, snapshot.CapturedAt, "manifest should preserve the authoritative discovery capture time")
	require.Equal(t, []models.CodeReviewVisualEvidenceFetchStatus{
		models.CodeReviewVisualEvidenceFetchStatusAvailable,
		models.CodeReviewVisualEvidenceFetchStatusAvailable,
		models.CodeReviewVisualEvidenceFetchStatusAvailable,
		models.CodeReviewVisualEvidenceFetchStatusUnavailable,
	}, visualEvidenceStatuses(snapshot), "manifest should persist provenance only for retained sources")
	require.NotEmpty(t, snapshot.Evidence[0].StorageKey, "first available evidence should be materialized into first-party storage")
	require.Equal(t, snapshot.Evidence[0].StorageKey, snapshot.Evidence[1].StorageKey, "duplicate URLs should share one stored image")
	require.Equal(t, snapshot.Evidence[0].StorageKey, snapshot.Evidence[2].StorageKey, "byte-identical URLs should share one stored image")
	require.Equal(t, snapshot.Evidence[0].EvidenceID, snapshot.Evidence[1].DuplicateOfEvidenceID, "duplicate URL provenance should point at the first evidence item")
	require.Equal(t, snapshot.Evidence[0].EvidenceID, snapshot.Evidence[2].DuplicateOfEvidenceID, "byte-identical provenance should point at the first evidence item")
	require.Equal(t, 1, uploads.saveCalls, "content-addressed materialization should upload duplicate bytes once")
	require.Equal(t, 1, roundTripper.count(githubURL), "duplicate GitHub URLs should be downloaded once")
	require.Equal(t, []imageMetric{
		{surface: models.CodeReviewEvidenceSurfaceDescription, status: models.CodeReviewVisualEvidenceFetchStatusAvailable, fetched: true},
		{surface: models.CodeReviewEvidenceSurfaceIssueComment, status: models.CodeReviewVisualEvidenceFetchStatusAvailable, deduplicated: true},
		{surface: models.CodeReviewEvidenceSurfaceReviewBody, status: models.CodeReviewVisualEvidenceFetchStatusAvailable, fetched: true, deduplicated: true},
		{surface: models.CodeReviewEvidenceSurfaceReviewComment, status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, fetched: true},
	}, imageMetrics, "image metrics should distinguish fetched URLs and content reuse without retaining omitted provenance")
	require.Equal(t, "Bearer installation-token", roundTripper.headers(githubURL)[0].Get("Authorization"), "private GitHub user attachments should receive installation auth")
	redirectURL := "https://github-production-user-asset-6210df.s3.amazonaws.com/signed?token=secret"
	require.Empty(t, roundTripper.headers(redirectURL)[0].Get("Authorization"), "GitHub installation auth must not cross onto signed storage redirects")
	require.Empty(t, roundTripper.headers(externalURL)[0].Get("Authorization"), "external images must never receive GitHub installation auth")
	require.Equal(t, 1, discoverer.callCount(), "the first capture should query authoritative GitHub evidence once")

	restored, err := service.Capture(context.Background(), input)

	require.NoError(t, err, "Capture retry should restore the immutable persisted manifest")
	require.Equal(t, snapshot, restored, "Capture retry should return the exact first persisted manifest")
	require.Equal(t, 1, discoverer.callCount(), "Capture retry should not refetch mutable GitHub content")
	require.Equal(t, 1, uploads.saveCalls, "Capture retry should not rewrite first-party images")
	require.NotEmpty(t, restored.CanonicalHash(), "persisted visual evidence should expose a stable description-input hash")
}

func TestVisualEvidenceServiceRecordsIncompleteCapture(t *testing.T) {
	t.Parallel()

	type captureMetric struct {
		discovered int
		persisted  int
		complete   bool
		overflow   bool
		restored   bool
	}
	var recorded []captureMetric
	service := NewVisualEvidenceService(
		&visualEvidenceDiscovererStub{err: errors.New("GitHub review comments are unavailable")},
		&visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)},
		&visualEvidenceRepositoryStoreStub{},
		nil,
		&visualEvidenceUploadStoreStub{saved: make(map[string][]byte)},
		zerolog.Nop(),
	)
	service.recordCapture = func(_ context.Context, discovered, persisted int, complete, overflow, restored bool) {
		recorded = append(recorded, captureMetric{
			discovered: discovered, persisted: persisted, complete: complete, overflow: overflow, restored: restored,
		})
	}

	_, err := service.Capture(context.Background(), CaptureVisualEvidenceInput{
		OrgID: uuid.New(), SessionID: uuid.New(), RepositoryID: uuid.New(), PullRequestNumber: 42, HeadSHA: strings.Repeat("f", 40),
	})

	require.Error(t, err, "Capture should fail when an authoritative GitHub surface is unavailable")
	require.Equal(t, []captureMetric{{}}, recorded, "failed capture should emit one bounded incomplete-snapshot metric")
}

func TestVisualEvidenceServiceEnforcesAggregateLimit(t *testing.T) {
	t.Parallel()

	orgID, sessionID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	headSHA := strings.Repeat("b", 40)
	firstPNG := encodeVisualEvidencePNG(t, 2, 2)
	secondPNG := encodeVisualEvidencePNGWithColor(t, 2, 2, color.RGBA{R: 255, A: 255})
	discoverer := &visualEvidenceDiscovererStub{discovery: models.CodeReviewVisualEvidenceDiscovery{
		Version: 1, RepositoryID: repositoryID, Repository: "acme/web", PullRequestNumber: 7, HeadSHA: headSHA, CapturedAt: time.Now().UTC(),
		Sources: []models.CodeReviewVisualEvidenceSource{
			newVisualEvidenceSource("source-one", models.CodeReviewEvidenceSurfaceDescription, "https://one.example.com/image.png"),
			newVisualEvidenceSource("source-two", models.CodeReviewEvidenceSurfaceIssueComment, "https://two.example.com/image.png"),
		},
	}}
	roundTripper := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data := firstPNG
		if request.URL.Hostname() == "two.example.com" {
			data = secondPNG
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: request}, nil
	})
	service := NewVisualEvidenceService(
		discoverer,
		&visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)},
		&visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repositoryID, OrgID: orgID}},
		&visualEvidenceTokenProviderStub{},
		&visualEvidenceUploadStoreStub{saved: make(map[string][]byte)},
		zerolog.Nop(),
	)
	service.maxBytes = int64(len(firstPNG) + len(secondPNG) - 1)
	service.downloader.client = &http.Client{Transport: roundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	snapshot, err := service.Capture(context.Background(), CaptureVisualEvidenceInput{
		OrgID: orgID, SessionID: sessionID, RepositoryID: repositoryID, PullRequestNumber: 7, HeadSHA: headSHA,
	})

	require.NoError(t, err, "Capture should record aggregate overflow without failing the assessment operationally")
	require.True(t, snapshot.Overflow, "aggregate byte overflow should be visible in the immutable manifest")
	require.Equal(t, []models.CodeReviewVisualEvidenceFetchStatus{
		models.CodeReviewVisualEvidenceFetchStatusAvailable, models.CodeReviewVisualEvidenceFetchStatusOverLimit,
	}, visualEvidenceStatuses(snapshot), "only deterministic in-budget content should be stored")
}

func TestVisualEvidenceServiceRejectsChangedHeadAndIncompleteCheckpoint(t *testing.T) {
	t.Parallel()

	orgID, sessionID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	headSHA := strings.Repeat("c", 40)
	input := CaptureVisualEvidenceInput{OrgID: orgID, SessionID: sessionID, RepositoryID: repositoryID, PullRequestNumber: 42, HeadSHA: headSHA}
	tests := []struct {
		name       string
		discovery  models.CodeReviewVisualEvidenceDiscovery
		checkpoint *models.CodeReviewVisualEvidenceSnapshot
		errorText  string
	}{
		{
			name: "head changes during discovery",
			discovery: models.CodeReviewVisualEvidenceDiscovery{
				Version: 1, RepositoryID: repositoryID, Repository: "acme/web", PullRequestNumber: 42,
				HeadSHA: strings.Repeat("d", 40), CapturedAt: time.Now().UTC(),
			},
			errorText: "head changed",
		},
		{
			name:       "stored checkpoint is incomplete",
			checkpoint: &models.CodeReviewVisualEvidenceSnapshot{Version: 1, RepositoryID: repositoryID, PullRequestNumber: 42, HeadSHA: headSHA, Complete: false},
			errorText:  "incomplete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompts := &visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)}
			if tt.checkpoint != nil {
				metadata, err := json.Marshal(tt.checkpoint)
				require.NoError(t, err, "checkpoint fixture should encode")
				key := visualEvidenceRecordKey(sessionID, headSHA)
				prompts.records[orgID.String()+"/"+key] = models.CodeReviewPromptRecord{
					OrgID: orgID, SessionID: sessionID, RecordKey: key, Role: visualEvidencePromptRole, Metadata: metadata,
				}
			}
			service := NewVisualEvidenceService(
				&visualEvidenceDiscovererStub{discovery: tt.discovery}, prompts,
				&visualEvidenceRepositoryStoreStub{}, &visualEvidenceTokenProviderStub{},
				&visualEvidenceUploadStoreStub{saved: make(map[string][]byte)}, zerolog.Nop(),
			)

			_, err := service.Capture(context.Background(), input)

			require.Error(t, err, "Capture should reject stale or incomplete immutable evidence")
			require.Contains(t, err.Error(), tt.errorText, "Capture should explain the invalid snapshot boundary")
		})
	}
}

func TestVisualEvidenceDownloaderRetriesTransientResponses(t *testing.T) {
	t.Parallel()

	roundTripper := &visualEvidenceRoundTripper{requests: make(map[string][]http.Header), counts: make(map[string]int), png: encodeVisualEvidencePNG(t, 4, 3)}
	downloader := newVisualEvidenceDownloader()
	downloader.client = &http.Client{Transport: roundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	downloader.retryWait = func(context.Context, time.Duration) error { return nil }

	result := downloader.fetch(context.Background(), "https://retry.example.com/image.png", "")

	require.Equal(t, models.CodeReviewVisualEvidenceFetchStatusAvailable, result.status, "transient image failures should recover within the bounded retry budget")
	require.Equal(t, visualEvidenceFetchAttempts, roundTripper.count("https://retry.example.com/image.png"), "downloader should make at most three transient attempts")
	require.Equal(t, 4, result.width, "downloaded PNG width should be validated from bytes")
	require.Equal(t, 3, result.height, "downloaded PNG height should be validated from bytes")
}

func TestVisualEvidenceDownloaderRejectsOversizedDimensions(t *testing.T) {
	t.Parallel()

	webp := visualEvidenceVP8X(10_000, 5_000)
	downloader := newVisualEvidenceDownloader()
	downloader.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(webp)), ContentLength: int64(len(webp)), Request: request}, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	result := downloader.fetch(context.Background(), "https://evidence.example.com/huge.webp", "")

	require.Equal(t, models.CodeReviewVisualEvidenceFetchStatusOverLimit, result.status, "decompression-bomb dimensions should be rejected before agent attachment")
	require.Equal(t, 10_000, result.width, "oversized image audit metadata should preserve validated width")
	require.Equal(t, 5_000, result.height, "oversized image audit metadata should preserve validated height")
}

func TestVisualEvidenceDownloaderBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		startURL         string
		response         func(*http.Request, int32) *http.Response
		expectedStatus   models.CodeReviewVisualEvidenceFetchStatus
		expectedRequests int32
	}{
		{
			name:     "rejects content length above ten megabytes",
			startURL: "https://large.example.com/image.png",
			response: func(request *http.Request, _ int32) *http.Response {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), ContentLength: visualEvidenceMaxImageBytes + 1, Request: request}
			},
			expectedStatus: models.CodeReviewVisualEvidenceFetchStatusOverLimit, expectedRequests: 1,
		},
		{
			name:     "caps redirect chains",
			startURL: "https://redirect.example.com/image.png",
			response: func(request *http.Request, count int32) *http.Response {
				header := make(http.Header)
				header.Set("Location", fmt.Sprintf("https://redirect.example.com/image-%d.png", count))
				return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: request}
			},
			expectedStatus: models.CodeReviewVisualEvidenceFetchStatusUnavailable, expectedRequests: visualEvidenceMaxRedirects + 1,
		},
		{
			name:     "revalidates redirect targets",
			startURL: "https://redirect.example.com/image.png",
			response: func(request *http.Request, _ int32) *http.Response {
				header := make(http.Header)
				header.Set("Location", "https://169.254.169.254/latest/meta-data")
				return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: request}
			},
			expectedStatus: models.CodeReviewVisualEvidenceFetchStatusUnsupported, expectedRequests: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			downloader := newVisualEvidenceDownloader()
			downloader.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				count := requests.Add(1)
				return tt.response(request, count), nil
			}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

			result := downloader.fetch(context.Background(), tt.startURL, "installation-token")

			require.Equal(t, tt.expectedStatus, result.status, "downloader should enforce the selected security/resource boundary")
			require.Equal(t, tt.expectedRequests, requests.Load(), "downloader should stop issuing requests at the selected boundary")
		})
	}
}

func TestVisualEvidenceDefaultLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{name: "per image bytes", actual: visualEvidenceMaxImageBytes, expected: 10 << 20},
		{name: "aggregate bytes", actual: visualEvidenceMaxTotalBytes, expected: 64 << 20},
		{name: "pixels", actual: visualEvidenceMaxPixels, expected: 40_000_000},
		{name: "images", actual: visualEvidenceMaxImages, expected: 32},
		{name: "concurrency", actual: visualEvidenceConcurrency, expected: 4},
		{name: "redirects", actual: visualEvidenceMaxRedirects, expected: 3},
		{name: "attempts", actual: visualEvidenceFetchAttempts, expected: 3},
		{name: "timeout seconds", actual: int64(visualEvidenceFetchTimeout / time.Second), expected: 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, tt.actual, "direct-launch visual evidence resource limit should remain conservative")
		})
	}
}

func TestValidateVisualEvidenceURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawURL    string
		expectErr bool
	}{
		{name: "public HTTPS", rawURL: "https://example.com/image.png"},
		{name: "GitHub user attachment", rawURL: "https://github.com/user-attachments/assets/id"},
		{name: "plain HTTP", rawURL: "http://example.com/image.png", expectErr: true},
		{name: "file scheme", rawURL: "file:///etc/passwd", expectErr: true},
		{name: "loopback literal", rawURL: "https://127.0.0.1/image.png", expectErr: true},
		{name: "metadata literal", rawURL: "https://169.254.169.254/latest/meta-data", expectErr: true},
		{name: "IPv6 loopback", rawURL: "https://[::1]/image.png", expectErr: true},
		{name: "embedded credentials", rawURL: "https://user:secret@example.com/image.png", expectErr: true},
		{name: "relative URL", rawURL: "/user-attachments/image.png", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := validateVisualEvidenceURL(tt.rawURL)

			if tt.expectErr {
				require.Error(t, err, "unsafe visual-evidence URL should be rejected")
				return
			}
			require.NoError(t, err, "public HTTPS visual-evidence URL should be accepted")
		})
	}
}

func TestIsBlockedVisualEvidenceIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ip       string
		expected bool
	}{
		{name: "public IPv4", ip: "8.8.8.8", expected: false},
		{name: "public IPv6", ip: "2606:4700:4700::1111", expected: false},
		{name: "RFC1918", ip: "10.1.2.3", expected: true},
		{name: "loopback", ip: "127.0.0.1", expected: true},
		{name: "metadata", ip: "169.254.169.254", expected: true},
		{name: "IPv4-mapped metadata", ip: "::ffff:169.254.169.254", expected: true},
		{name: "Tailscale CGNAT", ip: "100.100.100.100", expected: true},
		{name: "IPv6 unique local", ip: "fd00::1", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, isBlockedVisualEvidenceIP(net.ParseIP(tt.ip)), "SSRF guard should classify the concrete dial address")
		})
	}
}

func TestVisualEvidenceSafeDialControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		address   string
		expectErr bool
	}{
		{name: "public IPv4", address: "8.8.8.8:443"},
		{name: "public IPv6", address: "[2606:4700:4700::1111]:443"},
		{name: "private IPv4", address: "10.0.0.5:443", expectErr: true},
		{name: "metadata", address: "169.254.169.254:443", expectErr: true},
		{name: "loopback IPv6", address: "[::1]:443", expectErr: true},
		{name: "unresolved hostname", address: "example.com:443", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := visualEvidenceSafeDialControl("tcp", tt.address, nil)

			if tt.expectErr {
				require.Error(t, err, "dial-time SSRF guard should reject the concrete target")
				return
			}
			require.NoError(t, err, "dial-time SSRF guard should accept a public concrete target")
		})
	}
}

func TestInspectVisualEvidenceImage(t *testing.T) {
	t.Parallel()

	imageFixture := image.NewRGBA(image.Rect(0, 0, 3, 2))
	var jpegBuffer bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpegBuffer, imageFixture, nil), "JPEG fixture should encode")
	var gifBuffer bytes.Buffer
	require.NoError(t, gif.Encode(&gifBuffer, imageFixture, nil), "GIF fixture should encode")
	tests := []struct {
		name        string
		data        []byte
		contentType string
		width       int
		height      int
		expectErr   bool
	}{
		{name: "PNG", data: encodeVisualEvidencePNG(t, 3, 2), contentType: "image/png", width: 3, height: 2},
		{name: "JPEG", data: jpegBuffer.Bytes(), contentType: "image/jpeg", width: 3, height: 2},
		{name: "GIF", data: gifBuffer.Bytes(), contentType: "image/gif", width: 3, height: 2},
		{name: "WebP", data: decodeVisualEvidenceWebP(t), contentType: "image/webp", width: 3, height: 2},
		{name: "SVG rejected", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), expectErr: true},
		{name: "malformed PNG rejected", data: []byte("\x89PNG\r\n\x1a\ntruncated"), expectErr: true},
		{name: "header-only WebP rejected", data: visualEvidenceVP8X(3, 2), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			contentType, width, height, err := inspectVisualEvidenceImage(tt.data)

			if tt.expectErr {
				require.Error(t, err, "unsupported or malformed image bytes should be rejected")
				return
			}
			require.NoError(t, err, "supported raster image bytes should validate")
			require.Equal(t, tt.contentType, contentType, "image magic should determine the content type")
			require.Equal(t, tt.width, width, "image decoder should return exact width")
			require.Equal(t, tt.height, height, "image decoder should return exact height")
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func newVisualEvidenceSource(id string, surface models.CodeReviewEvidenceSurface, imageURL string) models.CodeReviewVisualEvidenceSource {
	return models.CodeReviewVisualEvidenceSource{
		SourceID: id, Surface: surface, ProviderObjectID: id, SourceURL: "https://github.com/acme/web/pull/42",
		AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 1,
		ImageURL: imageURL, AltText: "untrusted", ContextText: "untrusted context", Untrusted: true,
	}
}

func visualEvidenceStatuses(snapshot models.CodeReviewVisualEvidenceSnapshot) []models.CodeReviewVisualEvidenceFetchStatus {
	statuses := make([]models.CodeReviewVisualEvidenceFetchStatus, 0, len(snapshot.Evidence))
	for _, evidence := range snapshot.Evidence {
		statuses = append(statuses, evidence.Status)
	}
	return statuses
}

func encodeVisualEvidencePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	return encodeVisualEvidencePNGWithColor(t, width, height, color.RGBA{G: 255, A: 255})
}

func encodeVisualEvidencePNGWithColor(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	fixture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			fixture.SetRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, fixture), "PNG fixture should encode")
	return buffer.Bytes()
}

func visualEvidenceVP8X(width, height int) []byte {
	data := make([]byte, 30)
	copy(data[:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8X")
	width--
	height--
	data[24], data[25], data[26] = byte(width), byte(width>>8), byte(width>>16)
	data[27], data[28], data[29] = byte(height), byte(height>>8), byte(height>>16)
	return data
}

func decodeVisualEvidenceWebP(t *testing.T) []byte {
	t.Helper()

	data, err := base64.StdEncoding.DecodeString("UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoDAAIAAgA0JaACdLoB+AADsAD+8Oj3/yC5YXXI1/8gP+QH/ID/+PIAAAA=")
	require.NoError(t, err, "WebP fixture should decode")
	return data
}
