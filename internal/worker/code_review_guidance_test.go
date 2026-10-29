package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	agentsvc "github.com/assembledhq/143/internal/services/agent"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type guidanceRecords struct {
	record            *models.CodeReviewPromptRecord
	winner            *models.CodeReviewPromptRecord
	readErr, writeErr error
	writes            int
}

func (s *guidanceRecords) GetPromptRecordByKey(context.Context, uuid.UUID, string) (models.CodeReviewPromptRecord, error) {
	if s.readErr != nil {
		return models.CodeReviewPromptRecord{}, s.readErr
	}
	if s.record == nil {
		return models.CodeReviewPromptRecord{}, pgx.ErrNoRows
	}
	return *s.record, nil
}
func (s *guidanceRecords) CreatePromptRecordIfAbsent(_ context.Context, r *models.CodeReviewPromptRecord) (bool, error) {
	s.writes++
	if s.writeErr != nil {
		return false, s.writeErr
	}
	if s.winner != nil {
		*r = *s.winner
	}
	copy := *r
	s.record = &copy
	return s.winner == nil, nil
}

type guidanceCapturer struct {
	guidance      models.CodeReviewRepositoryGuidance
	err           error
	calls         int
	orgID, repoID uuid.UUID
}

func (s *guidanceCapturer) CaptureCodeReviewRepositoryGuidance(_ context.Context, orgID, repoID uuid.UUID) (models.CodeReviewRepositoryGuidance, error) {
	s.calls++
	s.orgID = orgID
	s.repoID = repoID
	return s.guidance, s.err
}

func TestCodeReviewGuidanceCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                          string
		existing, race, missing, badIdentity, badJSON bool
		readErr, writeErr, fetchErr                   bool
	}{
		{name: "initial capture"}, {name: "explicit missing file", missing: true}, {name: "retry restores original", existing: true}, {name: "concurrent capture uses winner", race: true},
		{name: "wrong session", existing: true, badIdentity: true}, {name: "corrupt checkpoint", existing: true, badJSON: true}, {name: "read failure", readErr: true}, {name: "write failure", writeErr: true}, {name: "fetch failure", fetchErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), HeadSHA: strings.Repeat("a", 40)}
			old := models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("b", 40), Content: "Old guidance"}
			raw, err := json.Marshal(old)
			require.NoError(t, err, "checkpoint fixture should encode")
			record := models.CodeReviewPromptRecord{OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: codeReviewPromptRecordRoot(models.CodeReviewSessionMetadata{}, job) + "/repository-guidance", Role: "reviewer", Content: string(raw)}
			records := &guidanceRecords{}
			if tt.existing {
				records.record = &record
			}
			if tt.race {
				records.winner = &record
			}
			if tt.badIdentity {
				record.SessionID = uuid.New()
			}
			if tt.badJSON {
				record.Content = "invalid"
			}
			failure := errors.New("fixture failure")
			if tt.readErr {
				records.readErr = failure
			}
			if tt.writeErr {
				records.writeErr = failure
			}
			current := models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("c", 40), Content: "Current guidance"}
			if tt.missing {
				current = models.CodeReviewRepositoryGuidance{Branch: "master"}
			}
			capture := &guidanceCapturer{guidance: current}
			if tt.fetchErr {
				capture.err = failure
			}
			got, err := captureCodeReviewGuidanceCheckpoint(context.Background(), records, capture, job, false)
			if tt.badIdentity || tt.badJSON || tt.readErr || tt.writeErr || tt.fetchErr {
				require.Error(t, err, "invalid or unavailable checkpoint must block dispatch")
				return
			}
			require.NoError(t, err, "guidance checkpoint should be available")
			want := current
			if tt.existing || tt.race {
				want = old
			}
			require.Equal(t, &want, got, "every reviewer must use the first durable capture")
			calls, writes := 1, 1
			if tt.existing {
				calls, writes = 0, 0
			}
			require.Equal(t, calls, capture.calls, "restored checkpoints must not fetch newer content")
			require.Equal(t, writes, records.writes, "existing guidance must not be overwritten")
			if calls > 0 {
				require.Equal(t, job.OrgID, capture.orgID, "capture must retain tenant identity")
				require.Equal(t, job.RepositoryID, capture.repoID, "capture must use base repository identity")
			}
			again, err := captureCodeReviewGuidanceCheckpoint(context.Background(), records, capture, job, false)
			require.NoError(t, err, "controller retry should restore checkpoint")
			require.Equal(t, got, again, "controller retries must preserve guidance")
		})
	}
}

