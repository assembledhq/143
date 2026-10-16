package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestVisualEvidenceAssessmentCapturesDoNotReuseSessionSnapshot(t *testing.T) {
	t.Parallel()
	orgID, sessionID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	head := strings.Repeat("a", 40)
	imageURL := "https://evidence.example.com/image.png"
	discoverer := &visualEvidenceDiscovererStub{discovery: models.CodeReviewVisualEvidenceDiscovery{
		Version: 1, RepositoryID: repositoryID, Repository: "acme/web", PullRequestNumber: 42,
		HeadSHA: head, CapturedAt: time.Now().UTC(),
		Sources: []models.CodeReviewVisualEvidenceSource{newVisualEvidenceSource("image", models.CodeReviewEvidenceSurfaceDescription, imageURL)},
	}}
	promptStore := &visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)}
	transport := &visualEvidenceRoundTripper{requests: make(map[string][]http.Header), counts: make(map[string]int), png: encodeVisualEvidencePNG(t, 10, 8)}
	service := NewVisualEvidenceService(discoverer, promptStore, &visualEvidenceRepositoryStoreStub{}, nil, &visualEvidenceUploadStoreStub{saved: make(map[string][]byte)}, zerolog.Nop())
	service.downloader.client = &http.Client{Transport: transport}
	input := CaptureVisualEvidenceInput{OrgID: orgID, SessionID: sessionID, AssessmentID: &firstID, RepositoryID: repositoryID, PullRequestNumber: 42, HeadSHA: head}
	first, err := service.Capture(context.Background(), input)
	require.NoError(t, err, "first assessment should capture the original image")

	// Capture waits for all downloads, so the next assessment sees new bytes
	// at the same source URL without racing a downloader.
	transport.png = encodeVisualEvidencePNG(t, 20, 16)
	input.AssessmentID = &secondID
	second, err := service.Capture(context.Background(), input)
	require.NoError(t, err, "second assessment should independently capture the changed image")
	require.Equal(t, &secondID, second.AssessmentID, "new snapshot should retain its assessment identity")
	require.NotEqual(t, first.CanonicalHash(), second.CanonicalHash(), "new bytes at the same URL should invalidate visual evidence equality")
	require.Equal(t, 2, discoverer.callCount(), "two assessments at the same session and head should each discover evidence")
	require.Equal(t, 2, transport.count(imageURL), "each assessment should fetch current bytes")

	input.AssessmentID = &firstID
	restored, err := service.Capture(context.Background(), input)
	require.NoError(t, err, "retry should restore its original assessment snapshot")
	require.Equal(t, first, restored, "new evidence must not overwrite the previous assessment's immutable snapshot")
	require.Equal(t, 2, discoverer.callCount(), "retry should not rediscover mutable evidence")

	input.Fresh = true
	fresh, err := service.Capture(context.Background(), input)
	require.NoError(t, err, "publication freshness check should fetch current bytes")
	require.Equal(t, second.CanonicalHash(), fresh.CanonicalHash(), "freshness check must see changed bytes at the original assessment URL")
	require.Equal(t, 3, transport.count(imageURL), "freshness must not restore the old immutable snapshot")
	input.Fresh = false
	restored, err = service.Capture(context.Background(), input)
	require.NoError(t, err, "original capture should remain readable after a freshness check")
	require.Equal(t, first, restored, "freshness must never overwrite assessment evidence")
	require.Equal(t, 2, len(promptStore.records), "freshness must not create an orphan prompt record")
}

func TestRestoreVisualEvidenceRejectsAssessmentIdentityMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		storedID *uuid.UUID
	}{
		{name: "legacy snapshot", storedID: nil},
		{name: "another assessment", storedID: func() *uuid.UUID { id := uuid.New(); return &id }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			input := CaptureVisualEvidenceInput{OrgID: uuid.New(), SessionID: uuid.New(), AssessmentID: &id, RepositoryID: uuid.New(), PullRequestNumber: 42, HeadSHA: strings.Repeat("a", 40)}
			raw, err := json.Marshal(models.CodeReviewVisualEvidenceSnapshot{AssessmentID: tt.storedID})
			require.NoError(t, err, "fixture should serialize")
			record := models.CodeReviewPromptRecord{OrgID: input.OrgID, SessionID: input.SessionID, RecordKey: visualEvidenceInputRecordKey(input), Role: visualEvidencePromptRole, Metadata: raw}
			_, err = restoreVisualEvidenceSnapshot(record, input)
			require.ErrorContains(t, err, "assessment identity", "a matching record key must not hide a mismatched snapshot identity")
		})
	}
}

