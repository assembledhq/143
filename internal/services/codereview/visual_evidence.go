package codereview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

const (
	visualEvidenceSnapshotVersion = 1
	visualEvidencePromptRole      = "visual_evidence"
	visualEvidenceMaxImages       = 32
	visualEvidenceMaxTotalBytes   = 64 << 20
	visualEvidenceConcurrency     = 4
)

var visualEvidenceHeadSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
var visualEvidenceContentSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

type VisualEvidenceDiscoverer interface {
	DiscoverCodeReviewVisualEvidence(ctx context.Context, orgID, repositoryID uuid.UUID, number int) (models.CodeReviewVisualEvidenceDiscovery, error)
}

type VisualEvidencePromptStore interface {
	GetPromptRecordByKey(ctx context.Context, orgID uuid.UUID, recordKey string) (models.CodeReviewPromptRecord, error)
	CreatePromptRecordIfAbsent(ctx context.Context, record *models.CodeReviewPromptRecord) (bool, error)
}

type VisualEvidenceRepositoryStore interface {
	GetByID(ctx context.Context, orgID, repositoryID uuid.UUID) (models.Repository, error)
}

type VisualEvidenceService struct {
	discoverer VisualEvidenceDiscoverer
	prompts    VisualEvidencePromptStore
	repos      VisualEvidenceRepositoryStore
	tokens     InstallationTokenProvider
	uploads    storage.UploadStore
	downloader *visualEvidenceDownloader
	logger     zerolog.Logger
	maxImages  int
	maxBytes   int64
	concurrent int
}

type CaptureVisualEvidenceInput struct {
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	RepositoryID      uuid.UUID
	PullRequestNumber int
	HeadSHA           string
}

func NewVisualEvidenceService(
	discoverer VisualEvidenceDiscoverer,
	prompts VisualEvidencePromptStore,
	repos VisualEvidenceRepositoryStore,
	tokens InstallationTokenProvider,
	uploads storage.UploadStore,
	logger zerolog.Logger,
) *VisualEvidenceService {
	return &VisualEvidenceService{
		discoverer: discoverer,
		prompts:    prompts,
		repos:      repos,
		tokens:     tokens,
		uploads:    uploads,
		downloader: newVisualEvidenceDownloader(),
		logger:     logger,
		maxImages:  visualEvidenceMaxImages,
		maxBytes:   visualEvidenceMaxTotalBytes,
		concurrent: visualEvidenceConcurrency,
	}
}

func (s *VisualEvidenceService) Capture(ctx context.Context, input CaptureVisualEvidenceInput) (models.CodeReviewVisualEvidenceSnapshot, error) {
	input.HeadSHA = strings.ToLower(strings.TrimSpace(input.HeadSHA))
	if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || input.RepositoryID == uuid.Nil || input.PullRequestNumber <= 0 || !visualEvidenceHeadSHA.MatchString(input.HeadSHA) {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("org_id, session_id, repository_id, positive pull request number, and a full Git head SHA are required")
	}
	if s == nil || s.discoverer == nil || s.prompts == nil || s.repos == nil || s.uploads == nil || s.downloader == nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("code review visual evidence service is not configured")
	}
	if s.maxImages <= 0 || s.maxBytes <= 0 || s.concurrent <= 0 || s.concurrent > visualEvidenceConcurrency {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("code review visual evidence resource limits are invalid")
	}

	recordKey := visualEvidenceRecordKey(input.SessionID, input.HeadSHA)
	record, err := s.prompts.GetPromptRecordByKey(ctx, input.OrgID, recordKey)
	if err == nil {
		restored, restoreErr := restoreVisualEvidenceSnapshot(record, input)
		if restoreErr == nil {
			metrics.RecordCodeReviewVisualEvidenceCapture(ctx, len(restored.Evidence), len(restored.Evidence), restored.Complete, restored.Overflow, true)
		}
		return restored, restoreErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("load code review visual evidence checkpoint: %w", err)
	}

	discovery, err := s.discoverer.DiscoverCodeReviewVisualEvidence(ctx, input.OrgID, input.RepositoryID, input.PullRequestNumber)
	if err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("discover code review visual evidence: %w", err)
	}
	if err := validateVisualEvidenceDiscovery(discovery, input); err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, err
	}

	token := ""
	if discoveryNeedsGitHubAssetToken(discovery) {
		repository, repoErr := s.repos.GetByID(ctx, input.OrgID, input.RepositoryID)
		if repoErr != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("load repository for private visual evidence: %w", repoErr)
		}
		if repository.InstallationID == 0 || s.tokens == nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("GitHub installation auth is unavailable for visual evidence")
		}
		token, err = s.tokens.GetInstallationToken(ctx, repository.InstallationID)
		if err != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("load GitHub installation token for visual evidence: %w", err)
		}
	}

	snapshot := s.materialize(ctx, input, discovery, token)
	metadata, err := json.Marshal(snapshot)
	if err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("encode code review visual evidence manifest: %w", err)
	}
	record = models.CodeReviewPromptRecord{
		OrgID: input.OrgID, SessionID: input.SessionID, RecordKey: recordKey, Role: visualEvidencePromptRole,
		Content: visualEvidenceSummary(snapshot), Metadata: metadata,
	}
	created, err := s.prompts.CreatePromptRecordIfAbsent(ctx, &record)
	if err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("persist code review visual evidence checkpoint: %w", err)
	}
	persisted, err := restoreVisualEvidenceSnapshot(record, input)
	if err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, err
	}
	metrics.RecordCodeReviewVisualEvidenceCapture(ctx, len(discovery.Sources), len(persisted.Evidence), persisted.Complete, persisted.Overflow, !created)
	return persisted, nil
}

