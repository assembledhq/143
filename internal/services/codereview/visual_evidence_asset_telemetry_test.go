package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestVisualEvidenceCaptureAttributesOnlyAuthenticatedAssetHops(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		external  bool
		blocked   bool
		redirects int
	}{
		{name: "authenticated asset to signed CDN", redirects: 1},
		{name: "each authenticated GitHub redirect is observed", redirects: 2},
		{name: "private redirect remains blocked", redirects: 1, blocked: true},
		{name: "arbitrary image needs no GitHub token or observer", external: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, repoID, prID, assessmentID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			assetURL := "https://github.com/user-attachments/assets/00000000-1111-2222-3333-444444444444/private-filename.png?signature=private-query"
			cdnURL := "https://cdn.example.invalid/private-cdn-name.png?signature=private-cdn-signature"
			target := assetURL
			if tt.external {
				target = cdnURL
			}
			head := strings.Repeat("a", 40)
			discoverer := &visualEvidenceDiscovererStub{discovery: models.CodeReviewVisualEvidenceDiscovery{Version: 1, RepositoryID: repoID, Repository: "acme/demo", PullRequestNumber: 42, HeadSHA: head, CapturedAt: time.Now().UTC(), Sources: []models.CodeReviewVisualEvidenceSource{newVisualEvidenceSource("synthetic-source", models.CodeReviewEvidenceSurfaceDescription, target)}}}
			prompts := &visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)}
			repos := &visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/demo", InstallationID: 42}}
			tokens := &visualEvidenceTokenProviderStub{token: "private-installation-token"}
			uploads := &visualEvidenceUploadStoreStub{saved: make(map[string][]byte)}
			var logs bytes.Buffer
			logger := zerolog.New(zerolog.SyncWriter(&logs))
			service := NewVisualEvidenceService(discoverer, prompts, repos, tokens, uploads, logger)
			png := encodeVisualEvidencePNG(t, 4, 3)
			var mu sync.Mutex
			var requested []string
			githubHops := 0
			base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				requested = append(requested, request.URL.String())
				if request.URL.Hostname() == "github.com" {
					githubHops++
					require.Equal(t, "Bearer private-installation-token", request.Header.Get("Authorization"), "only the GitHub asset endpoint may receive installation credentials")
					metadata, ok := telemetry.RequestMetadataFromContext(request.Context())
					require.True(t, ok, "asset downloads must receive capture-owned attribution")
					require.Equal(t, telemetry.RequestMetadata{Kind: telemetry.RequestKindAPI, AuthType: telemetry.AuthTypeAppInstallation, InstallationID: 42, Caller: telemetry.CallerVisualAsset, OrgID: orgID.String(), SessionID: sessionID.String(), Repository: "acme/demo", PullRequestID: prID, AssessmentID: assessmentID}, metadata, "capture must derive installation from its owning repository while preserving the PR scope")
					header := make(http.Header)
					destination := cdnURL
					if githubHops < tt.redirects {
						destination = "https://github.com/user-attachments/assets/other-private-id/private-redirect-name.png?signature=private-redirect-query"
					}
					if tt.blocked {
						destination = "https://169.254.169.254/latest/meta-data?private-target=true"
					}
					header.Set("Location", destination)
					return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("private-redirect-body")), Request: request}, nil
				}
				require.Equal(t, "cdn.example.invalid", request.URL.Hostname(), "every redirect must still pass public HTTPS target validation")
				require.Equal(t, "", request.Header.Get("Authorization"), "neither signed CDN redirects nor arbitrary images may receive the GitHub credential")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(png)), ContentLength: int64(len(png)), Request: request}, nil
			})
			service.downloader.client.Transport = telemetry.NewGitHubAssetTransport(base, logger)
			service.downloader.retryWait = func(context.Context, time.Duration) error { return nil }
			parent := telemetry.WithRequestMetadata(jobctx.WithJobID(context.Background(), jobID), telemetry.RequestMetadata{Kind: telemetry.RequestKindOAuth, AuthType: telemetry.AuthTypeUser, InstallationID: 999, Caller: "parent_caller", OrgID: uuid.NewString(), SessionID: uuid.NewString(), Repository: "other/repository", PullRequestID: prID})
			input := CaptureVisualEvidenceInput{OrgID: orgID, SessionID: sessionID, AssessmentID: &assessmentID, RepositoryID: repoID, PullRequestNumber: 42, HeadSHA: head}
			snapshot, err := service.Capture(parent, input)
			require.NoError(t, err, "telemetry must not change capture or persisted checkpoint behavior")
			wantStatus := models.CodeReviewVisualEvidenceFetchStatusAvailable
			if tt.blocked {
				wantStatus = models.CodeReviewVisualEvidenceFetchStatusUnsupported
			}
			require.Equal(t, []models.CodeReviewVisualEvidenceFetchStatus{wantStatus}, visualEvidenceStatuses(snapshot), "asset attribution must preserve image decoding and private-redirect rejection")
			wantRequests := tt.redirects + 1
			wantTokens := 1
			if tt.external {
				wantRequests, wantTokens = 1, 0
			}
			if tt.blocked {
				wantRequests = 1
			}
			require.Equal(t, wantRequests, len(requested), "observation must not add HTTP calls or follow blocked redirect targets")
			require.Equal(t, wantTokens, tokens.calls, "instrumentation must not mint additional credentials or acquire credentials for arbitrary images")
			require.Equal(t, wantTokens, repos.calls, "only authenticated asset discovery should resolve the owning installation")
			events := decodeVisualAssetEvents(t, &logs)
			require.Equal(t, tt.redirects, len(events), "only actually executed authenticated GitHub hops may produce GitHub summaries")
			for _, event := range events {
				require.Equal(t, "github api request", event["message"], "asset observations must remain visible to the native HTTP collector")
				require.Equal(t, telemetry.CallerVisualAsset, event["github_caller"], "asset hops must remain distinct from inherited API callers")
				require.Equal(t, float64(42), event["github_installation_id"], "declared installation must come from the capture's repository")
				require.Equal(t, prID.String(), event["pull_request_id"], "asset observations must retain parent PR ownership")
				require.Equal(t, jobID.String(), event["job_id"], "asset observations must retain the actual controller job")
				_, resourcePresent := event["github_rate_limit_resource"]
				require.False(t, resourcePresent, "missing provider resource headers must never be guessed as core")
			}
			for _, private := range []string{"private-filename", "private-query", "private-cdn", "private-redirect", "private-installation-token", "00000000-1111-2222-3333-444444444444", "private-target"} {
				require.NotContains(t, logs.String(), private, "URL paths, signed redirects, credentials and response bodies must not enter asset logs")
			}
			restored, err := service.Capture(parent, input)
			require.NoError(t, err, "retry must still restore the existing immutable checkpoint")
			require.Equal(t, snapshot, restored, "telemetry must not change the captured image manifest")
			require.Equal(t, wantRequests, len(requested), "checkpoint restoration must not perform or duplicate any downloads")
			require.Equal(t, wantTokens, tokens.calls, "checkpoint restoration must not acquire another token")
		})
	}
}

