package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// --- mock stores ---

type mockTeamUserStore struct {
	listByOrgViaMembershipsFn  func(ctx context.Context, orgID uuid.UUID, filters db.MembershipPageFilters) ([]models.User, time.Time, error)
	getByIDGlobalFn            func(ctx context.Context, userID uuid.UUID) (models.User, error)
	getByEmailFn               func(ctx context.Context, email string) (models.User, error)
	isGitHubLoginMemberOfOrgFn func(ctx context.Context, githubLogin string, orgID uuid.UUID) (bool, error)
}

func (m *mockTeamUserStore) ListByOrgViaMemberships(ctx context.Context, orgID uuid.UUID, filters db.MembershipPageFilters) ([]models.User, time.Time, error) {
	if m.listByOrgViaMembershipsFn != nil {
		return m.listByOrgViaMembershipsFn(ctx, orgID, filters)
	}
	return nil, time.Time{}, nil
}
func (m *mockTeamUserStore) GetByIDGlobal(ctx context.Context, userID uuid.UUID) (models.User, error) {
	if m.getByIDGlobalFn != nil {
		return m.getByIDGlobalFn(ctx, userID)
	}
	return models.User{ID: userID}, nil
}
func (m *mockTeamUserStore) GetByEmail(ctx context.Context, email string) (models.User, error) {
	if m.getByEmailFn != nil {
		return m.getByEmailFn(ctx, email)
	}
	return models.User{}, pgx.ErrNoRows
}
func (m *mockTeamUserStore) IsGitHubLoginMemberOfOrg(ctx context.Context, githubLogin string, orgID uuid.UUID) (bool, error) {
	if m.isGitHubLoginMemberOfOrgFn != nil {
		return m.isGitHubLoginMemberOfOrgFn(ctx, githubLogin, orgID)
	}
	return false, nil
}

type mockTeamMembershipStore struct {
	getFn               func(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
	updateRoleGuardedFn func(ctx context.Context, userID, orgID uuid.UUID, role string) (string, error)
	removeGuardedFn     func(ctx context.Context, userID, orgID uuid.UUID) (string, int, error)
	countForUserFn      func(ctx context.Context, userID uuid.UUID) (int, error)
}

func (m *mockTeamMembershipStore) Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if m.getFn != nil {
		return m.getFn(ctx, userID, orgID)
	}
	return models.OrganizationMembership{}, pgx.ErrNoRows
}
func (m *mockTeamMembershipStore) UpdateRoleGuarded(ctx context.Context, userID, orgID uuid.UUID, role string) (string, error) {
	if m.updateRoleGuardedFn != nil {
		return m.updateRoleGuardedFn(ctx, userID, orgID, role)
	}
	return "member", nil
}
func (m *mockTeamMembershipStore) RemoveGuarded(ctx context.Context, userID, orgID uuid.UUID) (string, int, error) {
	if m.removeGuardedFn != nil {
		return m.removeGuardedFn(ctx, userID, orgID)
	}
	return "member", 0, nil
}
func (m *mockTeamMembershipStore) CountForUser(ctx context.Context, userID uuid.UUID) (int, error) {
	if m.countForUserFn != nil {
		return m.countForUserFn(ctx, userID)
	}
	return 0, nil
}

type mockTeamSessionStore struct {
	deleteByUserIDFn func(ctx context.Context, userID uuid.UUID) error
}

func (m *mockTeamSessionStore) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	if m.deleteByUserIDFn != nil {
		return m.deleteByUserIDFn(ctx, userID)
	}
	return nil
}

type mockTeamInvitationStore struct {
	createFn                      func(ctx context.Context, inv *models.Invitation) error
	getByTokenFn                  func(ctx context.Context, token string) (models.Invitation, error)
	listPendingByOrgWithInviterFn func(ctx context.Context, orgID uuid.UUID) ([]models.InvitationWithInviter, error)
	acceptFn                      func(ctx context.Context, id uuid.UUID) error
	revokeFn                      func(ctx context.Context, orgID, id uuid.UUID) error
}

type mockTeamRepositoryStore struct {
	getAnyInstallationIDByOrgFn func(ctx context.Context, orgID uuid.UUID) (int64, error)
}