func validateVisualEvidenceDiscovery(discovery models.CodeReviewVisualEvidenceDiscovery, input CaptureVisualEvidenceInput) error {
	if discovery.Version != visualEvidenceSnapshotVersion || discovery.RepositoryID != input.RepositoryID ||
		discovery.PullRequestNumber != input.PullRequestNumber || strings.TrimSpace(discovery.Repository) == "" || discovery.CapturedAt.IsZero() {
		return fmt.Errorf("visual evidence discovery identity is invalid")
	}
	if !strings.EqualFold(strings.TrimSpace(discovery.HeadSHA), input.HeadSHA) {
		return fmt.Errorf("visual evidence head changed during capture")
	}
	seenSourceIDs := make(map[string]struct{}, len(discovery.Sources))
	for _, source := range discovery.Sources {
		if strings.TrimSpace(source.SourceID) == "" || strings.TrimSpace(source.ProviderObjectID) == "" || strings.TrimSpace(source.ImageURL) == "" || source.ImageIndex <= 0 || !source.Untrusted {
			return fmt.Errorf("visual evidence discovery contains invalid or trusted source metadata")
		}
		if _, duplicate := seenSourceIDs[source.SourceID]; duplicate {
			return fmt.Errorf("visual evidence discovery contains duplicate source IDs")
		}
		seenSourceIDs[source.SourceID] = struct{}{}
		if err := source.Surface.Validate(); err != nil {
			return err
		}
		if err := source.AuthorType.Validate(); err != nil {
			return err
		}
		if source.Surface != models.CodeReviewEvidenceSurfaceDescription && !source.AuthorType.IsHuman() {
			return fmt.Errorf("visual evidence discovery contains non-human discussion content")
		}
		if source.Surface != models.CodeReviewEvidenceSurfaceDescription && strings.TrimSpace(source.AuthorLogin) == "" {
			return fmt.Errorf("visual evidence discovery contains discussion content without a human author")
		}
	}
	return nil
}

