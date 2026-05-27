package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakePreviewSecretBundleStore struct {
	upsertInput  *db.UpsertPreviewSecretBundleInput
	replaceID    uuid.UUID
	replaceInput *db.UpsertPreviewSecretBundleInput
	replaceErr   error
	disabledName string
	disabledUser uuid.UUID
	row          models.PreviewSecretBundle
	source       models.PreviewSecretBundleSource
	outputs      []models.PreviewSecretBundleOutput
}

func (s *fakePreviewSecretBundleStore) Upsert(_ context.Context, _ uuid.UUID, in db.UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error) {
	s.upsertInput = &in
	return &s.row, nil
}

func (s *fakePreviewSecretBundleStore) ReplaceActiveByID(_ context.Context, _ uuid.UUID, id uuid.UUID, in db.UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error) {
	s.replaceID = id
	s.replaceInput = &in
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	return &s.row, nil
}

func (s *fakePreviewSecretBundleStore) GetActive(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) (*models.PreviewSecretBundle, error) {
	return &s.row, nil
}

func (s *fakePreviewSecretBundleStore) GetActiveByID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*models.PreviewSecretBundle, error) {
	return &s.row, nil
}

func (s *fakePreviewSecretBundleStore) ListActive(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.PreviewSecretBundle, error) {
	return []models.PreviewSecretBundle{s.row}, nil
}

func (s *fakePreviewSecretBundleStore) Disable(_ context.Context, _ uuid.UUID, _ uuid.UUID, name string, userID uuid.UUID) error {
	s.disabledName = name
	s.disabledUser = userID
	return nil
}

func (s *fakePreviewSecretBundleStore) DecryptSource(_ context.Context, _ uuid.UUID, _ models.PreviewSecretBundle) (models.PreviewSecretBundleSource, error) {
	return s.source, nil
}

func (s *fakePreviewSecretBundleStore) DecryptOutputs(_ context.Context, _ uuid.UUID, _ models.PreviewSecretBundle) ([]models.PreviewSecretBundleOutput, error) {
	return s.outputs, nil
}

func TestPreviewSecretBundleHandler_PatchByIDMergesExistingBundle(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	bundleID := uuid.New()
	store := &fakePreviewSecretBundleStore{
		row: models.PreviewSecretBundle{
			ID:              bundleID,
			OrgID:           orgID,
			RepositoryID:    repoID,
			Name:            "repo-dev",
			SourceType:      "managed",
			ExposurePolicy:  "preview_runtime",
			CreatedByUserID: userID,
			CreatedAt:       time.Now(),
		},
		source: models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"DATABASE_URL": "postgres://dev"}},
		outputs: []models.PreviewSecretBundleOutput{{
			Type:   "env",
			Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"},
		}},
	}
	handler := NewPreviewSecretBundleHandler(store)
	body := bytes.NewBufferString(`{"name":"repo-renamed"}`)
	req := previewSecretBundleRequest(http.MethodPatch, "/api/v1/preview-secret-bundles/"+bundleID.String(), body, orgID, userID)
	addURLParam(req, "id", bundleID.String())
	rr := httptest.NewRecorder()

	handler.Patch(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Patch should return success")
	require.Equal(t, bundleID, store.replaceID, "Patch should update the active version addressed by id")
	require.NotNil(t, store.replaceInput, "Patch should pass a replacement input")
	require.Equal(t, repoID, store.replaceInput.RepositoryID, "Patch should keep the existing repository scope")
	require.Equal(t, "repo-renamed", store.replaceInput.Name, "Patch should apply the requested rename")
	require.Equal(t, store.source, store.replaceInput.Source, "Patch should preserve source when omitted")
	require.Equal(t, store.outputs, store.replaceInput.Outputs, "Patch should preserve outputs when omitted")
}

func TestPreviewSecretBundleHandler_PatchByIDReturnsConflictForNameCollision(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	bundleID := uuid.New()
	store := &fakePreviewSecretBundleStore{
		row: models.PreviewSecretBundle{
			ID:              bundleID,
			OrgID:           orgID,
			RepositoryID:    repoID,
			Name:            "repo-dev",
			SourceType:      "managed",
			ExposurePolicy:  "preview_runtime",
			CreatedByUserID: userID,
			CreatedAt:       time.Now(),
		},
		source:     models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"DATABASE_URL": "postgres://dev"}},
		outputs:    []models.PreviewSecretBundleOutput{{Type: "env", Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"}}},
		replaceErr: db.ErrPreviewSecretBundleNameConflict,
	}
	handler := NewPreviewSecretBundleHandler(store)
	body := bytes.NewBufferString(`{"name":"repo-prod"}`)
	req := previewSecretBundleRequest(http.MethodPatch, "/api/v1/preview-secret-bundles/"+bundleID.String(), body, orgID, userID)
	addURLParam(req, "id", bundleID.String())
	rr := httptest.NewRecorder()

	handler.Patch(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "Patch should return conflict when renaming to an existing active bundle")
	require.Contains(t, rr.Body.String(), "PREVIEW_SECRET_BUNDLE_NAME_CONFLICT", "response should include the conflict error code")
}

