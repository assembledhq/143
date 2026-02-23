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
	listByOrgFn   func(ctx context.Context, orgID uuid.UUID) ([]models.User, error)
	getByIDFn     func(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
	getByEmailFn  func(ctx context.Context, email string) (models.User, error)
	updateRoleFn  func(ctx context.Context, orgID, userID uuid.UUID, role string) error
	deleteFn      func(ctx context.Context, orgID, userID uuid.UUID) error
	countAdminsFn func(ctx context.Context, orgID uuid.UUID) (int, error)
}

func (m *mockTeamUserStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.User, error) {
	if m.listByOrgFn != nil {
		return m.listByOrgFn(ctx, orgID)
	}
	return nil, nil
}
func (m *mockTeamUserStore) GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, userID)
	}
	return models.User{}, pgx.ErrNoRows
}
func (m *mockTeamUserStore) GetByEmail(ctx context.Context, email string) (models.User, error) {
	if m.getByEmailFn != nil {
		return m.getByEmailFn(ctx, email)
	}
	return models.User{}, pgx.ErrNoRows
}
func (m *mockTeamUserStore) UpdateRole(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	if m.updateRoleFn != nil {
		return m.updateRoleFn(ctx, orgID, userID, role)
	}
	return nil
}
func (m *mockTeamUserStore) Delete(ctx context.Context, orgID, userID uuid.UUID) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, userID)
	}
	return nil
}
func (m *mockTeamUserStore) CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
	if m.countAdminsFn != nil {
		return m.countAdminsFn(ctx, orgID)
	}
	return 2, nil
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

func newTeamHandler(users *mockTeamUserStore, sessions *mockTeamSessionStore, invitations *mockTeamInvitationStore, orgs *mockTeamOrgStore) *TeamHandler {
	if users == nil {
		users = &mockTeamUserStore{}
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
	return NewTeamHandler(users, sessions, invitations, orgs, "http://localhost:3000")
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

			h := newTeamHandler(tt.users, nil, nil, nil)
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
			name:     "cannot demote last admin",
			memberID: memberID.String(),
			body:     map[string]string{"role": "member"},
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Role: "admin"}, nil
				},
				countAdminsFn: func(_ context.Context, _ uuid.UUID) (int, error) {
					return 1, nil
				},
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "LAST_ADMIN",
		},
		{
			name:     "successfully changes role",
			memberID: memberID.String(),
			body:     map[string]string{"role": "viewer"},
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Role: "member", Email: "b@c.com", Name: "Bob"}, nil
				},
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, nil, nil, nil)
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
			name:     "cannot remove last admin",
			memberID: memberID.String(),
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Role: "admin"}, nil
				},
				countAdminsFn: func(_ context.Context, _ uuid.UUID) (int, error) {
					return 1, nil
				},
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "LAST_ADMIN",
		},
		{
			name:     "successfully removes member",
			memberID: memberID.String(),
			currentUser: adminUser,
			users: &mockTeamUserStore{
				getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.User, error) {
					return models.User{ID: memberID, Role: "member"}, nil
				},
			},
			expectedCode: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTeamHandler(tt.users, nil, nil, nil)
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

// --- CreateInvitation tests ---

func TestTeamHandler_CreateInvitation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUser := &models.User{ID: uuid.New(), OrgID: orgID, Name: "Admin", Role: "admin"}

	tests := []struct {
		name         string
		body         map[string]string
		users        *mockTeamUserStore
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
					return models.User{OrgID: orgID}, nil
				},
			},
			expectedCode: http.StatusConflict,
			expectedBody: "ALREADY_MEMBER",
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

			h := newTeamHandler(tt.users, nil, tt.invitations, nil)
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

			h := newTeamHandler(nil, nil, tt.invitations, nil)
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
					return models.Invitation{
						Status:    "pending",
						OrgID:     orgID,
						Email:     "new@b.com",
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

			h := newTeamHandler(tt.users, nil, tt.invitations, tt.orgs)
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