func (m *mockTeamRepositoryStore) GetAnyInstallationIDByOrg(ctx context.Context, orgID uuid.UUID) (int64, error) {
	if m.getAnyInstallationIDByOrgFn != nil {
		return m.getAnyInstallationIDByOrgFn(ctx, orgID)
	}
	return 0, pgx.ErrNoRows
}

type mockTeamIntegrationStore struct {
	listByOrgAndProviderFn func(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error)
}

func (m *mockTeamIntegrationStore) ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error) {
	if m.listByOrgAndProviderFn != nil {
		return m.listByOrgAndProviderFn(ctx, orgID, provider)
	}
	return nil, nil
}

type mockTeamGitHubService struct {
	getInstallationTokenFn func(ctx context.Context, installationID int64) (string, error)
}

func (m *mockTeamGitHubService) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	if m.getInstallationTokenFn != nil {
		return m.getInstallationTokenFn(ctx, installationID)
	}
	return "token", nil
}

func (m *mockTeamInvitationStore) Create(ctx context.Context, inv *models.Invitation) error {
	if m.createFn != nil {
		return m.createFn(ctx, inv)
	}
	inv.ID = uuid.New()
	inv.Status = "pending"
	inv.CreatedAt = time.Now()
	return nil
}
func (m *mockTeamInvitationStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
	if m.getByTokenFn != nil {
		return m.getByTokenFn(ctx, token)
	}
	return models.Invitation{}, pgx.ErrNoRows
}
func (m *mockTeamInvitationStore) ListPendingByOrgWithInviter(ctx context.Context, orgID uuid.UUID) ([]models.InvitationWithInviter, error) {
	if m.listPendingByOrgWithInviterFn != nil {
		return m.listPendingByOrgWithInviterFn(ctx, orgID)
	}
	return nil, nil
}
func (m *mockTeamInvitationStore) Accept(ctx context.Context, id uuid.UUID) error {
	if m.acceptFn != nil {
		return m.acceptFn(ctx, id)
	}
	return nil
}
func (m *mockTeamInvitationStore) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, orgID, id)
	}
	return nil
}