func (s *VisualEvidenceService) materialize(ctx context.Context, input CaptureVisualEvidenceInput, discovery models.CodeReviewVisualEvidenceDiscovery, token string) models.CodeReviewVisualEvidenceSnapshot {
	snapshot := models.CodeReviewVisualEvidenceSnapshot{
		Version: visualEvidenceSnapshotVersion, RepositoryID: input.RepositoryID, Repository: discovery.Repository,
		PullRequestNumber: input.PullRequestNumber, HeadSHA: input.HeadSHA, CapturedAt: discovery.CapturedAt,
		Complete: true, Overflow: len(discovery.Sources) > s.maxImages,
		Evidence: make([]models.CodeReviewVisualEvidence, 0, len(discovery.Sources)),
	}

	eligibleCount := len(discovery.Sources)
	if eligibleCount > s.maxImages {
		eligibleCount = s.maxImages
	}
	uniqueURLs := make([]string, 0, eligibleCount)
	urlIndexes := make(map[string]int, eligibleCount)
	firstSourceIndexes := make([]int, 0, eligibleCount)
	for index := 0; index < eligibleCount; index++ {
		originalURL := strings.TrimSpace(discovery.Sources[index].ImageURL)
		if _, exists := urlIndexes[originalURL]; exists {
			continue
		}
		urlIndexes[originalURL] = len(uniqueURLs)
		uniqueURLs = append(uniqueURLs, originalURL)
		firstSourceIndexes = append(firstSourceIndexes, index)
	}

	type storedContent struct {
		key        string
		url        string
		evidenceID string
	}
	type preparedURL struct {
		contentSHA256         string
		contentType           string
		byteSize              int64
		width                 int
		height                int
		status                models.CodeReviewVisualEvidenceFetchStatus
		failureReason         string
		hostClass             string
		duration              time.Duration
		storageKey            string
		storedURL             string
		firstEvidenceID       string
		duplicateOfEvidenceID string
	}
	preparedURLs := make([]preparedURL, len(uniqueURLs))
	storedByHash := make(map[string]storedContent)
	var acceptedBytes int64
	for batchStart := 0; batchStart < len(uniqueURLs); batchStart += s.concurrent {
		batchEnd := batchStart + s.concurrent
		if batchEnd > len(uniqueURLs) {
			batchEnd = len(uniqueURLs)
		}
		batchResults := make([]visualEvidenceFetchResult, batchEnd-batchStart)
		var waitGroup sync.WaitGroup
		for index := batchStart; index < batchEnd; index++ {
			index := index
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				batchResults[index-batchStart] = s.downloader.fetch(ctx, uniqueURLs[index], token)
			}()
		}
		waitGroup.Wait()

		for index := batchStart; index < batchEnd; index++ {
			result := batchResults[index-batchStart]
			firstSource := discovery.Sources[firstSourceIndexes[index]]
			prepared := preparedURL{
				contentSHA256: result.contentSHA256, contentType: result.contentType, byteSize: result.byteSize,
				width: result.width, height: result.height, status: result.status, failureReason: result.failureReason,
				hostClass: result.hostClass, duration: result.duration,
			}
			prepared.firstEvidenceID = visualEvidenceID(firstSource.SourceID, prepared.contentSHA256, prepared.status)
			if result.status == models.CodeReviewVisualEvidenceFetchStatusAvailable {
				if stored, duplicate := storedByHash[result.contentSHA256]; duplicate {
					prepared.storageKey = stored.key
					prepared.storedURL = stored.url
					prepared.duplicateOfEvidenceID = stored.evidenceID
				} else if acceptedBytes+int64(len(result.data)) > s.maxBytes {
					prepared.status = models.CodeReviewVisualEvidenceFetchStatusOverLimit
					prepared.failureReason = fmt.Sprintf("assessment exceeds the %d byte aggregate image limit", s.maxBytes)
					prepared.firstEvidenceID = visualEvidenceID(firstSource.SourceID, prepared.contentSHA256, prepared.status)
					snapshot.Overflow = true
				} else {
					extension := visualEvidenceExtension(result.contentType)
					key := fmt.Sprintf("%s/code-review-evidence/%s/%s%s", input.OrgID, input.SessionID, result.contentSHA256, extension)
					storedURL, saveErr := s.uploads.Save(ctx, key, bytes.NewReader(result.data), result.contentType)
					if saveErr != nil {
						s.logger.Warn().
							Str("org_id", input.OrgID.String()).
							Str("session_id", input.SessionID.String()).
							Str("source_id", firstSource.SourceID).
							Msg("failed to persist code review visual evidence in first-party storage")
						prepared.status = models.CodeReviewVisualEvidenceFetchStatusUnavailable
						prepared.failureReason = "first-party image storage is unavailable"
						prepared.firstEvidenceID = visualEvidenceID(firstSource.SourceID, prepared.contentSHA256, prepared.status)
					} else {
						prepared.storageKey = key
						prepared.storedURL = storedURL
						acceptedBytes += int64(len(result.data))
						storedByHash[result.contentSHA256] = storedContent{key: key, url: storedURL, evidenceID: prepared.firstEvidenceID}
					}
				}
			}
			preparedURLs[index] = prepared
		}
	}

	for index, source := range discovery.Sources {
		originalURL := strings.TrimSpace(source.ImageURL)
		if index >= s.maxImages {
			evidence := overLimitVisualEvidence(source, originalURL, fmt.Sprintf("assessment exceeds the %d image limit", s.maxImages))
			snapshot.Evidence = append(snapshot.Evidence, evidence)
			metrics.RecordCodeReviewVisualEvidenceImage(ctx, string(source.Surface), string(evidence.Status), visualEvidenceHostClass(originalURL), 0, 0)
			continue
		}

		prepared := preparedURLs[urlIndexes[originalURL]]
		evidence := models.CodeReviewVisualEvidence{
			Source: source, OriginalURL: originalURL, StorageKey: prepared.storageKey, StoredURL: prepared.storedURL,
			ContentSHA256: prepared.contentSHA256, ContentType: prepared.contentType, ByteSize: prepared.byteSize,
			Width: prepared.width, Height: prepared.height, Status: prepared.status, FailureReason: prepared.failureReason,
		}
		evidence.EvidenceID = visualEvidenceID(source.SourceID, evidence.ContentSHA256, evidence.Status)
		if firstSourceIndexes[urlIndexes[originalURL]] != index {
			evidence.DuplicateOfEvidenceID = prepared.firstEvidenceID
		} else {
			evidence.DuplicateOfEvidenceID = prepared.duplicateOfEvidenceID
		}
		snapshot.Evidence = append(snapshot.Evidence, evidence)
		metrics.RecordCodeReviewVisualEvidenceImage(ctx, string(source.Surface), string(evidence.Status), prepared.hostClass, evidence.ByteSize, prepared.duration.Seconds())
	}
	return snapshot
}

