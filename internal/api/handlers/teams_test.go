package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// --- mocks for orgTeamStore and orgTeamUserStore ---

type mockOrgTeamStore struct {
	createFn       func(ctx context.Context, team *models.Team) error
	updateFn       func(ctx context.Context, orgID, teamID uuid.UUID, name, slug string, description *string) error
	deleteFn       func(ctx context.Context, orgID, teamID uuid.UUID) error
	getByIDFn      func(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error)
	listByOrgFn    func(ctx context.Context, orgID uuid.UUID) ([]models.Team, error)
	listByUserFn   func(ctx context.Context, orgID, userID uuid.UUID) ([]models.Team, error)
	addMemberFn    func(ctx context.Context, orgID, teamID, userID uuid.UUID, role string) error
	removeMemberFn func(ctx context.Context, orgID, teamID, userID uuid.UUID) error
	listMembersFn  func(ctx context.Context, orgID, teamID uuid.UUID) ([]models.User, error)
	syncFn         func(ctx context.Context, orgID uuid.UUID, ghTeams []db.GitHubTeamSync) error
}

func (m *mockOrgTeamStore) Create(ctx context.Context, team *models.Team) error {
	if m.createFn != nil {
		return m.createFn(ctx, team)
	}
	team.ID = uuid.New()
	return nil
}
func (m *mockOrgTeamStore) Update(ctx context.Context, orgID, teamID uuid.UUID, name, slug string, description *string) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, orgID, teamID, name, slug, description)
	}
	return nil
}
func (m *mockOrgTeamStore) Delete(ctx context.Context, orgID, teamID uuid.UUID) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, teamID)
	}
	return nil
}
func (m *mockOrgTeamStore) GetByID(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, teamID)
	}
	return models.Team{ID: teamID, OrgID: orgID, Name: "Frontend", Slug: "frontend"}, nil
}
func (m *mockOrgTeamStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Team, error) {
	if m.listByOrgFn != nil {
		return m.listByOrgFn(ctx, orgID)
	}
	return nil, nil
}
func (m *mockOrgTeamStore) ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.Team, error) {
	if m.listByUserFn != nil {
		return m.listByUserFn(ctx, orgID, userID)
	}
	return nil, nil
}
func (m *mockOrgTeamStore) AddMember(ctx context.Context, orgID, teamID, userID uuid.UUID, role string) error {
	if m.addMemberFn != nil {
		return m.addMemberFn(ctx, orgID, teamID, userID, role)
	}
	return nil
}
func (m *mockOrgTeamStore) RemoveMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, orgID, teamID, userID)
	}
	return nil
}
func (m *mockOrgTeamStore) ListMembers(ctx context.Context, orgID, teamID uuid.UUID) ([]models.User, error) {
	if m.listMembersFn != nil {
		return m.listMembersFn(ctx, orgID, teamID)
	}
	return nil, nil
}
func (m *mockOrgTeamStore) SyncFromGitHub(ctx context.Context, orgID uuid.UUID, ghTeams []db.GitHubTeamSync) error {
	if m.syncFn != nil {
		return m.syncFn(ctx, orgID, ghTeams)
	}
	return nil
}

type mockOrgTeamUserStore struct {
	getByIDFn func(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

func (m *mockOrgTeamUserStore) GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, userID)
	}
	return models.User{ID: userID, OrgID: orgID}, nil
}

// --- helpers ---

func newOrgTeamHandler(teams *mockOrgTeamStore, users *mockOrgTeamUserStore) *OrgTeamHandler {
	if teams == nil {
		teams = &mockOrgTeamStore{}
	}
	if users == nil {
		users = &mockOrgTeamUserStore{}
	}
	return NewOrgTeamHandler(teams, users)
}

func orgTeamCtx(orgID uuid.UUID, user *models.User) context.Context {
	ctx := context.Background()
	ctx = middleware.WithOrgID(ctx, orgID)
	if user != nil {
		ctx = middleware.WithUser(ctx, user)
	}
	return ctx
}

// --- Create validation ---

func TestOrgTeamHandler_Create_RejectsMissingName(t *testing.T) {
	t.Parallel()

	h := newOrgTeamHandler(nil, nil)
	body := map[string]string{"name": "   "}
	buf, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams", bytes.NewReader(buf))
	req = req.WithContext(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOrgTeamHandler_Create_DerivesSlugFromName(t *testing.T) {
	t.Parallel()

	var captured *models.Team
	teams := &mockOrgTeamStore{
		createFn: func(_ context.Context, team *models.Team) error {
			captured = team
			team.ID = uuid.New()
			return nil
		},
	}
	h := newOrgTeamHandler(teams, nil)

	body := map[string]string{"name": "Mobile Apps"}
	buf, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams", bytes.NewReader(buf))
	req = req.WithContext(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, captured)
	require.Equal(t, "mobile-apps", captured.Slug)
}

// --- AddMember validation ---

func TestOrgTeamHandler_AddMember_RejectsInvalidRole(t *testing.T) {
	t.Parallel()

	h := newOrgTeamHandler(nil, nil)
	body := map[string]string{"user_id": uuid.NewString(), "role": "captain"}
	buf, _ := json.Marshal(body)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID.String()+"/members", bytes.NewReader(buf))
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.AddMember(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "VALIDATION_ERROR")
}

func TestOrgTeamHandler_AddMember_RejectsInvalidUserID(t *testing.T) {
	t.Parallel()

	h := newOrgTeamHandler(nil, nil)
	body := map[string]string{"user_id": "not-a-uuid"}
	buf, _ := json.Marshal(body)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID.String()+"/members", bytes.NewReader(buf))
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.AddMember(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_USER_ID")
}

func TestOrgTeamHandler_AddMember_RejectsCrossOrgUser(t *testing.T) {
	t.Parallel()

	users := &mockOrgTeamUserStore{
		getByIDFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.User, error) {
			return models.User{}, pgx.ErrNoRows
		},
	}
	h := newOrgTeamHandler(nil, users)

	body := map[string]string{"user_id": uuid.NewString()}
	buf, _ := json.Marshal(body)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID.String()+"/members", bytes.NewReader(buf))
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.AddMember(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "USER_NOT_FOUND")
}