func TestVisualEvidenceUsesSharedDiscoveryAfterCheckpointLookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                           string
		seed, fresh, unavailable, wrongHead, wrongRepo bool
		callbackCalls, standaloneCalls                 int
		wantError                                      bool
	}{
		{name: "uncached shared inventory", callbackCalls: 1},
		{name: "checkpoint bypasses shared inventory", seed: true, unavailable: true, standaloneCalls: 1},
		{name: "fresh capture invokes shared inventory", seed: true, fresh: true, callbackCalls: 1, standaloneCalls: 1},
		{name: "shared discovery error propagates", unavailable: true, callbackCalls: 1, wantError: true},
		{name: "shared head fence", wrongHead: true, callbackCalls: 1, wantError: true},
		{name: "shared repository fence", wrongRepo: true, callbackCalls: 1, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, repoID, assessmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			head := strings.Repeat("a", 40)
			capturedAt := time.Now().UTC()
			discovery := models.CodeReviewVisualEvidenceDiscovery{Version: 1, RepositoryID: repoID, Repository: "acme/web", PullRequestNumber: 42, HeadSHA: head, CapturedAt: capturedAt}
			discoverer := &visualEvidenceDiscovererStub{discovery: discovery}
			prompts := &visualEvidencePromptStoreStub{records: make(map[string]models.CodeReviewPromptRecord)}
			service := NewVisualEvidenceService(discoverer, prompts, &visualEvidenceRepositoryStoreStub{}, nil, &visualEvidenceUploadStoreStub{saved: make(map[string][]byte)}, zerolog.Nop())
			input := CaptureVisualEvidenceInput{OrgID: orgID, SessionID: sessionID, AssessmentID: &assessmentID, RepositoryID: repoID, PullRequestNumber: 42, HeadSHA: head}
			if tt.seed {
				_, err := service.Capture(context.Background(), input)
				require.NoError(t, err, "initial standalone capture should seed its immutable checkpoint")
			}
			callbackCalls := 0
			input.Fresh = tt.fresh
			input.Discover = func(context.Context) (models.CodeReviewVisualEvidenceDiscovery, error) {
				callbackCalls++
				if tt.unavailable {
					return models.CodeReviewVisualEvidenceDiscovery{}, errors.New("shared inventory unavailable")
				}
				shared := discovery
				if tt.wrongHead {
					shared.HeadSHA = strings.Repeat("b", 40)
				}
				if tt.wrongRepo {
					shared.RepositoryID = uuid.New()
				}
				return shared, nil
			}
			actual, err := service.Capture(context.Background(), input)
			if tt.wantError {
				require.Error(t, err, "shared inventory failures or identity changes must remain fatal")
				require.Equal(t, 0, len(prompts.records), "failed shared capture must not persist partial evidence")
			} else {
				require.NoError(t, err, "valid shared or restored evidence should remain capturable")
				require.Equal(t, models.CodeReviewVisualEvidenceSnapshot{Version: 1, AssessmentID: &assessmentID, RepositoryID: repoID, Repository: "acme/web", PullRequestNumber: 42, HeadSHA: head, CapturedAt: capturedAt, Complete: true, Evidence: []models.CodeReviewVisualEvidence{}}, actual, "shared evidence should retain the same snapshot contract as standalone discovery")
				require.Equal(t, 1, len(prompts.records), "freshness checks and checkpoint restoration must not replace or duplicate persisted evidence")
			}
			require.Equal(t, tt.callbackCalls, callbackCalls, "shared inventory should run only when discovery is required")
			require.Equal(t, tt.standaloneCalls, discoverer.callCount(), "shared discovery must bypass the standalone provider path")
		})
	}
}
