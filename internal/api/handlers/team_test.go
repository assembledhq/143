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
	"github.com/assembledhq/143/internal/models"
)

// --- mock stores ---

type mockTeamUserStore struct {
	listByOrgFn     func(ctx context.Context, orgID uuid.UUID) ([]models.User, error)
	getByIDGlobalFn func(ctx context.Context, userID uuid.UUID) (models.User, error)
	getByEmailFn    func(ctx context.Context, email string) (models.User, error)
}

func (m *mockTeamUserStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.User, error) {
	if m.listByOrgFn != nil {
		return m.listByOrgFn(ctx, orgID)
	}
	return nil, nil
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

type mockTeamMembershipStore struct {
	getFn          func(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
	updateRoleFn   func(ctx context.Context, userID, orgID uuid.UUID, role string) error
	removeFn       func(ctx context.Context, userID, orgID uuid.UUID) error
	countAdminsFn  func(ctx context.Context, orgID uuid.UUID) (int, error)
	countForUserFn func(ctx context.Context, userID uuid.UUID) (int, error)
}

func (m *mockTeamMembershipStore) Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if m.getFn != nil {
		return m.getFn(ctx, userID, orgID)
	}
	return models.OrganizationMembership{}, pgx.ErrNoRows
}
func (m *mockTeamMembershipStore) UpdateRole(ctx context.Context, userID, orgID uuid.UUID, role string) error {
	if m.updateRoleFn != nil {
		return m.updateRoleFn(ctx, userID, orgID, role)
	}
	return nil
}
func (m *mockTeamMembershipStore) Remove(ctx context.Context, userID, orgID uuid.UUID) error {
	if m.removeFn != nil {
		return m.removeFn(ctx, userID, orgID)
	}
	return nil
}
func (m *mockTeamMembershipStore) CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
	if m.countAdminsFn != nil {
		return m.countAdminsFn(ctx, orgID)
	}
	return 2, nil
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
				listByOrgFn: func(_ context.Context, _ uuid.UUID) ([]models.User, error) {
					return []models.User{
						{ID: uuid.New(), Email: "a@b.com", Name: "Alice", Role: "admin"},
						{ID: uuid.New(), Email: "c@d.com", Name: "Bob", Role: "member"},
					}, nil
				},
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name: "returns empty array when no members",
			users: &mockTeamUserStore{
				listByOrgFn: func(_ context.Context, _ uuid.UUID) ([]models.User, error) {
					return nil, nil
				},
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name: "store error returns 500",
			users: &mockTeamUserStore{
				listByOrgFn: func(_ context.Context, _ uuid.UUID) ([]models.User, error) {
					return nil, fmt.Errorf("db error")
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
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{}, pgx.ErrNoRows
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
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "admin"}, nil
				},
				countAdminsFn: func(_ context.Context, _ uuid.UUID) (int, error) {
					return 1, nil
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
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "member"}, nil
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
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "admin"}, nil
				},
				countAdminsFn: func(_ context.Context, _ uuid.UUID) (int, error) {
					return 1, nil
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
				getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
					return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "member"}, nil
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

// ChangeRole surfaces an internal error when membership lookup fails with a
// non-ErrNoRows error (e.g. DB down), distinct from the 404 not-found path.
func TestTeamHandler_ChangeRole_LookupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(nil, &mockTeamMembershipStore{
		getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
			return models.OrganizationMembership{}, fmt.Errorf("boom")
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
	require.Contains(t, w.Body.String(), "LOOKUP_FAILED")
}

// ChangeRole converts pgx.ErrNoRows from UpdateRole into a 404 (race where
// the membership was removed between the Get and the UpdateRole).
func TestTeamHandler_ChangeRole_UpdateRaceNotFound(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Role: "admin"}
	memberID := uuid.New()

	h := newTeamHandler(nil, &mockTeamMembershipStore{
		getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
			return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "member"}, nil
		},
		updateRoleFn: func(_ context.Context, _, _ uuid.UUID, _ string) error {
			return pgx.ErrNoRows
		},
	}, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"role": "viewer"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/members/"+memberID.String()+"/role", bytes.NewReader(body))
	ctx := teamCtx(orgID, adminUser)
	ctx = withChiParam(ctx, "id", memberID.String())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ChangeRole(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "MEMBER_NOT_FOUND")
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
			getFn: func(_ context.Context, id, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{UserID: id, OrgID: orgID, Role: "member"}, nil
			},
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
			getFn: func(_ context.Context, id, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{UserID: id, OrgID: orgID, Role: "member"}, nil
			},
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

// RemoveMember returns 404 when the user identity lookup fails — it's the
// same public response as "no such membership" because we don't want to leak
// whether a user id exists at all.
func TestTeamHandler_RemoveMember_UserLookupFails(t *testing.T) {
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

// RemoveMember returns 404 when membership lookup reports ErrNoRows; this is
// the "user exists but isn't a member of this org" path.
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
			getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{}, pgx.ErrNoRows
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

// RemoveMember returns 500 when membership lookup fails with a non-ErrNoRows
// error (DB down, etc.).
func TestTeamHandler_RemoveMember_MembershipLookupError(t *testing.T) {
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
			getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{}, fmt.Errorf("db down")
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
	require.Contains(t, w.Body.String(), "LOOKUP_FAILED")
}

// RemoveMember returns 500 when CountAdmins fails during the last-admin check,
// so admins don't get silently kept/removed based on a count we couldn't read.
func TestTeamHandler_RemoveMember_CountAdminsFails(t *testing.T) {
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
			getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "admin"}, nil
			},
			countAdminsFn: func(_ context.Context, _ uuid.UUID) (int, error) {
				return 0, fmt.Errorf("boom")
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
	require.Contains(t, w.Body.String(), "COUNT_FAILED")
}

// RemoveMember treats a race where Remove returns pgx.ErrNoRows (another
// admin deleted the membership first) as a 404, not a 500.
func TestTeamHandler_RemoveMember_RemoveRaceNotFound(t *testing.T) {
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
			getFn: func(_ context.Context, _, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{UserID: memberID, OrgID: orgID, Role: "member"}, nil
			},
			removeFn: func(_ context.Context, _, _ uuid.UUID) error {
				return pgx.ErrNoRows
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

// RemoveMember surfaces an internal error when Remove fails for a reason
// other than pgx.ErrNoRows.
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
			getFn: func(_ context.Context, id, _ uuid.UUID) (models.OrganizationMembership, error) {
				return models.OrganizationMembership{UserID: id, OrgID: orgID, Role: "member"}, nil
			},
			removeFn: func(_ context.Context, _, _ uuid.UUID) error {
				return fmt.Errorf("boom")
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