func TestPreviewSecretBundleHandler_TestDoesNotReturnPlaintext(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	bundleID := uuid.New()
	store := &fakePreviewSecretBundleStore{
		row: models.PreviewSecretBundle{
			ID:              bundleID,
			OrgID:           orgID,
			RepositoryID:    repoID,
			Name:            "repo-dev",
			SourceType:      "managed",
			ExposurePolicy:  "preview_runtime",
			CreatedByUserID: userID,
			CreatedAt:       time.Now(),
		},
		source: models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"DATABASE_URL": "postgres://user:pass@db/app"}},
		outputs: []models.PreviewSecretBundleOutput{{
			Type:   "env",
			Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"},
		}},
	}
	handler := NewPreviewSecretBundleHandler(store)
	req := previewSecretBundleRequest(http.MethodPost, "/api/v1/preview-secret-bundles/"+bundleID.String()+"/test", nil, orgID, userID)
	addURLParam(req, "id", bundleID.String())
	rr := httptest.NewRecorder()

	handler.Test(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Test should return success for a resolvable bundle")
	require.NotContains(t, rr.Body.String(), "postgres://user:pass@db/app", "Test response should not include plaintext secret values")
	var resp models.SingleResponse[models.PreviewSecretBundleTestResult]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Test response should be valid JSON")
	require.Equal(t, "ready", resp.Data.Status, "Test should report ready for valid managed bundle outputs")
	require.Equal(t, "repo-dev", resp.Data.Bundle.Name, "Test should return non-secret bundle metadata")
}

func TestPreviewSecretBundleHandler_UpsertRejectsInvalidJSONFileValue(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	store := &fakePreviewSecretBundleStore{}
	handler := NewPreviewSecretBundleHandler(store)
	body := bytes.NewBufferString(`{
		"name": "repo-dev",
		"source": {"type": "managed", "values": {"development_conf_json": "{\"bad\":"}},
		"outputs": [{
			"type": "file",
			"path": "development.conf.json",
			"format": "json",
			"value": "secret:development_conf_json"
		}]
	}`)
	req := previewSecretBundleRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/preview-secret-bundles", body, orgID, userID)
	addURLParam(req, "id", repoID.String())
	rr := httptest.NewRecorder()

	handler.Upsert(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Upsert should reject invalid JSON file secret values before saving")
	require.Nil(t, store.upsertInput, "Upsert should not save invalid preview secret bundle output configuration")
}

func TestPreviewSecretBundleHandler_DeletePassesUserForInactiveSuccessor(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	store := &fakePreviewSecretBundleStore{
		row: models.PreviewSecretBundle{
			ID:              uuid.New(),
			OrgID:           orgID,
			RepositoryID:    repoID,
			Name:            "repo-dev",
			SourceType:      "managed",
			ExposurePolicy:  "preview_runtime",
			CreatedByUserID: userID,
			CreatedAt:       time.Now(),
		},
		outputs: []models.PreviewSecretBundleOutput{{Type: "env", Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"}}},
	}
	handler := NewPreviewSecretBundleHandler(store)
	req := previewSecretBundleRequest(http.MethodDelete, "/api/v1/repositories/"+repoID.String()+"/preview-secret-bundles/repo-dev", nil, orgID, userID)
	addURLParam(req, "id", repoID.String())
	addURLParam(req, "name", "repo-dev")
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "Delete should return no content")
	require.Equal(t, "repo-dev", store.disabledName, "Delete should disable the requested bundle")
	require.Equal(t, userID, store.disabledUser, "Delete should pass the actor for the inactive successor version")
}

func previewSecretBundleRequest(method string, target string, body *bytes.Buffer, orgID, userID uuid.UUID) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body.Bytes())
	}
	req := httptest.NewRequest(method, target, reader)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	return req.WithContext(ctx)
}

func addURLParam(req *http.Request, key string, value string) {
	routeCtx := chi.RouteContext(req.Context())
	if routeCtx == nil {
		routeCtx = chi.NewRouteContext()
		*req = *req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	}
	routeCtx.URLParams.Add(key, value)
}
