package handlers

import (
	"encoding/json"
	"errors"
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

func TestInternalPRStagedTextForwarding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		body         string
		coordinator  func(sessionID uuid.UUID) *internalPRCoordinatorStub
		expectedCode int
		expectedErr  string
	}{
		{
			name: "staged text reaches the coordinator",
			body: `{"title":"feature-brief: Plan exports","body":"Source plan: docs/plans/exports.md"}`,
			coordinator: func(sessionID uuid.UUID) *internalPRCoordinatorStub {
				return &internalPRCoordinatorStub{result: &publicationintent.PublicationIntentResult{Status: publicationintent.ResultAwaitingAutomaticPublication, SessionID: sessionID}}
			},
			expectedCode: http.StatusAccepted,
		},
		{
			name: "rejected staged text is a client error",
			body: `{"title":"feature-brief: Plan exports"}`,
			coordinator: func(uuid.UUID) *internalPRCoordinatorStub {
				return &internalPRCoordinatorStub{err: &publicationintent.Error{Code: publicationintent.ErrorInvalidPublicationOptions, Err: errors.New("title and body are accepted only as automatic publication metadata")}}
			},
			expectedCode: http.StatusBadRequest,
			expectedErr:  string(publicationintent.ErrorInvalidPublicationOptions),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sessionID := uuid.New()
			var req internalCreatePullRequestRequest
			require.NoError(t, json.Unmarshal([]byte(tt.body), &req), "internal create request should decode staged text")
			coordinator := tt.coordinator(sessionID)
			handler := &InternalPullRequestHandler{coordinator: coordinator, logger: zerolog.Nop()}
			rr := httptest.NewRecorder()
			handler.createWithCoordinator(rr, httptest.NewRequest(http.MethodPost, "/api/v1/internal/session/pr", nil), uuid.New(), sessionID, req)
			require.Equal(t, tt.expectedCode, rr.Code, "handler should preserve the coordinator's typed outcome")
			require.Contains(t, rr.Body.String(), tt.expectedErr, "client errors should carry the coordinator code")
			require.NotNil(t, coordinator.requested, "handler should call the coordinator")
			require.Equal(t, req.Title, coordinator.requested.Title, "handler must forward the staged title unchanged")
			require.Equal(t, req.Body, coordinator.requested.Body, "handler must forward the staged body unchanged")
		})
	}
}
