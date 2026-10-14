package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type linearCoverageCredentialReader func(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error)

func (f linearCoverageCredentialReader) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	return f(ctx, orgID, provider)
}

type linearCoverageWriter struct {
	enable   func(uuid.UUID, bool) error
	settings func(uuid.UUID, models.LinearAgentSettings) error
}

func (w linearCoverageWriter) SetLinearAgentEnabled(_ context.Context, orgID uuid.UUID, enabled bool) error {
	return w.enable(orgID, enabled)
}
func (w linearCoverageWriter) SetLinearAgentSettings(_ context.Context, orgID uuid.UUID, settings models.LinearAgentSettings) error {
	return w.settings(orgID, settings)
}

func linearCoverageRequest(method, path, body string, orgID uuid.UUID) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	return r.WithContext(middleware.WithOrgID(r.Context(), orgID))
}

func requireLinearCoverageError(t *testing.T, rr *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	require.Equal(t, status, rr.Code, "handler should return the appropriate caller or dependency status")
	var actual models.ErrorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "failure response should use the standard JSON envelope")
	require.Equal(t, models.ErrorResponse{Error: models.ErrorDetail{Code: code, Message: message}}, actual, "failure response should expose only the stable public code and message")
}

func TestLinearAgentSettingsAllEndpointsRequireTenant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*LinearAgentSettingsHandler, http.ResponseWriter, *http.Request)
	}{
		{"status", (*LinearAgentSettingsHandler).GetStatus}, {"mapping list", (*LinearAgentSettingsHandler).ListMappings},
		{"mapping upsert", (*LinearAgentSettingsHandler).UpsertMapping}, {"mapping delete", (*LinearAgentSettingsHandler).DeleteMapping},
		{"settings patch", (*LinearAgentSettingsHandler).PatchSettings}, {"session list", (*LinearAgentSettingsHandler).ListSessions}, {"session detail", (*LinearAgentSettingsHandler).GetSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "tenant guard test should create an isolated mock")
			t.Cleanup(mock.Close)
			writer := linearCoverageWriter{enable: func(uuid.UUID, bool) error { t.Fatal("missing tenant must not mutate settings"); return nil }, settings: func(uuid.UUID, models.LinearAgentSettings) error {
				t.Fatal("missing tenant must not mutate settings")
				return nil
			}}
			h := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{Mappings: db.NewLinearTeamRepoMappingStore(mock), AgentSessions: db.NewLinearAgentSessionStore(mock), Activities: db.NewLinearAgentActivityLogStore(mock), Orgs: writer})
			rr := httptest.NewRecorder()
			tt.call(h, rr, httptest.NewRequest(http.MethodGet, "/", nil))
			requireLinearCoverageError(t, rr, http.StatusUnauthorized, "UNAUTHORIZED", "org context missing")
			require.NoError(t, mock.ExpectationsWereMet(), "missing org context must stop before every database operation")
		})
	}
}

