package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type mockInternalEvalSessionStore struct {
	session models.Session
	err     error
}

func TestBuildEvalCandidateWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   integration.AddEvalCandidateParams
		expected []string
	}{
		{
			name: "detects weak candidate signals",
			params: integration.AddEvalCandidateParams{
				PRNumber:          42,
				PRTitle:           "Update docs",
				BaseCommitSHA:     "abc123",
				SolutionCommitSHA: "def456",
				SolutionDiff:      "diff --git a/README.md b/README.md\n+notes\n",
				IssueDescription:  "fix",
				ScoringCriteria:   json.RawMessage(`[{"name":"judge","grader_type":"llm_judge","weight":1}]`),
				Complexity:        string(models.EvalComplexitySimple),
				FitnessScore:      0.5,
				FitnessReasoning:  "Small but real.",
				Evidence:          json.RawMessage(`{"changed_files":["README.md"],"test_commands":[]}`),
			},
			expected: []string{"missing_deterministic_check", "missing_test_command", "docs_only", "weak_prompt"},
		},
		{
			name: "keeps caller warnings and appends deterministic warnings once",
			params: integration.AddEvalCandidateParams{
				PRNumber:          43,
				PRTitle:           "Fix test",
				BaseCommitSHA:     "abc123",
				SolutionCommitSHA: "def456",
				SolutionDiff:      "diff --git a/app.go b/app.go\n+fix\n",
				IssueDescription:  "Fix the timeout in checkout handling by making the retry path deterministic.",
				ScoringCriteria:   json.RawMessage(`[{"name":"unit","grader_type":"code_check","grader_config":{"command":"sleep 1 && go test ./..."},"weight":1}]`),
				Complexity:        string(models.EvalComplexityModerate),
				FitnessScore:      0.9,
				FitnessReasoning:  "Clear regression.",
				Warnings:          []string{"custom_warning"},
			},
			expected: []string{"custom_warning", "flaky_command_pattern"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := buildEvalCandidateWarnings(tt.params)

			require.Equal(t, tt.expected, actual, "candidate warnings should be deterministic and compact")
		})
	}
}

func TestChangedFilesFromDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		diff     string
		expected []string
	}{
		{
			name:     "simple path",
			diff:     "diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "path with spaces",
			diff:     "diff --git a/my code/main.go b/my code/main.go\n--- a/my code/main.go",
			expected: []string{"my code/main.go"},
		},
		{
			name:     "path containing ' b/' substring",
			diff:     "diff --git a/a b/file.go b/a b/file.go\n--- a/a b/file.go",
			expected: []string{"a b/file.go"},
		},
		{
			name:     "multiple files",
			diff:     "diff --git a/foo.go b/foo.go\ndiff --git a/bar.go b/bar.go",
			expected: []string{"foo.go", "bar.go"},
		},
		{
			name:     "empty diff",
			diff:     "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := changedFilesFromDiff(tt.diff)
			require.Equal(t, tt.expected, got, "changedFilesFromDiff should extract correct paths")
		})
	}
}

func (m mockInternalEvalSessionStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return m.session, m.err
}

func TestInternalEvalHandler_AddCandidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		origin       models.SessionOrigin
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, repoID, sessionID, threadID, bootstrapRunID uuid.UUID)
		expectedCode int
	}{
		{
			name:   "adds candidate for eval bootstrap session",
			origin: models.SessionOriginEvalBootstrap,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, sessionID, threadID, bootstrapRunID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE org_id = @org_id AND session_id = @session_id AND thread_id = @thread_id").
					WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "repo_id", "status", "candidates", "session_id",
						"thread_id", "created_by", "created_at", "completed_at", "error_message",
					}).AddRow(bootstrapRunID, orgID, repoID, string(models.EvalBootstrapStatusRunning), nil, &sessionID, &threadID, nil, now, nil, nil))
				mock.ExpectQuery("INSERT INTO eval_bootstrap_candidates").
					WithArgs(anyArgs(17)...).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "repo_id", "candidate_index", "status", "created_at"}).
						AddRow(uuid.New(), sessionID, &threadID, repoID, 0, string(models.EvalBootstrapCandidateStatusProposed), now))
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:         "rejects normal sessions",
			origin:       models.SessionOriginManual,
			setupMock:    func(pgxmock.PgxPoolIface, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {},
			expectedCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			sessionID := uuid.New()
			threadID := uuid.New()
			bootstrapRunID := uuid.New()
			secret := "internal-eval-test-secret"
			token, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, &threadID, []string{"eval:add"}, string(models.SessionOriginEvalBootstrap), &bootstrapRunID, time.Minute)
			require.NoError(t, err, "GenerateSessionThreadToken should create a scoped sandbox token")

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			tt.setupMock(mock, orgID, repoID, sessionID, threadID, bootstrapRunID)

			handler := NewInternalEvalHandler(
				db.NewEvalBootstrapStore(mock),
				mockInternalEvalSessionStore{session: models.Session{
					ID:           sessionID,
					OrgID:        orgID,
					Origin:       tt.origin,
					RepositoryID: &repoID,
				}},
				secret,
				zerolog.Nop(),
			)
			body := []byte(`{"pr_number":42,"pr_title":"Fix checkout timeout","base_commit_sha":"abc123","solution_commit_sha":"def456","solution_diff":"diff --git","issue_description":"Fix checkout timeout","scoring_criteria":[{"name":"fixes-timeout","grader_type":"llm_judge","description":"Checkout no longer times out"}],"complexity":"moderate","fitness_score":0.9,"fitness_reasoning":"Real regression"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/eval/candidates", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()

			handler.AddCandidate(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "AddCandidate should return the expected status code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