type mockTeamOrgStore struct {
	getByIDFn func(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

func (m *mockTeamOrgStore) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return models.Organization{Name: "Test Org"}, nil
}

// --- helpers ---

func newTeamHandler(users *mockTeamUserStore, memberships *mockTeamMembershipStore, sessions *mockTeamSessionStore, invitations *mockTeamInvitationStore, orgs *mockTeamOrgStore) *TeamHandler {
	if users == nil {
		users = &mockTeamUserStore{}
	}
	if memberships == nil {
		memberships = &mockTeamMembershipStore{}
	}
	if sessions == nil {
		sessions = &mockTeamSessionStore{}
	}
	if invitations == nil {
		invitations = &mockTeamInvitationStore{}
	}
	if orgs == nil {
		orgs = &mockTeamOrgStore{}
	}
	return NewTeamHandler(users, memberships, sessions, invitations, orgs, "http://localhost:3000", nil)
}

func TestTeamHandler_GitHubInviteStatus_FallsBackToRepositoryInstallationID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	h := newTeamHandler(nil, nil, nil, nil, nil)
	h.integrations = &mockTeamIntegrationStore{
		listByOrgAndProviderFn: func(ctx context.Context, gotOrgID uuid.UUID, provider string) ([]models.Integration, error) {
			return []models.Integration{{
				ID:       uuid.New(),
				OrgID:    gotOrgID,
				Provider: models.IntegrationProviderGitHub,
				Config:   []byte(`{}`),
				Status:   models.IntegrationStatusActive,
			}}, nil
		},
	}
	h.githubSvc = &mockTeamGitHubService{}
	h.repositories = &mockTeamRepositoryStore{
		getAnyInstallationIDByOrgFn: func(ctx context.Context, gotOrgID uuid.UUID) (int64, error) {
			require.Equal(t, orgID, gotOrgID, "fallback repo installation lookup should stay org-scoped")
			return 12345, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/team/github/status", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	h.GitHubInviteStatus(w, req)

	require.Equal(t, http.StatusOK, w.Code, "github invite status should succeed")

	var resp models.SingleResponse[GitHubInviteStatus]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "github invite status response should decode")
	require.True(t, resp.Data.Connected, "repo installation id fallback should mark github invite search connected")
}

func TestTeamHandler_SetRepositoryStore(t *testing.T) {
	t.Parallel()

	h := newTeamHandler(nil, nil, nil, nil, nil)
	repositories := &mockTeamRepositoryStore{}

	h.SetRepositoryStore(repositories)

	require.Same(t, repositories, h.repositories, "SetRepositoryStore should store the repository dependency")
}

func TestTeamHandler_GetGitHubInstallationID_FallbackCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	tests := []struct {
		name         string
		integrations teamIntegrationStore
		repositories teamRepositoryStore
		expected     int64
		expectErr    bool
	}{
		{
			name:         "returns zero when neither integrations nor repositories are configured",
			integrations: nil,
			repositories: nil,
			expected:     0,
		},
		{
			name:         "returns zero when repository fallback has no rows",
			integrations: nil,
			repositories: &mockTeamRepositoryStore{},
			expected:     0,
		},
		{
			name:         "returns repository fallback error",
			integrations: nil,
			repositories: &mockTeamRepositoryStore{
				getAnyInstallationIDByOrgFn: func(ctx context.Context, gotOrgID uuid.UUID) (int64, error) {
					require.Equal(t, orgID, gotOrgID, "repository fallback should stay org-scoped")
					return 0, context.DeadlineExceeded
				},
			},
			expectErr: true,
		},
		{
			name: "falls back after integrations without installation id",
			integrations: &mockTeamIntegrationStore{
				listByOrgAndProviderFn: func(ctx context.Context, gotOrgID uuid.UUID, provider string) ([]models.Integration, error) {
					require.Equal(t, orgID, gotOrgID, "integration lookup should stay org-scoped")
					require.Equal(t, string(models.IntegrationProviderGitHub), provider, "integration lookup should target github")
					return []models.Integration{{
						ID:       uuid.New(),
						OrgID:    gotOrgID,
						Provider: models.IntegrationProviderGitHub,
						Config:   []byte(`{}`),
						Status:   models.IntegrationStatusActive,
					}}, nil
				},
			},
			repositories: &mockTeamRepositoryStore{
				getAnyInstallationIDByOrgFn: func(ctx context.Context, gotOrgID uuid.UUID) (int64, error) {
					require.Equal(t, orgID, gotOrgID, "repository fallback should stay org-scoped")
					return 67890, nil
				},
			},
			expected: 67890,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(nil, nil, nil, nil, nil)
			h.integrations = tt.integrations
			h.repositories = tt.repositories

			installationID, err := h.getGitHubInstallationID(context.Background(), orgID)
			if tt.expectErr {
				require.Error(t, err, "getGitHubInstallationID should return an error for repository fallback failures")
				return
			}
			require.NoError(t, err, "getGitHubInstallationID should not return an error")
			require.Equal(t, tt.expected, installationID, "getGitHubInstallationID should return the expected installation id")
		})
	}
}

func teamCtx(orgID uuid.UUID, user *models.User) context.Context {
	ctx := context.Background()
	ctx = middleware.WithOrgID(ctx, orgID)
	if user != nil {
		ctx = middleware.WithUser(ctx, user)
	}
	return ctx
}

func withChiParam(ctx context.Context, key, value string) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

// --- ListMembers tests ---