func TestLinearAgentSettingsStatusCredentialAndFallbackContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                      string
		config                                                    models.ProviderConfig
		credentialNil, credentialError, settingsError, teamsError bool
		expected                                                  LinearAgentInstallStatus
	}{
		{name: "agent install", config: models.LinearConfig{Scope: "app:assignable,app:mentionable", AppUserName: "143"}, expected: LinearAgentInstallStatus{Enabled: true, HasLinearIntegration: true, AgentScopesGranted: true, AppUserName: "143"}},
		{name: "legacy install", config: models.LinearConfig{Scope: "read"}, expected: LinearAgentInstallStatus{Enabled: true, HasLinearIntegration: true}},
		{name: "unrecognized config", config: models.OpenAIConfig{}, expected: LinearAgentInstallStatus{Enabled: true, HasLinearIntegration: true}},
		{name: "missing credential", credentialNil: true, expected: LinearAgentInstallStatus{Enabled: true}},
		{name: "credential unavailable", credentialError: true, expected: LinearAgentInstallStatus{Enabled: true}},
		{name: "settings unavailable", credentialNil: true, settingsError: true},
		{name: "teams unavailable", credentialNil: true, teamsError: true, expected: LinearAgentInstallStatus{Enabled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			enabled := true
			teams := []db.LinearTeamKey{{OrgID: orgID, TeamID: "LT1", TeamKey: "API", TeamName: "Platform"}}
			fault := errors.New("private dependency diagnostic")
			h := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{
				Credentials: linearCoverageCredentialReader(func(_ context.Context, gotOrg uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
					require.Equal(t, orgID, gotOrg, "credential lookup should use the request tenant")
					require.Equal(t, models.ProviderLinear, provider, "status should request only Linear credentials")
					if tt.credentialError {
						return nil, fault
					}
					if tt.credentialNil {
						return nil, nil
					}
					return &models.DecryptedCredential{Config: tt.config}, nil
				}),
				Settings: linearAgentSettingsLoaderFunc(func(_ context.Context, gotOrg uuid.UUID) (models.LinearAgentSettings, error) {
					require.Equal(t, orgID, gotOrg, "settings lookup should use the request tenant")
					if tt.settingsError {
						return models.LinearAgentSettings{}, fault
					}
					return models.LinearAgentSettings{Enabled: &enabled, DefaultRepoID: &repoID}, nil
				}),
				TeamKeys: linearTeamKeyListerFunc(func(_ context.Context, gotOrg uuid.UUID) ([]db.LinearTeamKey, error) {
					require.Equal(t, orgID, gotOrg, "team directory should use the request tenant")
					if tt.teamsError {
						return nil, fault
					}
					return teams, nil
				}),
			})
			rr := httptest.NewRecorder()
			h.GetStatus(rr, linearCoverageRequest(http.MethodGet, "/", "", orgID))
			require.Equal(t, http.StatusOK, rr.Code, "optional dependencies should not prevent install status from loading")
			expected := tt.expected
			if !tt.settingsError {
				expected.DefaultRepoID = &repoID
			}
			if !tt.teamsError {
				expected.AvailableTeams = teams
			}
			var actual models.SingleResponse[LinearAgentInstallStatus]
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "status should serialize as a standard response")
			require.Equal(t, models.SingleResponse[LinearAgentInstallStatus]{Data: expected}, actual, "status should report precise install state and safe defaults on dependency failure")
		})
	}
}

