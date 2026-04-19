package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type mockTeamLookup struct {
	getByIDFn func(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error)
}

func (m *mockTeamLookup) GetByID(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, teamID)
	}
	return models.Team{ID: teamID, OrgID: orgID}, nil
}

func TestResolveTeamID_EmptyReturnsNil(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	id, ok := resolveTeamID(rec, req, nil, uuid.New(), "")
	require.True(t, ok)
	require.Nil(t, id)
	require.Equal(t, http.StatusOK, rec.Code) // no response written
}

func TestResolveTeamID_InvalidUUID(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	id, ok := resolveTeamID(rec, req, nil, uuid.New(), "not-a-uuid")
	require.False(t, ok)
	require.Nil(t, id)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_TEAM_ID")
}

func TestResolveTeamID_CrossTenantRejected(t *testing.T) {
	t.Parallel()

	lookup := &mockTeamLookup{
		getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.Team, error) {
			return models.Team{}, pgx.ErrNoRows
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	id, ok := resolveTeamID(rec, req, lookup, uuid.New(), uuid.NewString())
	require.False(t, ok)
	require.Nil(t, id)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "TEAM_NOT_FOUND")
}

func TestResolveTeamID_LookupError(t *testing.T) {
	t.Parallel()

	lookup := &mockTeamLookup{
		getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.Team, error) {
			return models.Team{}, errors.New("boom")
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	id, ok := resolveTeamID(rec, req, lookup, uuid.New(), uuid.NewString())
	require.False(t, ok)
	require.Nil(t, id)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "TEAM_LOOKUP_FAILED")
}

func TestResolveTeamID_NilLookupSkipsVerification(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	raw := uuid.NewString()
	id, ok := resolveTeamID(rec, req, nil, uuid.New(), raw)
	require.True(t, ok)
	require.NotNil(t, id)
	require.Equal(t, raw, id.String())
}

func TestResolveTeamID_ValidLookupSucceeds(t *testing.T) {
	t.Parallel()

	lookup := &mockTeamLookup{} // default returns team found
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	raw := uuid.NewString()
	id, ok := resolveTeamID(rec, req, lookup, uuid.New(), raw)
	require.True(t, ok)
	require.NotNil(t, id)
	require.Equal(t, raw, id.String())
}