func TestTeamHandler_ListMembers(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	tests := []struct {
		name         string
		users        *mockTeamUserStore
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns members",
			users: &mockTeamUserStore{
				listByOrgViaMembershipsFn: func(_ context.Context, _ uuid.UUID, _ db.MembershipPageFilters) ([]models.User, time.Time, error) {
					return []models.User{
						{ID: uuid.New(), Email: "a@b.com", Name: "Alice", Role: "admin"},
						{ID: uuid.New(), Email: "c@d.com", Name: "Bob", Role: "member"},
					}, time.Now(), nil
				},
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name: "returns empty array when no members",
			users: &mockTeamUserStore{
				listByOrgViaMembershipsFn: func(_ context.Context, _ uuid.UUID, _ db.MembershipPageFilters) ([]models.User, time.Time, error) {
					return nil, time.Time{}, nil
				},
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name: "store error returns 500",
			users: &mockTeamUserStore{
				listByOrgViaMembershipsFn: func(_ context.Context, _ uuid.UUID, _ db.MembershipPageFilters) ([]models.User, time.Time, error) {
					return nil, time.Time{}, fmt.Errorf("db error")
				},
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/team/members", nil)
			req = req.WithContext(teamCtx(orgID, nil))
			w := httptest.NewRecorder()

			h.ListMembers(w, req)
			require.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK {
				var resp models.ListResponse[models.User]
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Data, tt.expectedLen)
			}
		})
	}
}

// --- ChangeRole tests ---

func TestTeamHandler_ChangeRole(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	tests := []struct {
		name         string
		memberID     string
		body         map[string]string
		currentUser  *models.User
		users        *mockTeamUserStore
		memberships  *mockTeamMembershipStore
		expectedCode int
		expectedBody string
	}{
		{
			name:         "invalid member ID returns 400",
			memberID:     "not-a-uuid",
			body:         map[string]string{"role": "member"},
			currentUser:  adminUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
		{
			name:         "invalid role returns 400",
			memberID:     memberID.String(),
			body:         map[string]string{"role": "superadmin"},
			currentUser:  adminUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "VALIDATION_ERROR",
		},
		{
			name:         "cannot change own role",
			memberID:     adminUser.ID.String(),
			body:         map[string]string{"role": "member"},
			currentUser:  adminUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "CANNOT_CHANGE_OWN_ROLE",
		},
		{
			name:        "member not found returns 404",
			memberID:    memberID.String(),
			body:        map[string]string{"role": "member"},
			currentUser: adminUser,
			memberships: &mockTeamMembershipStore{
				updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, _ string) (string, error) {
					return "", pgx.ErrNoRows
				},
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "MEMBER_NOT_FOUND",
		},
		{
			name:        "cannot demote last admin",
			memberID:    memberID.String(),
			body:        map[string]string{"role": "member"},
			currentUser: adminUser,
			memberships: &mockTeamMembershipStore{
				updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, _ string) (string, error) {
					return "admin", db.ErrLastAdmin
				},
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "LAST_ADMIN",
		},
		{
			name:        "successfully changes role",
			memberID:    memberID.String(),
			body:        map[string]string{"role": "viewer"},
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Email: "b@c.com", Name: "Bob"}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, _ string) (string, error) {
					return "member", nil
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name:        "successfully changes role to builder",
			memberID:    memberID.String(),
			body:        map[string]string{"role": "builder"},
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Email: "b@c.com", Name: "Bob"}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, role string) (string, error) {
					require.Equal(t, "builder", role, "ChangeRole should pass the builder role through to the membership store")
					return "member", nil
				},
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, tt.memberships, nil, nil, nil)
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/members/"+tt.memberID+"/role", bytes.NewReader(body))
			ctx := teamCtx(orgID, tt.currentUser)
			ctx = withChiParam(ctx, "id", tt.memberID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			h.ChangeRole(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}

// --- RemoveMember tests ---

func TestTeamHandler_RemoveMember(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	tests := []struct {
		name         string
		memberID     string
		currentUser  *models.User
		users        *mockTeamUserStore
		memberships  *mockTeamMembershipStore
		expectedCode int
		expectedBody string
	}{
		{
			name:         "invalid member ID returns 400",
			memberID:     "bad",
			currentUser:  adminUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
		{
			name:         "cannot remove self",
			memberID:     adminUser.ID.String(),
			currentUser:  adminUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "CANNOT_REMOVE_SELF",
		},
		{
			name:        "cannot remove last admin",
			memberID:    memberID.String(),
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Email: "b@c.com"}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) {
					return "admin", 0, db.ErrLastAdmin
				},
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "LAST_ADMIN",
		},
		{
			name:        "successfully removes member",
			memberID:    memberID.String(),
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Email: "b@c.com"}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) {
					return "member", 0, nil
				},
			},
			expectedCode: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, tt.memberships, nil, nil, nil)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+tt.memberID, nil)
			ctx := teamCtx(orgID, tt.currentUser)
			ctx = withChiParam(ctx, "id", tt.memberID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			h.RemoveMember(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}

// ChangeRole surfaces an internal error when the guarded update fails with a
// non-ErrNoRows / non-ErrLastAdmin error (e.g. DB down), distinct from the
// 404 not-found path and the LAST_ADMIN guard.
func TestTeamHandler_ChangeRole_LookupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(nil, &mockTeamMembershipStore{
		updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, _ string) (string, error) {
			return "", fmt.Errorf("boom")
		},
	}, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"role": "member"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/members/"+memberID.String()+"/role", bytes.NewReader(body))
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ChangeRole(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "UPDATE_FAILED")
}