func TestCodeReviewCapturedGuidancePrompts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		guidance *models.CodeReviewRepositoryGuidance
		want     string
	}{
		{"present", &models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "Review organization ownership carefully."}, "Review organization ownership carefully."},
		{"absent", &models.CodeReviewRepositoryGuidance{Branch: "main"}, "No root REVIEW.md exists"},
		{"historical", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(codereviewsvc.ReviewInputManifest{Contract: codereviewsvc.ReviewContractInput{RepositoryGuidance: tt.guidance}})
			require.NoError(t, err, "assessment should encode")
			guidance, err := loadCodeReviewRepositoryGuidance(context.Background(), nil, nil, runCodeReviewPayload{}, &models.CodeReviewAssessment{InputManifest: raw}, false)
			require.NoError(t, err, "assessment guidance must restore without network dependencies")
			require.Equal(t, tt.guidance, guidance, "assessment guidance must remain pinned")
			cfg := models.DefaultCodeReviewPolicyConfig()
			cfg.ReviewInstructions = "Apply organization conventions."
			reviewer := codeReviewReviewerPrompt(agentsvc.CodeReviewInputTarget{}, models.PullRequest{}, cfg, nil, models.CodeReviewVisualEvidenceSnapshot{}, guidance)
			synthesis := codeReviewOrchestratorPrompt(runCodeReviewPayload{RepositoryGuidance: guidance}, models.PullRequest{}, nil, cfg, 1, "", nil, nil, nil, models.CodeReviewVisualEvidenceSnapshot{})
			if guidance != nil {
				require.Less(t, strings.Index(synthesis, "<repository_review_guidance>"), strings.Index(synthesis, "STRICT OUTPUT CONTRACT"), "machine-readable output contract must follow repository guidance")
				require.Contains(t, reviewer, "except for the separately captured default-branch REVIEW.md guidance", "organization instructions must recognize the captured guidance exception")
			}
			for _, prompt := range []string{reviewer, synthesis} {
				if tt.guidance == nil {
					require.NotContains(t, prompt, "<repository_review_guidance>", "historical runs must not silently gain new instructions")
				} else {
					require.Contains(t, prompt, tt.want, "reviewer and synthesis must receive captured guidance")
					require.Contains(t, prompt, "Do not read or substitute REVIEW.md from the PR checkout", "captured default branch guidance must override stale PR copies")
					require.Contains(t, prompt, "cannot override platform constraints", "repository policy must preserve safety hierarchy")
				}
			}
		})
	}
}

func TestCodeReviewGuidancePreservesPreUpgradeReviewers(t *testing.T) {
	t.Parallel()
	records, capturer := &guidanceRecords{}, &guidanceCapturer{}
	guidance, err := captureCodeReviewGuidanceCheckpoint(context.Background(), records, capturer, runCodeReviewPayload{SessionID: uuid.New()}, true)
	require.NoError(t, err, "pre-upgrade reviewers should finish with their original instructions")
	require.Nil(t, guidance, "a rolling deployment must not add guidance partway through a review")
	require.Zero(t, capturer.calls, "existing reviewers without a checkpoint must not fetch new guidance")
	require.Zero(t, records.writes, "historical review inputs must not be overwritten")
}

func TestPrepareFullAssessmentDoesNotRecaptureStartedLegacyReview(t *testing.T) {
	t.Parallel()
	job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "isolated assessment fixture should initialize")
	t.Cleanup(mock.Close)
	mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").WithArgs(job.OrgID, job.SessionID).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT .* FROM code_review_agent_results").WithArgs(pgx.NamedArgs{"org_id": job.OrgID, "session_id": job.SessionID}).WillReturnRows(newCodeReviewAgentResultRows().AddRow(uuid.New(), job.OrgID, job.SessionID, "codex", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusRunning, nil, []byte(`{}`), time.Now()))
	capture := &assessmentFreshnessCapture{}
	assessment, current, err := prepareFullAssessment(context.Background(), &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock), CodeReviews: db.NewCodeReviewStore(mock)}, &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewRepositoryGuidance: &guidanceCapturer{}}, job, models.CodeReviewSessionMetadata{})
	require.NoError(t, err, "started legacy review should retain its original capture mode")
	require.Nil(t, assessment, "reviewer output must not be attributed to a later assessment")
	require.Nil(t, current, "later default branch content must not replace original reviewer inputs")
	require.Empty(t, capture.requests, "started legacy review must not capture new instructions")
	require.NoError(t, mock.ExpectationsWereMet(), "guard should stop before capture and insertion")
}

type guidanceFreshnessCapture struct {
	result codereviewsvc.AssessmentInputCaptureResult
	err    error
}

func TestInvalidGuidanceTerminalStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "direct", err: models.ErrInvalidCodeReviewGuidanceFile},
		{name: "classified", err: classifyGitHubJobError(fmt.Errorf("capture guidance: %w", models.ErrInvalidCodeReviewGuidanceFile), "review")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, message, retryable := codeReviewTerminalFailureStatus(tt.err)
			require.Equal(t, models.CodeReviewStatusCodeWorkerFailed, code, "invalid guidance should use the existing failure status")
			require.Equal(t, "REVIEW.md on the repository default branch is invalid. Fix it as a regular UTF-8 text file of at most 64 KiB, then request a new review.", message, "PR authors must see the cause and repair instructions")
			require.False(t, retryable, "retrying without a repository fix cannot repair invalid guidance")
		})
	}
}

