package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
)

func TestInternalPRReviewerMetadataResponse(t *testing.T) {
	t.Parallel()
	sessionID := uuid.New()
	coordinator := &internalPRCoordinatorStub{result: &publicationintent.PublicationIntentResult{Status: publicationintent.ResultAwaitingAutomaticPublication, SessionID: sessionID}}
	handler := &InternalPullRequestHandler{coordinator: coordinator, logger: zerolog.Nop()}
	rr := httptest.NewRecorder()
	handler.createWithCoordinator(rr, httptest.NewRequest(http.MethodPost, "/api/v1/internal/session/pr", nil), uuid.New(), sessionID, internalCreatePullRequestRequest{TeamReviewers: []string{"acme/team"}})
	require.Equal(t, http.StatusAccepted, rr.Code, "metadata-only requests should preserve the accepted response contract")
	var response internalCreatePullRequestResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "typed metadata response should decode")
	require.Equal(t, internalCreatePullRequestResponse{Status: "awaiting_automatic_publication", SessionID: sessionID.String()}, response, "staging must not pretend a publication or review job already exists")
	require.Equal(t, []string{"acme/team"}, coordinator.requested.TeamReviewers, "handler must forward explicit teams to server-owned authority checks")
}

func TestInternalPRTeamMetadataRejectsPerTargetToken(t *testing.T) {
	t.Parallel()
	orgID, repoID, sessionID := uuid.New(), uuid.New(), uuid.New()
	token, err := auth.GenerateSessionThreadTokenWithClaims(prHandlerSecret, orgID, repoID, sessionID, nil, models.PerTargetToolScopes(), string(models.SessionOriginAutomation), nil, time.Minute)
	require.NoError(t, err, "test should sign a per-target sandbox token")
	handler := &InternalPullRequestHandler{signingSecret: prHandlerSecret, logger: zerolog.Nop()}
	rr := httptest.NewRecorder()
	handler.Create(rr, newPRHandlerRequest(t, token, sessionID.String(), map[string]any{"team_reviewers": []string{"acme/team"}}))
	require.Equal(t, http.StatusForbidden, rr.Code, "restricted per-target token must be denied before database or coordinator access")
	require.Contains(t, rr.Body.String(), "TOOL_NOT_AVAILABLE", "per-target denial should retain the established tool fence")
}