// ChangeRole returns 200 with a minimal {id, role} payload when the response-
// shaping user lookup fails after the role has already been updated. The
// mutation succeeded at the DB level, so the client should treat it as
// successful and re-fetch /team/members to hydrate display fields — surfacing
// 500 would incorrectly suggest the role change failed and invite retries.
func TestTeamHandler_ChangeRole_PostUpdateLookupFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
				return models.User{}, fmt.Errorf("boom")
			},
		},
		&mockTeamMembershipStore{
			updateRoleGuardedFn: func(_ context.Context, _, _ uuid.UUID, _ string) (string, error) {
				return "member", nil
			},
		},
		nil, nil, nil,
	)

	body, _ := json.Marshal(map[string]string{"role": "viewer"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/members/"+memberID.String()+"/role", bytes.NewReader(body))
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ChangeRole(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.User]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, memberID, resp.Data.ID)
	require.Equal(t, "viewer", resp.Data.Role)
}

func TestTeamHandler_CreateInvitation_AcceptsBuilderRole(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin", Name: "Admin User"}
	createdRoles := make([]string, 0, 1)

	h := newTeamHandler(nil, &mockTeamMembershipStore{}, nil, &mockTeamInvitationStore{
		createFn: func(_ context.Context, inv *models.Invitation) error {
			createdRoles = append(createdRoles, inv.Role)
			inv.ID = uuid.New()
			inv.Status = "pending"
			inv.CreatedAt = time.Now()
			return nil
		},
	}, nil)

	body, _ := json.Marshal(map[string]string{"email": "builder@example.com", "role": "builder"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations", bytes.NewReader(body))
	req = req.WithContext(teamCtx(orgID, adminUser))
	w := httptest.NewRecorder()

	h.CreateInvitation(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "CreateInvitation should accept builder as a valid membership role")
	require.Equal(t, []string{"builder"}, createdRoles, "CreateInvitation should persist the requested builder role")
}

// RemoveMember succeeds even when CountForUser fails afterward — the removal
// has already happened, so the CountForUser error is logged and the response
// is still 204. The caller's session cleanup is best-effort.
func TestTeamHandler_RemoveMember_CountForUserErrorIsLogged(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id}, nil
			},
		},
		&mockTeamMembershipStore{
			removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) { return "member", 0, nil },
			countForUserFn:  func(_ context.Context, _ uuid.UUID) (int, error) { return 0, fmt.Errorf("count down") },
		},
		&mockTeamSessionStore{
			deleteByUserIDFn: func(_ context.Context, _ uuid.UUID) error { return nil },
		},
		nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

// RemoveMember still returns 204 when DeleteByUserID fails — the removal
// succeeded; stale session cleanup is best-effort and will be caught the
// next time the session's membership lookup returns ErrNoRows.
func TestTeamHandler_RemoveMember_DeleteSessionsErrorIsLogged(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id}, nil
			},
		},
		&mockTeamMembershipStore{
			removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) { return "member", 0, nil },
			countForUserFn:  func(_ context.Context, _ uuid.UUID) (int, error) { return 0, nil },
		},
		&mockTeamSessionStore{
			deleteByUserIDFn: func(_ context.Context, _ uuid.UUID) error { return fmt.Errorf("del err") },
		},
		nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

