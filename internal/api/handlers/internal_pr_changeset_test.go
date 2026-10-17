package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
)

func TestInternalPRCreateChangesetSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		changeset       string
		perTarget       bool
		threadMode      models.ThreadExecutionMode
		filesystemMode  models.ThreadFilesystemMode
		repoMismatch    bool
		sessionMismatch bool
		wantStatus      int
		wantError       string
		wantResult      publicationintent.ResultStatus
	}{
		{name: "running automation primary metadata", changeset: "primary", wantStatus: http.StatusAccepted, wantResult: publicationintent.ResultAwaitingAutomaticPublication},
		{name: "interactive child uses coordinator guards", changeset: "child", wantStatus: http.StatusAccepted, wantResult: publicationintent.ResultPRQueued},
		{name: "malformed changeset rejected", changeset: "bad-id", wantStatus: http.StatusBadRequest, wantError: "INVALID_ID"},
		{name: "zero changeset rejected", changeset: uuid.Nil.String(), wantStatus: http.StatusBadRequest, wantError: "INVALID_ID"},
		{name: "per-target denied", changeset: "primary", perTarget: true, wantStatus: http.StatusForbidden, wantError: "TOOL_NOT_AVAILABLE"},
		{name: "read-only thread denied", changeset: "primary", filesystemMode: models.ThreadFilesystemModeReadOnly, wantStatus: http.StatusForbidden, wantError: "TOOL_NOT_AVAILABLE"},
		{name: "review thread denied", changeset: "primary", threadMode: models.ThreadExecutionModeReview, wantStatus: http.StatusForbidden, wantError: "TOOL_NOT_AVAILABLE"},
		{name: "repository scope denied", changeset: "primary", repoMismatch: true, wantStatus: http.StatusForbidden, wantError: "REPO_MISMATCH"},
		{name: "body session scope denied", changeset: "primary", sessionMismatch: true, wantStatus: http.StatusBadRequest, wantError: "SESSION_MISMATCH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "request should have its own database mock")
			t.Cleanup(mock.Close)
			orgID, repoID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			origin := models.SessionOriginManual
			if tt.wantResult == publicationintent.ResultAwaitingAutomaticPublication || tt.perTarget {
				origin = models.SessionOriginAutomation
			}
			requestedID := tt.changeset
			if requestedID == "primary" || requestedID == "child" {
				requestedID = changesetID.String()
			}
			var threadID *uuid.UUID
			if tt.threadMode != "" || tt.filesystemMode != "" {
				id := uuid.New()
				threadID = &id
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE id .+ AND org_id").
					WithArgs(pgx.NamedArgs{"id": id, "org_id": orgID}).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "execution_mode", "filesystem_mode"}).AddRow(id, sessionID, orgID, tt.threadMode, tt.filesystemMode))
			} else if !tt.perTarget && !tt.sessionMismatch {
				row := sessionHandlerDetailRow(sessionID, orgID, time.Now())
				for index, column := range sessionColumns {
					switch column {
					case "repository_id":
						row[index] = &repoID
						if tt.repoMismatch {
							otherRepoID := uuid.New()
							row[index] = &otherRepoID
						}
					case "status":
						row[index] = models.SessionStatusRunning
					case "origin":
						row[index] = origin
					}
				}
				mock.ExpectQuery("SELECT .+ FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
					WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
			}
			var scopes []string
			if tt.perTarget {
				scopes = models.PerTargetToolScopes()
			}
			token, err := auth.GenerateSessionThreadTokenWithClaims(prHandlerSecret, orgID, repoID, sessionID, threadID, scopes, string(origin), nil, time.Minute)
			require.NoError(t, err, "request should use a signed session-scoped token")
			bodySessionID := sessionID
			if tt.sessionMismatch {
				bodySessionID = uuid.New()
			}
			body, err := json.Marshal(map[string]any{"session_id": bodySessionID.String(), "changeset_id": requestedID, "team_reviewers": []string{"acme/team"}, "trigger_kind": models.SessionPublicationTriggerExplicitAction})
			require.NoError(t, err, "changeset publication body should encode")
			request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session/pr", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			coordinator := &internalPRCoordinatorStub{result: &publicationintent.PublicationIntentResult{Status: tt.wantResult, SessionID: sessionID}}
			handler := newPRHandler(mock)
			handler.SetThreadStore(db.NewSessionThreadStore(mock))
			handler.SetPublicationIntentCoordinator(coordinator)
			rr := httptest.NewRecorder()

			handler.Create(rr, request)

			require.Equal(t, tt.wantStatus, rr.Code, "changeset selection must retain the canonical handler authorization contract: %s", rr.Body.String())
			if tt.wantError != "" {
				require.Contains(t, rr.Body.String(), tt.wantError, "rejected selection should explain its validation or authorization failure")
				require.Nil(t, coordinator.requested, "invalid or unauthorized selectors must never reach the coordinator")
			} else {
				require.Equal(t, &publicationintent.RequestPullRequest{ChangesetID: &changesetID, TeamReviewers: []string{"acme/team"}, Source: models.SessionPublicationSourceAgentTool, TriggerKind: models.SessionPublicationTriggerExplicitAction}, coordinator.requested, "canonical creation must forward the exact selector, metadata and explicit publish trigger without changing publication authority")
				var response internalCreatePullRequestResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "accepted selection should return typed publication JSON")
				require.Equal(t, internalCreatePullRequestResponse{Status: string(tt.wantResult), SessionID: sessionID.String()}, response, "metadata staging and interactive publication must retain honest distinct results")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "canonical authorization must use only the exact token-scoped repository and thread checks")
		})
	}
}

func TestInternalPRChangesetSelectionFailsClosedWithoutCoordinator(t *testing.T) {
	t.Parallel()
	changesetID := uuid.New()
	handler := &InternalPullRequestHandler{}
	rr := httptest.NewRecorder()

	handler.createWithCoordinator(rr, httptest.NewRequest(http.MethodPost, "/api/v1/internal/session/pr", nil), uuid.New(), uuid.New(), internalCreatePullRequestRequest{
		ChangesetID: changesetID.String(), TeamReviewers: []string{"acme/team"},
	})

	require.Equal(t, http.StatusInternalServerError, rr.Code, "an unavailable coordinator must not fall back to publishing the primary changeset")
	require.Contains(t, rr.Body.String(), "PUBLICATION_INTENT_FAILED", "unavailable publication authority must return the established failure code")
}