// --- ListMine ---

func TestOrgTeamHandler_ListMine_ReturnsCallerTeams(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	teams := &mockOrgTeamStore{
		listByUserFn: func(_ context.Context, _ uuid.UUID, uid uuid.UUID) ([]models.Team, error) {
			require.Equal(t, userID, uid)
			return []models.Team{{ID: uuid.New(), Name: "Platform"}}, nil
		},
	}
	h := newOrgTeamHandler(teams, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/teams/mine", nil)
	req = req.WithContext(orgTeamCtx(uuid.New(), &models.User{ID: userID}))
	rec := httptest.NewRecorder()

	h.ListMine(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Platform")
}

// --- SyncGitHub guard ---

func TestOrgTeamHandler_SyncGitHub_503WhenNotConfigured(t *testing.T) {
	t.Parallel()

	h := newOrgTeamHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/teams/sync-github", nil)
	req = req.WithContext(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}))
	rec := httptest.NewRecorder()

	h.SyncGitHub(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "GITHUB_NOT_CONFIGURED")
}

// --- Update ---

func TestOrgTeamHandler_Update_Success(t *testing.T) {
	t.Parallel()

	var capturedName, capturedSlug string
	teams := &mockOrgTeamStore{
		updateFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, name, slug string, _ *string) error {
			capturedName = name
			capturedSlug = slug
			return nil
		},
		getByIDFn: func(_ context.Context, orgID, teamID uuid.UUID) (models.Team, error) {
			return models.Team{ID: teamID, OrgID: orgID, Name: "Renamed", Slug: "renamed"}, nil
		},
	}
	h := newOrgTeamHandler(teams, nil)

	body := map[string]string{"name": "Renamed"}
	buf, _ := json.Marshal(body)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/teams/"+teamID.String(), bytes.NewReader(buf))
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.Update(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Renamed", capturedName)
	require.Equal(t, "renamed", capturedSlug)
	require.Contains(t, rec.Body.String(), "Renamed")
}

func TestOrgTeamHandler_Update_NotFound(t *testing.T) {
	t.Parallel()

	teams := &mockOrgTeamStore{
		updateFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, _, _ string, _ *string) error {
			return pgx.ErrNoRows
		},
	}
	h := newOrgTeamHandler(teams, nil)

	body := map[string]string{"name": "X"}
	buf, _ := json.Marshal(body)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/teams/"+teamID.String(), bytes.NewReader(buf))
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.Update(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "NOT_FOUND")
}

// --- Delete ---

func TestOrgTeamHandler_Delete_Success(t *testing.T) {
	t.Parallel()

	var capturedOrgID, capturedTeamID uuid.UUID
	teams := &mockOrgTeamStore{
		deleteFn: func(_ context.Context, orgID, teamID uuid.UUID) error {
			capturedOrgID = orgID
			capturedTeamID = teamID
			return nil
		},
	}
	h := newOrgTeamHandler(teams, nil)

	orgID := uuid.New()
	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/teams/"+teamID.String(), nil)
	req = req.WithContext(withChiParam(orgTeamCtx(orgID, &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, orgID, capturedOrgID)
	require.Equal(t, teamID, capturedTeamID)
}

func TestOrgTeamHandler_Delete_NotFound(t *testing.T) {
	t.Parallel()

	teams := &mockOrgTeamStore{
		deleteFn: func(_ context.Context, _, _ uuid.UUID) error {
			return pgx.ErrNoRows
		},
	}
	h := newOrgTeamHandler(teams, nil)

	teamID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/teams/"+teamID.String(), nil)
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", teamID.String()))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "NOT_FOUND")
}

func TestOrgTeamHandler_Delete_RejectsInvalidID(t *testing.T) {
	t.Parallel()

	h := newOrgTeamHandler(nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/teams/not-a-uuid", nil)
	req = req.WithContext(withChiParam(orgTeamCtx(uuid.New(), &models.User{ID: uuid.New()}), "id", "not-a-uuid"))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_ID")
}

// --- toSlug helper ---

func TestToSlug(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"Frontend", "frontend"},
		{"  Mobile Apps  ", "mobile-apps"},
		{"!!!", "team"},
		{"data/eng", "data-eng"},
		{"a___b", "a-b"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, toSlug(c.in), "input=%q", c.in)
	}
}