// CreateInvitation rejects with 409 when the GitHub-username branch finds
// an existing member: the dedup join across organization_memberships
// catches users whose only membership in this org is non-primary.
func TestTeamHandler_CreateInvitation_GitHubAlreadyMember(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}

	h := newTeamHandler(
		&mockTeamUserStore{
			isGitHubLoginMemberOfOrgFn: func(_ context.Context, _ string, _ uuid.UUID) (bool, error) {
				return true, nil
			},
		},
		nil, nil, nil, nil,
	)

	body, _ := json.Marshal(map[string]string{
		"email":           "octocat@example.com",
		"github_username": "octocat",
		"role":            "member",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations", bytes.NewReader(body))
	req = req.WithContext(teamCtx(orgID, adminUser))
	w := httptest.NewRecorder()

	h.CreateInvitation(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "ALREADY_MEMBER")
}

// CreateInvitation surfaces a 500 when the GitHub-username dedup lookup
// fails with a DB error — we cannot safely invite if dedup is unreliable.
func TestTeamHandler_CreateInvitation_GitHubDedupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}

	h := newTeamHandler(
		&mockTeamUserStore{
			isGitHubLoginMemberOfOrgFn: func(_ context.Context, _ string, _ uuid.UUID) (bool, error) {
				return false, fmt.Errorf("db down")
			},
		},
		nil, nil, nil, nil,
	)

	body, _ := json.Marshal(map[string]string{
		"email":           "octocat@example.com",
		"github_username": "octocat",
		"role":            "member",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations", bytes.NewReader(body))
	req = req.WithContext(teamCtx(orgID, adminUser))
	w := httptest.NewRecorder()

	h.CreateInvitation(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "LOOKUP_FAILED")
}

// CreateInvitation surfaces a 500 when the email-dedup membership lookup
// fails with a non-ErrNoRows error — we don't invite into an org we can't
// reliably check dedup against.
func TestTeamHandler_CreateInvitation_DedupLookupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	existingID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByEmailFn: func(_ context.Context, _ string) (models.User, error) {
				return models.User{ID: existingID}, nil
			},
		},
		&mockTeamMembershipStore{
			getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{}, fmt.Errorf("db down")
			},
		},
		nil, nil, nil,
	)

	body, _ := json.Marshal(map[string]string{"email": "invite@example.com", "role": "member"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations", bytes.NewReader(body))
	ctx := teamCtx(orgID, adminUser)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.CreateInvitation(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "LOOKUP_FAILED")
}

// RemoveMember deletes the user's sessions only when the user has no other
// remaining memberships — otherwise they would be logged out of orgs they
// still belong to.
func TestTeamHandler_RemoveMember_DeletesSessionsWhenLastMembership(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	deletedForUser := uuid.Nil
	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id, Email: "m@c.com"}, nil
			},
		},
		&mockTeamMembershipStore{
			countForUserFn: func(_ context.Context, _ uuid.UUID) (int, error) { return 0, nil },
		},
		&mockTeamSessionStore{
			deleteByUserIDFn: func(_ context.Context, id uuid.UUID) error {
				deletedForUser = id
				return nil
			},
		},
		nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, memberID, deletedForUser, "session store should be called with the removed member's id")
}

// RemoveMember keeps the user logged in when they have remaining memberships
// elsewhere.
func TestTeamHandler_RemoveMember_KeepsSessionsWhenOtherMembershipsRemain(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	sessionDeleted := false
	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id, Email: "m@c.com"}, nil
			},
		},
		&mockTeamMembershipStore{
			countForUserFn: func(_ context.Context, _ uuid.UUID) (int, error) { return 2, nil },
		},
		&mockTeamSessionStore{
			deleteByUserIDFn: func(_ context.Context, _ uuid.UUID) error {
				sessionDeleted = true
				return nil
			},
		},
		nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.False(t, sessionDeleted, "sessions should not be deleted while other memberships remain")
}