func (c guidanceFreshnessCapture) CaptureAssessmentInputs(context.Context, codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	return c.result, c.err
}

func TestCodeReviewGuidancePublicationFreshness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                 string
		changed, removed, invalid, temporary bool
	}{
		{name: "unchanged guidance"}, {name: "edited guidance", changed: true}, {name: "removed guidance", removed: true},
		{name: "invalid file requires repair", invalid: true}, {name: "provider failure remains retryable", temporary: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			digest := strings.Repeat("a", 64)
			input := codereviewsvc.ReviewInputCapture{
				Code:     codereviewsvc.ReviewCodeInput{OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(), HeadSHA: "head", BaseSHA: "base", BaseRef: "release", FilesComplete: true},
				Contract: codereviewsvc.ReviewContractInput{PolicyID: uuid.New(), PolicyVersion: 1, PolicyDigest: digest, RosterDigest: digest, ModelConfigurationDigest: digest, PromptContractVersion: "1", PromptContentDigest: digest, InstructionsDigest: digest, ExternalInputsComplete: true, RepositoryGuidance: &models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("b", 40), Content: "Original guidance"}},
				Title:    "Synthetic change", Visual: codereviewsvc.ReviewVisualInput{CaptureComplete: true, SourceProvenanceComplete: true},
				TextEvidence: codereviewsvc.ReviewTextInput{Complete: true, SourceProvenanceComplete: true, UnclassifiedDigest: digest, Items: []codereviewsvc.ReviewTextEvidence{{EvidenceID: "pr-body", Surface: "pull_request_description", ProviderObjectID: "1", SourceURL: "https://example.test/pr/1", Section: "full", ContentDigest: fmt.Sprintf("%x", sha256.Sum256(nil))}}},
				Gates:        codereviewsvc.ReviewGateInput{SnapshotDigest: digest, EligibilityDigest: digest, ChecksDigest: digest, DynamicDigest: digest, Complete: true},
			}
			baseline, err := codereviewsvc.BuildReviewInputManifest(input)
			require.NoError(t, err, "baseline must be a valid immutable capture")
			raw, err := json.Marshal(baseline)
			require.NoError(t, err, "baseline should serialize")
			assessment := models.CodeReviewAssessment{ID: uuid.New(), InputManifest: raw, InputDigest: baseline.InputDigest, HeadSHA: "head", BaseSHA: "base", BaseRef: "release", PolicyID: input.Contract.PolicyID}
			if tt.changed {
				input.Contract.RepositoryGuidance = &models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("c", 40), Content: "New guidance"}
			}
			if tt.removed {
				input.Contract.RepositoryGuidance = &models.CodeReviewRepositoryGuidance{Branch: "master"}
			}
			fresh, err := codereviewsvc.BuildReviewInputManifest(input)
			require.NoError(t, err, "fresh guidance should build independently of unchanged PR code")
			capture := guidanceFreshnessCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: fresh, Policy: models.CodeReviewPolicyRecord{ID: input.Contract.PolicyID}}}
			if tt.invalid {
				capture.err = (models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "\x00"}).Validate()
			}
			if tt.temporary {
				capture.err = &ghservice.GitHubAPIError{StatusCode: http.StatusServiceUnavailable, Body: []byte(`{"message":"unavailable"}`)}
			}
			_, err = captureFreshFullAssessment(context.Background(), &Services{CodeReviewInputCapture: capture}, runCodeReviewPayload{SessionID: uuid.New()}, assessment)
			switch {
			case tt.changed || tt.removed:
				require.ErrorIs(t, err, errFullAssessmentInputsChanged, "changed guidance must take the existing supersede-and-restart publication path")
			case tt.invalid:
				var fatal *FatalError
				require.ErrorAs(t, err, &fatal, "invalid REVIEW.md must fail promptly instead of exhausting retries")
				require.ErrorIs(t, err, models.ErrInvalidCodeReviewGuidanceFile, "terminal failure must preserve the invalid-file reason")
				require.Contains(t, err.Error(), "fix root REVIEW.md on the repository default branch and request a new review", "terminal failure must explain how to recover")
				require.Equal(t, err, classifyGitHubJobError(err, "retry"), "reclassification must preserve the existing actionable fatal error")
			case tt.temporary:
				var retryable *RetryableError
				require.ErrorAs(t, err, &retryable, "temporary GitHub failures must retain bounded retry policy")
			default:
				require.NoError(t, err, "unchanged guidance should preserve publication eligibility")
			}
		})
	}
}