func visualEvidenceRecordKey(sessionID uuid.UUID, headSHA string) string {
	return fmt.Sprintf("code-review-prompts/%s/%s/visual-evidence-v1", sessionID, strings.ToLower(strings.TrimSpace(headSHA)))
}

func restoreVisualEvidenceSnapshot(record models.CodeReviewPromptRecord, input CaptureVisualEvidenceInput) (models.CodeReviewVisualEvidenceSnapshot, error) {
	if record.OrgID != input.OrgID || record.SessionID != input.SessionID || record.RecordKey != visualEvidenceRecordKey(input.SessionID, input.HeadSHA) || record.Role != visualEvidencePromptRole {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence checkpoint identity is invalid")
	}
	var snapshot models.CodeReviewVisualEvidenceSnapshot
	if err := json.Unmarshal(record.Metadata, &snapshot); err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("decode stored code review visual evidence checkpoint: %w", err)
	}
	if snapshot.Version != visualEvidenceSnapshotVersion || snapshot.RepositoryID != input.RepositoryID || snapshot.PullRequestNumber != input.PullRequestNumber || !strings.EqualFold(snapshot.HeadSHA, input.HeadSHA) || !snapshot.Complete {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest is incomplete or does not match the assessment")
	}
	seenIDs := make(map[string]struct{}, len(snapshot.Evidence))
	seenSourceIDs := make(map[string]struct{}, len(snapshot.Evidence))
	for _, evidence := range snapshot.Evidence {
		if strings.TrimSpace(evidence.EvidenceID) == "" || strings.TrimSpace(evidence.Source.SourceID) == "" || !evidence.Source.Untrusted {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains invalid evidence identity")
		}
		if _, duplicate := seenIDs[evidence.EvidenceID]; duplicate {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains duplicate evidence IDs")
		}
		seenIDs[evidence.EvidenceID] = struct{}{}
		if _, duplicate := seenSourceIDs[evidence.Source.SourceID]; duplicate {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains duplicate source IDs")
		}
		seenSourceIDs[evidence.Source.SourceID] = struct{}{}
		if err := evidence.Source.Surface.Validate(); err != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, err
		}
		if err := evidence.Source.AuthorType.Validate(); err != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, err
		}
		if err := evidence.Status.Validate(); err != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, err
		}
		if evidence.EvidenceID != visualEvidenceID(evidence.Source.SourceID, evidence.ContentSHA256, evidence.Status) {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains a non-canonical evidence ID")
		}
		if evidence.Status == models.CodeReviewVisualEvidenceFetchStatusAvailable &&
			(strings.TrimSpace(evidence.StorageKey) == "" || strings.TrimSpace(evidence.StoredURL) == "" || strings.TrimSpace(evidence.ContentSHA256) == "") {
			return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains an unavailable attachment marked available")
		}
		if evidence.Status == models.CodeReviewVisualEvidenceFetchStatusAvailable {
			expectedStoragePrefix := fmt.Sprintf("%s/code-review-evidence/%s/", input.OrgID, input.SessionID)
			if !strings.HasPrefix(evidence.StorageKey, expectedStoragePrefix) ||
				evidence.StoredURL != "/api/v1/uploads/files/"+evidence.StorageKey ||
				!visualEvidenceContentSHA.MatchString(evidence.ContentSHA256) || evidence.ByteSize <= 0 ||
				evidence.Width <= 0 || evidence.Height <= 0 {
				return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains an invalid first-party attachment")
			}
		}
		if evidence.DuplicateOfEvidenceID != "" {
			if evidence.DuplicateOfEvidenceID == evidence.EvidenceID {
				return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains a self-referential duplicate")
			}
			if _, exists := seenIDs[evidence.DuplicateOfEvidenceID]; !exists {
				return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("stored code review visual evidence manifest contains an invalid duplicate reference")
			}
		}
	}
	return snapshot, nil
}