// RemoveMember returns 404 when the user identity row does not exist
// (pgx.ErrNoRows). The membership store is not consulted in this case.
func TestTeamHandler_RemoveMember_UserLookupNotFound(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
				return models.User{}, pgx.ErrNoRows
			},
		},
		&mockTeamMembershipStore{},
		nil, nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "MEMBER_NOT_FOUND")
}

// RemoveMember returns 500 (not 404) when the user identity lookup fails
// with a non-ErrNoRows error like a DB outage — masking these as 404 hides
// real failures from operators.
func TestTeamHandler_RemoveMember_UserLookupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, _ uuid.UUID) (models.User, error) {
				return models.User{}, fmt.Errorf("db down")
			},
		},
		&mockTeamMembershipStore{},
		nil, nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "LOOKUP_FAILED")
}

// RemoveMember returns 404 when the guarded removal reports ErrNoRows.
// This is the "user exists but isn't a member of this org" path.
func TestTeamHandler_RemoveMember_MembershipNotFound(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id}, nil
			},
		},
		&mockTeamMembershipStore{
			removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) {
				return "", 0, pgx.ErrNoRows
			},
		},
		nil, nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "MEMBER_NOT_FOUND")
}

// RemoveMember surfaces an internal error when the guarded removal fails for
// any reason other than ErrNoRows or the LAST_ADMIN guard.
func TestTeamHandler_RemoveMember_DeleteError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(
		&mockTeamUserStore{
			getByIDGlobalFn: func(_ context.Context, id uuid.UUID) (models.User, error) {
				return models.User{ID: id}, nil
			},
		},
		&mockTeamMembershipStore{
			removeGuardedFn: func(_ context.Context, _, _ uuid.UUID) (string, int, error) {
				return "", 0, fmt.Errorf("boom")
			},
		},
		nil, nil, nil,
	)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/members/"+memberID.String(), nil)
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.RemoveMember(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "DELETE_FAILED")
}

// --- CreateInvitation tests ---

