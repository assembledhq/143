package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	agentsvc "github.com/assembledhq/143/internal/services/agent"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
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
			reviewer := codeReviewReviewerPrompt(agentsvc.CodeReviewInputTarget{}, models.PullRequest{}, cfg, nil, models.CodeReviewVisualEvidenceSnapshot{}, guidance)
			synthesis := codeReviewOrchestratorPrompt(runCodeReviewPayload{RepositoryGuidance: guidance}, models.PullRequest{}, nil, cfg, 1, "", nil, nil, nil, models.CodeReviewVisualEvidenceSnapshot{})
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