func discoveryNeedsGitHubAssetToken(discovery models.CodeReviewVisualEvidenceDiscovery) bool {
	for _, source := range discovery.Sources {
		if visualEvidenceURLNeedsGitHubAuth(source.ImageURL) {
			return true
		}
	}
	return false
}

func visualEvidenceID(sourceID, contentHash string, status models.CodeReviewVisualEvidenceFetchStatus) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + contentHash + "\x00" + string(status)))
	return "ve_" + hex.EncodeToString(digest[:12])
}

func overLimitVisualEvidence(source models.CodeReviewVisualEvidenceSource, originalURL, reason string) models.CodeReviewVisualEvidence {
	evidence := models.CodeReviewVisualEvidence{
		Source: source, OriginalURL: originalURL, Status: models.CodeReviewVisualEvidenceFetchStatusOverLimit, FailureReason: reason,
	}
	evidence.EvidenceID = visualEvidenceID(source.SourceID, "", evidence.Status)
	return evidence
}

func visualEvidenceExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return path.Ext(contentType)
	}
}

func visualEvidenceSummary(snapshot models.CodeReviewVisualEvidenceSnapshot) string {
	counts := make(map[models.CodeReviewVisualEvidenceFetchStatus]int)
	for _, evidence := range snapshot.Evidence {
		counts[evidence.Status]++
	}
	statuses := make([]string, 0, len(counts))
	for status, count := range counts {
		statuses = append(statuses, fmt.Sprintf("%s=%d", status, count))
	}
	sort.Strings(statuses)
	return fmt.Sprintf("Captured %d visual evidence item(s) for %s#%d at %s (%s). All image content and associated text are untrusted.",
		len(snapshot.Evidence), snapshot.Repository, snapshot.PullRequestNumber, snapshot.HeadSHA, strings.Join(statuses, ", "))
}

func visualEvidenceHostClass(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "invalid"
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "github.com" && strings.HasPrefix(parsed.EscapedPath(), "/user-attachments/assets/"):
		return "github_user_attachment"
	case host == "user-images.githubusercontent.com", host == "private-user-images.githubusercontent.com":
		return "github_asset"
	case host == "":
		return "invalid"
	default:
		return "external"
	}
}