func TestTeamHandler_CreateInvitation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Name: "Admin", Role: "admin"}

	existingUserID := uuid.New()

	tests := []struct {
		name         string
		body         map[string]string
		users        *mockTeamUserStore
		memberships  *mockTeamMembershipStore
		invitations  *mockTeamInvitationStore
		expectedCode int
		expectedBody string
	}{
		{
			name:         "invalid email returns 400",
			body:         map[string]string{"email": "not-email", "role": "member"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "VALIDATION_ERROR",
		},
		{
			name:         "invalid role returns 400",
			body:         map[string]string{"email": "a@b.com", "role": "superadmin"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "VALIDATION_ERROR",
		},
		{
			name: "already a member returns 409",
			body: map[string]string{"email": "existing@b.com", "role": "member"},
			users: &mockTeamUserStore{
				getByEmailFn: func(_ context.Context, _ string) (models.User, error) {
					return models.User{ID: existingUserID}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				getFn: func(_ context.Context, userID, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{UserID: userID, OrgID: orgID, Role: "member"}, nil
				},
			},
			expectedCode: http.StatusConflict,
			expectedBody: "ALREADY_MEMBER",
		},
		{
			name: "existing user in another org does not block invite",
			body: map[string]string{"email": "elsewhere@b.com", "role": "member"},
			users: &mockTeamUserStore{
				getByEmailFn: func(_ context.Context, _ string) (models.User, error) {
					return models.User{ID: existingUserID}, nil
				},
			},
			memberships: &mockTeamMembershipStore{
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{}, pgx.ErrNoRows
				},
			},
			expectedCode: http.StatusCreated,
		},
		{
			name: "successfully creates invitation",
			body: map[string]string{"email": "new@b.com", "role": "member"},
			users: &mockTeamUserStore{
				getByEmailFn: func(_ context.Context, _ string) (models.User, error) {
					return models.User{}, pgx.ErrNoRows
				},
			},
			expectedCode: http.StatusCreated,
		},
		{
			name: "defaults role to member",
			body: map[string]string{"email": "new@b.com"},
			users: &mockTeamUserStore{
				getByEmailFn: func(_ context.Context, _ string) (models.User, error) {
					return models.User{}, pgx.ErrNoRows
				},
			},
			expectedCode: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, tt.memberships, nil, tt.invitations, nil)
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations", bytes.NewReader(body))
			req = req.WithContext(teamCtx(orgID, adminUser))
			w := httptest.NewRecorder()

			h.CreateInvitation(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}

// --- RevokeInvitation tests ---

func TestTeamHandler_RevokeInvitation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	invID := uuid.New()

	tests := []struct {
		name         string
		invitationID string
		invitations  *mockTeamInvitationStore
		expectedCode int
		expectedBody string
	}{
		{
			name:         "invalid ID returns 400",
			invitationID: "bad",
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
		{
			name:         "not found returns 404",
			invitationID: invID.String(),
			invitations: &mockTeamInvitationStore{
				revokeFn: func(_ context.Context, _, _ uuid.UUID) error {
					return pgx.ErrNoRows
				},
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "INVITE_NOT_FOUND",
		},
		{
			name:         "successfully revokes",
			invitationID: invID.String(),
			invitations: &mockTeamInvitationStore{
				revokeFn: func(_ context.Context, _, _ uuid.UUID) error {
					return nil
				},
			},
			expectedCode: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(nil, nil, nil, tt.invitations, nil)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/invitations/"+tt.invitationID, nil)
			ctx := teamCtx(orgID, nil)
			ctx = withChiParam(ctx, "id", tt.invitationID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			h.RevokeInvitation(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}

// --- AcceptInvitation tests ---

func TestTeamHandler_AcceptInvitation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	tests := []struct {
		name         string
		body         map[string]string
		invitations  *mockTeamInvitationStore
		orgs         *mockTeamOrgStore
		users        *mockTeamUserStore
		expectedCode int
		expectedBody string
	}{
		{
			name:         "missing token returns 400",
			body:         map[string]string{},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BODY",
		},
		{
			name:         "invalid token returns 404",
			body:         map[string]string{"token": "invalid-token"},
			expectedCode: http.StatusNotFound,
			expectedBody: "INVITE_NOT_FOUND",
		},
		{
			name: "already accepted returns 410",
			body: map[string]string{"token": "used-token"},
			invitations: &mockTeamInvitationStore{
				getByTokenFn: func(_ context.Context, _ string) (models.Invitation, error) {
					return models.Invitation{Status: "accepted", OrgID: orgID}, nil
				},
			},
			expectedCode: http.StatusGone,
			expectedBody: "INVITE_ALREADY_USED",
		},
		{
			name: "revoked returns 410",
			body: map[string]string{"token": "revoked-token"},
			invitations: &mockTeamInvitationStore{
				getByTokenFn: func(_ context.Context, _ string) (models.Invitation, error) {
					return models.Invitation{Status: "revoked", OrgID: orgID}, nil
				},
			},
			expectedCode: http.StatusGone,
			expectedBody: "INVITE_REVOKED",
		},
		{
			name: "expired returns 410",
			body: map[string]string{"token": "expired-token"},
			invitations: &mockTeamInvitationStore{
				getByTokenFn: func(_ context.Context, _ string) (models.Invitation, error) {
					return models.Invitation{
						Status:    "pending",
						OrgID:     orgID,
						ExpiresAt: time.Now().Add(-1 * time.Hour),
					}, nil
				},
			},
			expectedCode: http.StatusGone,
			expectedBody: "INVITE_EXPIRED",
		},
		{
			name: "valid token returns register action",
			body: map[string]string{"token": "valid-token"},
			invitations: &mockTeamInvitationStore{
				getByTokenFn: func(_ context.Context, _ string) (models.Invitation, error) {
					email := "new@b.com"
					return models.Invitation{
						Status:    "pending",
						OrgID:     orgID,
						Email:     &email,
						ExpiresAt: time.Now().Add(24 * time.Hour),
					}, nil
				},
			},
			orgs: &mockTeamOrgStore{
				getByIDFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
					return models.Organization{Name: "Test Org"}, nil
				},
			},
			expectedCode: http.StatusOK,
			expectedBody: "register",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, nil, nil, tt.invitations, tt.orgs)
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invitations/accept", bytes.NewReader(body))
			w := httptest.NewRecorder()

			h.AcceptInvitation(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}