func TestVisualEvidenceAssetRetriesRemainBoundedAndObservable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		dial   bool
	}{
		{name: "server errors retry three times", status: http.StatusServiceUnavailable},
		{name: "rate limits retain existing bounded retry", status: http.StatusTooManyRequests},
		{name: "private dial guard still refuses every attempt", dial: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			downloader := newVisualEvidenceDownloader(logger)
			calls, waits := 0, 0
			downloader.client.Transport = telemetry.NewGitHubAssetTransport(roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "Bearer synthetic-installation-token", request.Header.Get("Authorization"), "each original retry must retain the intended GitHub authorization")
				if tt.dial {
					return nil, visualEvidenceSafeDialControl("tcp", "127.0.0.1:443", nil)
				}
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private-error-body")), Request: request}, nil
			}), logger)
			downloader.retryWait = func(context.Context, time.Duration) error { waits++; return nil }
			ctx := telemetry.WithInstallationRequestMetadata(context.Background(), 42, "parent_caller")
			result := downloader.fetch(ctx, "https://github.com/user-attachments/assets/private-image.png?signature=private-query", "synthetic-installation-token")
			require.Equal(t, models.CodeReviewVisualEvidenceFetchStatusUnavailable, result.status, "telemetry must preserve existing retry exhaustion and SSRF failure behavior")
			require.Equal(t, visualEvidenceFetchAttempts, calls, "instrumentation must add no exchanges beyond the original retry budget")
			require.Equal(t, visualEvidenceFetchAttempts-1, waits, "instrumentation must preserve the original retry cadence")
			events := decodeVisualAssetEvents(t, &logs)
			require.Equal(t, visualEvidenceFetchAttempts, len(events), "each real authenticated retry attempt must produce exactly one summary")
			for _, private := range []string{"private-image", "private-query", "private-error-body", "synthetic-installation-token"} {
				require.NotContains(t, logs.String(), private, "bounded retry observations must not retain any raw request or response material")
			}
		})
	}
}

func decodeVisualAssetEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var events []map[string]any
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if err == io.EOF {
			return events
		}
		require.NoError(t, err, "every asset summary must remain structured JSON")
		events = append(events, event)
	}
}
