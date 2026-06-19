package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestInternalProjectHandler_Propose_RejectsDisconnectedRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	secret := "test-secret-32-chars-long-enough-xxx"
	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	// Return an org-matching repo with disconnected status so the IsActive
	// guard fires — the whole point of this test.
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "org/repo", "main",
				false, nil, nil, "https://github.com/org/repo.git", int64(12345), "disconnected",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	handler := NewInternalProjectHandler(
		nil,
		db.NewProjectStore(mock),
		db.NewProjectTaskStore(mock),
		db.NewRepositoryStore(mock),
		secret,
		zerolog.Nop(),
	)

	token, err := auth.GenerateInternalToken(secret, orgID, repoID, 5*time.Minute)
	require.NoError(t, err)

	body, err := json.Marshal(map[string]any{
		"repository_id": repoID.String(),
		"title":         "T",
		"goal":          "G",
		"reasoning":     "R",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/projects/propose", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.Propose(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "REPO_DISCONNECTED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalProjectHandler_Propose_AutomationGoalImprovementTokenRejected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	secret := "test-secret-32-chars-long-enough-xxx"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	handler := NewInternalProjectHandler(
		nil,
		db.NewProjectStore(mock),
		db.NewProjectTaskStore(mock),
		db.NewRepositoryStore(mock),
		secret,
		zerolog.Nop(),
	)

	token, err := auth.GenerateSessionThreadTokenWithClaims(
		secret,
		orgID,
		repoID,
		sessionID,
		nil,
		[]string{"automation-goal-improvement:complete"},
		string(models.SessionOriginAutomationGoalImprovement),
		nil,
		5*time.Minute,
	)
	require.NoError(t, err, "automation goal improvement token should be generated")

	body, err := json.Marshal(map[string]any{
		"repository_id": repoID.String(),
		"title":         "T",
		"goal":          "G",
		"reasoning":     "R",
	})
	require.NoError(t, err, "request body should marshal")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/projects/propose", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.Propose(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "goal improvement sessions should not be allowed to propose projects")
	require.Contains(t, rr.Body.String(), "TOOL_NOT_AVAILABLE", "response should explain the tool is unavailable")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made")
}