func TestLinearAgentSettingsPatchValidationAndMergeContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, failAt, code, message string
		status                            int
		enableOnly, clear, disable        bool
		loadCalls, writeCalls             int
	}{
		{name: "enable only", body: `{"enabled":true}`, enableOnly: true, status: 204, writeCalls: 1},
		{name: "disable only", body: `{"enabled":false}`, enableOnly: true, disable: true, status: 204, writeCalls: 1},
		{name: "merge default repo preserves enable", body: `{"default_repo_id":"REPO"}`, status: 204, loadCalls: 1, writeCalls: 1},
		{name: "combined patch", body: `{"enabled":false,"default_repo_id":"REPO"}`, disable: true, status: 204, loadCalls: 1, writeCalls: 1},
		{name: "clear default repo preserves enable", body: `{"default_repo_id":null}`, clear: true, status: 204, loadCalls: 1, writeCalls: 1},
		{name: "malformed JSON", body: `{`, status: 400, code: "INVALID_JSON", message: "failed to parse request body"},
		{name: "oversized JSON", body: `{"enabled":true,"padding":"` + strings.Repeat("x", 65536) + `"}`, status: 400, code: "INVALID_JSON", message: "failed to parse request body"},
		{name: "no recognized fields", body: `{}`, status: 400, code: "INVALID_REQUEST", message: "enabled or default_repo_id is required"},
		{name: "enabled wrong type", body: `{"enabled":"yes"}`, status: 400, code: "INVALID_REQUEST", message: "enabled must be a boolean"},
		{name: "combined enabled wrong type", body: `{"enabled":[],"default_repo_id":null}`, status: 400, loadCalls: 1, code: "INVALID_REQUEST", message: "enabled must be a boolean"},
		{name: "invalid repo string", body: `{"default_repo_id":"invalid"}`, status: 400, loadCalls: 1, code: "INVALID_REQUEST", message: "default_repo_id must be a repository id or null"},
		{name: "nil repo id", body: `{"default_repo_id":"00000000-0000-0000-0000-000000000000"}`, status: 400, loadCalls: 1, code: "INVALID_REQUEST", message: "default_repo_id must be a repository id or null"},
		{name: "enable write failure", body: `{"enabled":true}`, enableOnly: true, failAt: "write", status: 500, writeCalls: 1, code: "PATCH_FAILED", message: "failed to update agent settings"},
		{name: "settings read failure", body: `{"default_repo_id":null}`, failAt: "read", status: 500, loadCalls: 1, code: "PATCH_FAILED", message: "failed to load agent settings"},
		{name: "settings write failure", body: `{"default_repo_id":null}`, clear: true, failAt: "write", status: 500, loadCalls: 1, writeCalls: 1, code: "PATCH_FAILED", message: "failed to update agent settings"},
		{name: "foreign or inactive repo", body: `{"default_repo_id":"REPO"}`, failAt: "repo", status: 400, loadCalls: 1, writeCalls: 1, code: "INVALID_REQUEST", message: "default_repo_id is not a valid active repository for this organization"},
		{name: "writer unavailable", body: `{"enabled":true}`, failAt: "unwired", status: 503, code: "ORG_WRITER_UNAVAILABLE", message: "agent settings writer not configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, oldRepo, newRepo := uuid.New(), uuid.New(), uuid.New()
			enabled := true
			loaded := models.LinearAgentSettings{Enabled: &enabled, DefaultRepoID: &oldRepo}
			reads, writes := 0, 0
			fault := errors.New("private dependency diagnostic")
			writeError := func() error {
				if tt.failAt == "repo" {
					return ErrInvalidDefaultRepo
				}
				if tt.failAt == "write" {
					return fault
				}
				return nil
			}
			writer := linearCoverageWriter{
				enable: func(gotOrg uuid.UUID, gotEnabled bool) error {
					writes++
					require.Equal(t, orgID, gotOrg, "enable write should use the request tenant")
					require.True(t, tt.enableOnly, "enable-only patch should use the narrow writer")
					require.Equal(t, !tt.disable, gotEnabled, "enable writer should receive the requested boolean")
					return writeError()
				},
				settings: func(gotOrg uuid.UUID, got models.LinearAgentSettings) error {
					writes++
					require.Equal(t, orgID, gotOrg, "settings write should use the request tenant")
					wantEnabled := !tt.disable
					expected := models.LinearAgentSettings{Enabled: &wantEnabled}
					if !tt.clear {
						expected.DefaultRepoID = &newRepo
					}
					require.Equal(t, expected, got, "merge should preserve omitted enabled state and replace or clear only requested values")
					return writeError()
				},
			}
			cfg := LinearAgentSettingsConfig{Orgs: writer, Settings: linearAgentSettingsLoaderFunc(func(_ context.Context, gotOrg uuid.UUID) (models.LinearAgentSettings, error) {
				reads++
				require.Equal(t, orgID, gotOrg, "merge read should use the request tenant")
				if tt.failAt == "read" {
					return models.LinearAgentSettings{}, fault
				}
				return loaded, nil
			})}
			if tt.failAt == "unwired" {
				cfg.Orgs = nil
			}
			rr := httptest.NewRecorder()
			NewLinearAgentSettingsHandler(cfg).PatchSettings(rr, linearCoverageRequest(http.MethodPatch, "/", strings.ReplaceAll(tt.body, "REPO", newRepo.String()), orgID))
			if tt.status == 204 {
				require.Equal(t, 204, rr.Code, "valid patch should finish without a response body")
				require.Empty(t, rr.Body.String(), "successful settings mutation should return no content")
			} else {
				requireLinearCoverageError(t, rr, tt.status, tt.code, tt.message)
			}
			require.Equal(t, tt.loadCalls, reads, "patch should load settings only for a valid merge path")
			require.Equal(t, tt.writeCalls, writes, "invalid patches should never reach a writer")
			require.Equal(t, models.LinearAgentSettings{Enabled: &enabled, DefaultRepoID: &oldRepo}, loaded, "patch assembly should not mutate the loaded settings snapshot")
		})
	}
}

