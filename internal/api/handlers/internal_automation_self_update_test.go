package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestInternalAutomationHandler_PersonalBaseUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		body                         string
		origin                       models.SessionOrigin
		unbound, readOnly, crossRepo bool
		wantStatus                   int
	}{
		{name: "own base", body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusOK},
		{name: "no attempt", unbound: true, body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "manual session", origin: models.SessionOriginManual, body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "goal improvement", origin: models.SessionOriginAutomationGoalImprovement, body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "read grant", readOnly: true, body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "cross repo", crossRepo: true, body: `{"base_branch":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "identity", body: `{"base_branch":"stack/parent","identity_scope":"org"}`, wantStatus: http.StatusForbidden},
		{name: "capabilities", body: `{"base_branch":"stack/parent","capabilities":[]}`, wantStatus: http.StatusForbidden},
		{name: "schedule", body: `{"base_branch":"stack/parent","schedule_type":"none"}`, wantStatus: http.StatusForbidden},
		{name: "null extra field", body: `{"base_branch":"stack/parent","goal":null}`, wantStatus: http.StatusForbidden},
		{name: "unknown field", body: `{"base_branch":"stack/parent","unknown":true}`, wantStatus: http.StatusForbidden},
		{name: "case alias", body: `{"BASE_BRANCH":"stack/parent"}`, wantStatus: http.StatusForbidden},
		{name: "null base", body: `{"base_branch":null}`, wantStatus: http.StatusForbidden},
		{name: "empty base", body: `{"base_branch":""}`, wantStatus: http.StatusForbidden},
		{name: "missing base", body: `{}`, wantStatus: http.StatusForbidden},
		{name: "null object", body: `null`, wantStatus: http.StatusForbidden},
		{name: "invalid base", body: `{"base_branch":"--upload-pack=evil"}`, wantStatus: http.StatusBadRequest},
		{name: "malformed body", body: `{`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actor := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), JobID: uuid.New(), AttemptToken: uuid.New()}
			automationID := uuid.New()
			origin := tt.origin
			if origin == "" {
				origin = models.SessionOriginAutomation
			}
			access := models.AgentCapabilityAccessWrite
			if tt.readOnly {
				access = models.AgentCapabilityAccessRead
			}
			session := models.Session{ID: actor.SessionID, OrgID: actor.OrgID, RepositoryID: &actor.RepositoryID, Origin: origin,
				CapabilitySnapshot: automationManagementSnapshot(access)}
			repoID := actor.RepositoryID
			if tt.crossRepo {
				repoID = uuid.New()
			}
			delegate := &fakeInternalAutomationDelegate{}
			h := NewInternalAutomationHandler(delegate, fakeInternalSessionLookup{session: session}, fakeInternalAutomationLookup{automation: models.Automation{
				ID: automationID, OrgID: actor.OrgID, RepositoryID: &repoID, IdentityScope: models.AutomationIdentityScopePersonal}}, internalAutomationSecret)
			token, err := auth.GenerateAutomationActionToken(internalAutomationSecret, actor, nil, string(origin), time.Minute)
			require.NoError(t, err, "generate bound automation token")
			if tt.unbound {
				token = newInternalAutomationToken(t, actor.OrgID, actor.RepositoryID, actor.SessionID, origin)
			}
			req := newInternalAutomationRequest(t, http.MethodPatch, "/automations/"+automationID.String(), token, tt.body, map[string]string{"id": automationID.String()})
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "enforce Personal self-update boundary: %s", rr.Body.String())
			require.False(t, delegate.updateCalled, "Personal updates never enter the general update handler")
			require.Equal(t, tt.wantStatus == http.StatusOK, delegate.selfUpdateCalled, "only a valid base-only request reaches the fenced writer")
			if tt.wantStatus == http.StatusOK {
				require.Equal(t, actor, delegate.gotActor, "carry every signed attempt claim to the database writer")
				require.Equal(t, automationID, delegate.gotAutomationID, "authorize the requested automation in the database")
				require.Equal(t, actor.OrgID, delegate.gotOrgID, "preserve tenant scope for audit")
				require.Equal(t, "stack/parent", delegate.gotBaseBranch, "forward only the requested branch")
			}
		})
	}
}

func TestInternalAutomationHandler_PersonalBasePersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"authorized write", nil, http.StatusOK, ""},
		{"database rejects authority", pgx.ErrNoRows, http.StatusForbidden, "IDENTITY_SCOPE_FORBIDDEN"},
		{"database unavailable", errors.New("connection lost"), http.StatusInternalServerError, "UPDATE_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated store mock")
			t.Cleanup(mock.Close)
			actor := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), JobID: uuid.New(), AttemptToken: uuid.New()}
			automation := models.Automation{ID: uuid.New(), OrgID: actor.OrgID, RepositoryID: &actor.RepositoryID,
				IdentityScope: models.AutomationIdentityScopePersonal, BaseBranch: "main"}
			args := pgx.NamedArgs{"org_id": actor.OrgID, "id": automation.ID}
			mock.ExpectQuery("SELECT .* FROM automations").WithArgs(args).WillReturnRows(newAutomationRow(mock, automation))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM automations.*FOR UPDATE").WithArgs(args).WillReturnRows(newAutomationRow(mock, automation))
			q := mock.ExpectQuery("UPDATE automations a SET base_branch").WithArgs(actor.OrgID, automation.ID, actor.RepositoryID, actor.RunID,
				actor.ThreadID, actor.SessionID, actor.JobID, "stack/parent", actor.AttemptToken)
			if tt.err != nil {
				q.WillReturnError(tt.err)
				mock.ExpectRollback()
			} else {
				updated := automation
				updated.BaseBranch = "stack/parent"
				q.WillReturnRows(newAutomationRow(mock, updated))
				mock.ExpectCommit()
			}
			store := db.NewAutomationStore(mock)
			h := NewInternalAutomationHandler(NewAutomationHandler(store, nil), fakeInternalSessionLookup{session: models.Session{ID: actor.SessionID,
				RepositoryID: &actor.RepositoryID, OrgID: actor.OrgID, Origin: models.SessionOriginAutomation,
				CapabilitySnapshot: automationManagementSnapshot(models.AgentCapabilityAccessWrite)}}, store, internalAutomationSecret)
			token, err := auth.GenerateAutomationActionToken(internalAutomationSecret, actor, nil, "automation", time.Minute)
			require.NoError(t, err, "generate signed attempt")
			req := newInternalAutomationRequest(t, http.MethodPatch, "/automations/"+automation.ID.String(), token, `{"base_branch":"stack/parent"}`, map[string]string{"id": automation.ID.String()})
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			require.Equal(t, tt.status, rr.Code, "return the durable authorization outcome: %s", rr.Body.String())
			if tt.code != "" {
				require.Contains(t, rr.Body.String(), `"code":"`+tt.code+`"`, "use the stable failure code")
			} else {
				var result models.SingleResponse[models.Automation]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result), "decode updated automation")
				require.Equal(t, "stack/parent", result.Data.BaseBranch, "return the committed branch")
				require.Equal(t, models.AutomationIdentityScopePersonal, result.Data.IdentityScope, "retain Personal identity")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "use the atomic branch-only writer")
		})
	}
}

func TestInternalAutomationHandler_IdentityActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		identity models.AutomationIdentityScope
		call     func(*InternalAutomationHandler, http.ResponseWriter, *http.Request)
		status   int
	}{
		{"org update", models.AutomationIdentityScopeOrg, (*InternalAutomationHandler).Update, http.StatusOK},
		{"personal run", models.AutomationIdentityScopePersonal, (*InternalAutomationHandler).RunNow, http.StatusForbidden},
		{"personal pause", models.AutomationIdentityScopePersonal, (*InternalAutomationHandler).Pause, http.StatusForbidden},
		{"personal resume", models.AutomationIdentityScopePersonal, (*InternalAutomationHandler).Resume, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actor := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), JobID: uuid.New(), AttemptToken: uuid.New()}
			id := uuid.New()
			h := NewInternalAutomationHandler(&fakeInternalAutomationDelegate{}, fakeInternalSessionLookup{session: models.Session{ID: actor.SessionID,
				RepositoryID: &actor.RepositoryID, OrgID: actor.OrgID, Origin: models.SessionOriginAutomation, CapabilitySnapshot: automationManagementSnapshot(models.AgentCapabilityAccessWrite)}},
				fakeInternalAutomationLookup{automation: models.Automation{ID: id, RepositoryID: &actor.RepositoryID, IdentityScope: tt.identity}}, internalAutomationSecret)
			token, err := auth.GenerateAutomationActionToken(internalAutomationSecret, actor, nil, "automation", time.Minute)
			require.NoError(t, err, "generate executing attempt token")
			req := newInternalAutomationRequest(t, http.MethodPatch, "/automations/"+id.String(), token, `{"goal":"updated"}`, map[string]string{"id": id.String()})
			rr := httptest.NewRecorder()
			tt.call(h, rr, req)
			require.Equal(t, tt.status, rr.Code, "the Personal exception must not widen other actions")
		})
	}
}