func linearMappingCoverageRows(mapping *db.LinearTeamRepoMapping) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "linear_team_id", "linear_project_id", "repository_id", "default_branch", "priority", "created_at", "updated_at"})
	if mapping != nil {
		rows.AddRow(mapping.ID, mapping.OrgID, mapping.LinearTeamID, mapping.LinearProjectID, mapping.RepositoryID, mapping.DefaultBranch, mapping.Priority, mapping.CreatedAt, mapping.UpdatedAt)
	}
	return rows
}

func TestLinearAgentMappingCRUDAndErrorContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, operation, body, failure, code, message string
		status                                        int
		queryID, empty                                bool
	}{
		{name: "list mappings", operation: "list", status: 200}, {name: "empty mappings", operation: "list", empty: true, status: 200},
		{name: "list dependency failure", operation: "list", failure: "db", status: 500, code: "LIST_FAILED", message: "failed to list linear team repo mappings"},
		{name: "upsert mapping", operation: "upsert", body: `{"linear_team_id":"LT1","linear_project_id":"LP1","repository_id":"REPO","default_branch":"release","priority":3}`, status: 200},
		{name: "upsert foreign repo", operation: "upsert", body: `{"linear_team_id":"LT1","repository_id":"REPO"}`, failure: "missing", status: 400, code: "INVALID_REQUEST", message: "repository_id was not found for this organization"},
		{name: "upsert dependency failure", operation: "upsert", body: `{"linear_team_id":"LT1","repository_id":"REPO"}`, failure: "db", status: 500, code: "UPSERT_FAILED", message: "failed to upsert linear team repo mapping"},
		{name: "upsert malformed JSON", operation: "upsert", body: `{`, failure: "input", status: 400, code: "INVALID_JSON", message: "failed to parse request body"},
		{name: "upsert no team", operation: "upsert", body: `{"repository_id":"REPO"}`, failure: "input", status: 400, code: "INVALID_REQUEST", message: "linear_team_id is required"},
		{name: "upsert long team", operation: "upsert", body: `{"linear_team_id":"` + strings.Repeat("t", 65) + `","repository_id":"REPO"}`, failure: "input", status: 400, code: "INVALID_REQUEST", message: "linear_team_id exceeds length limit"},
		{name: "upsert long project", operation: "upsert", body: `{"linear_team_id":"LT1","linear_project_id":"` + strings.Repeat("p", 65) + `","repository_id":"REPO"}`, failure: "input", status: 400, code: "INVALID_REQUEST", message: "linear_project_id exceeds length limit"},
		{name: "upsert no repo", operation: "upsert", body: `{"linear_team_id":"LT1"}`, failure: "input", status: 400, code: "INVALID_REQUEST", message: "repository_id is required"},
		{name: "delete mapping", operation: "delete", status: 204}, {name: "delete query id fallback", operation: "delete", queryID: true, status: 204},
		{name: "delete missing or foreign", operation: "delete", failure: "missing", status: 404, code: "NOT_FOUND", message: "mapping not found"},
		{name: "delete dependency failure", operation: "delete", failure: "db", status: 500, code: "DELETE_FAILED", message: "failed to delete mapping"},
		{name: "delete invalid id", operation: "delete", failure: "input", status: 400, code: "INVALID_ID", message: "invalid id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mapping contract should create its own mock")
			t.Cleanup(mock.Close)
			orgID, repoID, id := uuid.New(), uuid.New(), uuid.New()
			project := "LP1"
			stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			mapping := db.LinearTeamRepoMapping{ID: id, OrgID: orgID, LinearTeamID: "LT1", LinearProjectID: &project, RepositoryID: repoID, DefaultBranch: "release", Priority: 3, CreatedAt: stamp, UpdatedAt: stamp}
			fault := errors.New("private database diagnostic")
			if tt.failure != "input" {
				switch tt.operation {
				case "list":
					q := mock.ExpectQuery(`FROM linear_team_repo_mappings WHERE org_id = @org_id`).WithArgs(pgx.NamedArgs{"org_id": orgID})
					if tt.failure == "db" {
						q.WillReturnError(fault)
					} else if tt.empty {
						q.WillReturnRows(linearMappingCoverageRows(nil))
					} else {
						q.WillReturnRows(linearMappingCoverageRows(&mapping))
					}
				case "upsert":
					var input upsertMappingRequest
					require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(tt.body, "REPO", repoID.String())), &input), "test fixture should decode a valid mapping")
					var projectArg any
					if input.LinearProjectID != "" {
						projectArg = input.LinearProjectID
					}
					q := mock.ExpectQuery(`INSERT INTO linear_team_repo_mappings .*FROM repositories r WHERE r.id = @repository_id AND r.org_id = @org_id AND r.status = 'active'`).WithArgs(pgx.NamedArgs{"org_id": orgID, "linear_team_id": input.LinearTeamID, "linear_project_id": projectArg, "repository_id": repoID, "default_branch": input.DefaultBranch, "priority": input.Priority})
					if tt.failure == "db" {
						q.WillReturnError(fault)
					} else if tt.failure == "missing" {
						q.WillReturnRows(linearMappingCoverageRows(nil))
					} else {
						q.WillReturnRows(linearMappingCoverageRows(&mapping))
					}
				case "delete":
					q := mock.ExpectExec(`DELETE FROM linear_team_repo_mappings WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": id, "org_id": orgID})
					if tt.failure == "db" {
						q.WillReturnError(fault)
					} else if tt.failure == "missing" {
						q.WillReturnResult(pgxmock.NewResult("DELETE", 0))
					} else {
						q.WillReturnResult(pgxmock.NewResult("DELETE", 1))
					}
				}
			}
			h := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{Mappings: db.NewLinearTeamRepoMappingStore(mock)})
			r := linearCoverageRequest(http.MethodPost, "/?id="+id.String(), strings.ReplaceAll(tt.body, "REPO", repoID.String()), orgID)
			if !tt.queryID {
				r.SetPathValue("id", id.String())
			}
			if tt.failure == "input" && tt.operation == "delete" {
				r.SetPathValue("id", "bad")
			}
			rr := httptest.NewRecorder()
			switch tt.operation {
			case "list":
				h.ListMappings(rr, r)
			case "upsert":
				h.UpsertMapping(rr, r)
			case "delete":
				h.DeleteMapping(rr, r)
			}
			if tt.status >= 400 {
				requireLinearCoverageError(t, rr, tt.status, tt.code, tt.message)
			} else {
				require.Equal(t, tt.status, rr.Code, "mapping operation should return its documented status")
				switch tt.operation {
				case "list":
					var actual models.ListResponse[db.LinearTeamRepoMapping]
					require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "mapping list should be valid JSON")
					expected := []db.LinearTeamRepoMapping{mapping}
					if tt.empty {
						expected = []db.LinearTeamRepoMapping{}
					}
					require.Equal(t, models.ListResponse[db.LinearTeamRepoMapping]{Data: expected}, actual, "mapping list should preserve all fields and normalize empty collections")
				case "upsert":
					var actual models.SingleResponse[db.LinearTeamRepoMapping]
					require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "mapping write should be valid JSON")
					require.Equal(t, models.SingleResponse[db.LinearTeamRepoMapping]{Data: mapping}, actual, "upsert should return the complete durable mapping")
				case "delete":
					require.Empty(t, rr.Body.String(), "mapping deletion should have no response body")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "mapping CRUD should preserve tenant ownership and stop on invalid inputs")
		})
	}
}
