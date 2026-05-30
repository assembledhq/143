package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
)

type previewSecretBundleStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, in db.UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error)
	ReplaceActiveByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, in db.UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error)
	GetActive(ctx context.Context, orgID, repositoryID uuid.UUID, name string) (*models.PreviewSecretBundle, error)
	GetActiveByID(ctx context.Context, orgID, id uuid.UUID) (*models.PreviewSecretBundle, error)
	ListActive(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.PreviewSecretBundle, error)
	Disable(ctx context.Context, orgID, repositoryID uuid.UUID, name string, userID uuid.UUID) error
	DecryptSource(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) (models.PreviewSecretBundleSource, error)
	DecryptOutputs(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) ([]models.PreviewSecretBundleOutput, error)
}

type PreviewSecretBundleHandler struct {
	store previewSecretBundleStore
	audit *db.AuditEmitter
}

func NewPreviewSecretBundleHandler(store previewSecretBundleStore) *PreviewSecretBundleHandler {
	return &PreviewSecretBundleHandler{store: store}
}

// SetAuditEmitter injects the audit emitter for logging preview secret bundle events.
func (h *PreviewSecretBundleHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

type previewSecretBundleUpsertRequest struct {
	Name           string                             `json:"name"`
	Source         models.PreviewSecretBundleSource   `json:"source"`
	Outputs        []models.PreviewSecretBundleOutput `json:"outputs"`
	ExposurePolicy string                             `json:"exposure_policy,omitempty"`
}

type previewSecretBundlePatchRequest struct {
	Name           *string                             `json:"name,omitempty"`
	Source         *models.PreviewSecretBundleSource   `json:"source,omitempty"`
	Outputs        *[]models.PreviewSecretBundleOutput `json:"outputs,omitempty"`
	ExposurePolicy *string                             `json:"exposure_policy,omitempty"`
}

func (h *PreviewSecretBundleHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	rows, err := h.store.ListActive(r.Context(), orgID, repositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_PREVIEW_SECRET_BUNDLES_FAILED", "failed to list preview secret bundles", err)
		return
	}
	summaries := make([]models.PreviewSecretBundleSummary, 0, len(rows))
	for _, row := range rows {
		summary, err := h.summary(r.Context(), orgID, row)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
			return
		}
		summaries = append(summaries, summary)
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewSecretBundleSummary]{Data: summaries})
}

func (h *PreviewSecretBundleHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	row, err := h.store.GetActive(r.Context(), orgID, repositoryID, chi.URLParam(r, "name"))
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleSummary]{Data: summary})
}

func (h *PreviewSecretBundleHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseUUIDParam(w, r, "id", "INVALID_PREVIEW_SECRET_BUNDLE_ID", "invalid preview secret bundle id")
	if !ok {
		return
	}
	row, err := h.store.GetActiveByID(r.Context(), orgID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleSummary]{Data: summary})
}

func (h *PreviewSecretBundleHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
		return
	}
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	var body previewSecretBundleUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body", err)
		return
	}
	if errs := validatePreviewSecretBundleRequest(body); len(errs) > 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_SECRET_BUNDLE", "invalid preview secret bundle", errors.New(strings.Join(errs, "; ")))
		return
	}
	row, err := h.store.Upsert(r.Context(), orgID, db.UpsertPreviewSecretBundleInput{
		RepositoryID:    repositoryID,
		Name:            body.Name,
		Source:          body.Source,
		Outputs:         body.Outputs,
		ExposurePolicy:  body.ExposurePolicy,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPSERT_PREVIEW_SECRET_BUNDLE_FAILED", "failed to save preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	h.emitAudit(r, models.AuditActionPreviewSecretBundleUpdated, *row, summary)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleSummary]{Data: summary})
}

func (h *PreviewSecretBundleHandler) Patch(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id", "INVALID_PREVIEW_SECRET_BUNDLE_ID", "invalid preview secret bundle id")
	if !ok {
		return
	}
	existing, err := h.store.GetActiveByID(r.Context(), orgID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	source, err := h.store.DecryptSource(r.Context(), orgID, *existing)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SOURCE_FAILED", "failed to load preview secret bundle source", err)
		return
	}
	outputs, err := h.store.DecryptOutputs(r.Context(), orgID, *existing)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_OUTPUTS_FAILED", "failed to load preview secret bundle outputs", err)
		return
	}
	body := previewSecretBundlePatchRequest{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body", err)
		return
	}
	name := existing.Name
	if body.Name != nil {
		name = *body.Name
	}
	if body.Source != nil {
		source = *body.Source
	}
	if body.Outputs != nil {
		outputs = *body.Outputs
	}
	exposurePolicy := existing.ExposurePolicy
	if body.ExposurePolicy != nil {
		exposurePolicy = *body.ExposurePolicy
	}
	merged := previewSecretBundleUpsertRequest{
		Name:           name,
		Source:         source,
		Outputs:        outputs,
		ExposurePolicy: exposurePolicy,
	}
	if errs := validatePreviewSecretBundleRequest(merged); len(errs) > 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_SECRET_BUNDLE", "invalid preview secret bundle", errors.New(strings.Join(errs, "; ")))
		return
	}
	row, err := h.store.ReplaceActiveByID(r.Context(), orgID, id, db.UpsertPreviewSecretBundleInput{
		RepositoryID:    existing.RepositoryID,
		Name:            merged.Name,
		Source:          merged.Source,
		Outputs:         merged.Outputs,
		ExposurePolicy:  merged.ExposurePolicy,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, db.ErrPreviewSecretBundleNameConflict) {
			writeError(w, r, http.StatusConflict, "PREVIEW_SECRET_BUNDLE_NAME_CONFLICT", "preview secret bundle name already exists")
			return
		}
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "PATCH_PREVIEW_SECRET_BUNDLE_FAILED", "failed to update preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	h.emitAudit(r, models.AuditActionPreviewSecretBundleUpdated, *row, summary)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleSummary]{Data: summary})
}

func (h *PreviewSecretBundleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
		return
	}
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	row, err := h.store.GetActive(r.Context(), orgID, repositoryID, name)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	if err := h.store.Disable(r.Context(), orgID, repositoryID, name, user.ID); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_PREVIEW_SECRET_BUNDLE_FAILED", "failed to delete preview secret bundle", err)
		return
	}
	h.emitAudit(r, models.AuditActionPreviewSecretBundleDeleted, *row, summary)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PreviewSecretBundleHandler) DeleteByID(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id", "INVALID_PREVIEW_SECRET_BUNDLE_ID", "invalid preview secret bundle id")
	if !ok {
		return
	}
	row, err := h.store.GetActiveByID(r.Context(), orgID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	if err := h.store.Disable(r.Context(), orgID, row.RepositoryID, row.Name, user.ID); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_PREVIEW_SECRET_BUNDLE_FAILED", "failed to delete preview secret bundle", err)
		return
	}
	h.emitAudit(r, models.AuditActionPreviewSecretBundleDeleted, *row, summary)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PreviewSecretBundleHandler) Test(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseUUIDParam(w, r, "id", "INVALID_PREVIEW_SECRET_BUNDLE_ID", "invalid preview secret bundle id")
	if !ok {
		return
	}
	row, err := h.store.GetActiveByID(r.Context(), orgID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	source, err := h.store.DecryptSource(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SOURCE_FAILED", "failed to test preview secret bundle source", err)
		return
	}
	outputs, err := h.store.DecryptOutputs(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_OUTPUTS_FAILED", "failed to test preview secret bundle outputs", err)
		return
	}
	summary, err := h.summary(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SUMMARY_FAILED", "failed to summarize preview secret bundle", err)
		return
	}
	result := models.PreviewSecretBundleTestResult{
		Status: "ready",
		Bundle: summary,
	}
	if err := previewsvc.ValidatePreviewSecretBundle(source, outputs); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleTestResult]{Data: result})
}

func (h *PreviewSecretBundleHandler) Reveal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseUUIDParam(w, r, "id", "INVALID_PREVIEW_SECRET_BUNDLE_ID", "invalid preview secret bundle id")
	if !ok {
		return
	}
	row, err := h.store.GetActiveByID(r.Context(), orgID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "PREVIEW_SECRET_BUNDLE_NOT_FOUND", "preview secret bundle not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_PREVIEW_SECRET_BUNDLE_FAILED", "failed to get preview secret bundle", err)
		return
	}
	source, err := h.store.DecryptSource(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_SOURCE_FAILED", "failed to reveal preview secret bundle source", err)
		return
	}
	outputs, err := h.store.DecryptOutputs(r.Context(), orgID, *row)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_SECRET_BUNDLE_OUTPUTS_FAILED", "failed to reveal preview secret bundle outputs", err)
		return
	}
	summary := models.PreviewSecretBundleSummary{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		SourceType:      row.SourceType,
		ExposurePolicy:  row.ExposurePolicy,
		Outputs:         summarizePreviewSecretOutputs(outputs),
		CreatedByUserID: row.CreatedByUserID,
		CreatedAt:       row.CreatedAt,
	}
	h.emitAudit(r, models.AuditActionPreviewSecretBundleRevealed, *row, summary)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PreviewSecretBundleRevealResult]{
		Data: models.PreviewSecretBundleRevealResult{
			Bundle:  summary,
			Source:  source,
			Outputs: outputs,
		},
	})
}

func (h *PreviewSecretBundleHandler) summary(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) (models.PreviewSecretBundleSummary, error) {
	outputs, err := h.store.DecryptOutputs(ctx, orgID, row)
	if err != nil {
		return models.PreviewSecretBundleSummary{}, err
	}
	return models.PreviewSecretBundleSummary{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		SourceType:      row.SourceType,
		ExposurePolicy:  row.ExposurePolicy,
		Outputs:         summarizePreviewSecretOutputs(outputs),
		CreatedByUserID: row.CreatedByUserID,
		CreatedAt:       row.CreatedAt,
	}, nil
}

func (h *PreviewSecretBundleHandler) emitAudit(r *http.Request, action models.AuditAction, row models.PreviewSecretBundle, summary models.PreviewSecretBundleSummary) {
	resourceID := row.ID.String()
	details := map[string]any{
		"repository_id":   row.RepositoryID.String(),
		"name":            row.Name,
		"source_type":     row.SourceType,
		"exposure_policy": row.ExposurePolicy,
		"outputs":         summary.Outputs,
	}
	emitUserAudit(
		h.audit,
		r,
		action,
		models.AuditResourcePreviewSecretBundle,
		&resourceID,
		marshalAuditDetails(*zerolog.Ctx(r.Context()), details),
	)
}

func parseRepositoryIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	return parseUUIDParam(w, r, "id", "INVALID_REPOSITORY_ID", "invalid repository id")
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, param string, code string, message string) (uuid.UUID, bool) {
	value, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, code, message)
		return uuid.Nil, false
	}
	return value, true
}

func validatePreviewSecretBundleRequest(body previewSecretBundleUpsertRequest) []string {
	var errs []string
	if body.Name == "" {
		errs = append(errs, "name is required")
	}
	if body.Source.Type != "managed" {
		errs = append(errs, `source.type must be "managed"`)
	}
	if len(body.Source.Values) == 0 {
		errs = append(errs, "source.values is required")
	}
	if len(body.Outputs) == 0 {
		errs = append(errs, "outputs is required")
	}
	if body.ExposurePolicy != "" && body.ExposurePolicy != "preview_runtime" {
		errs = append(errs, `exposure_policy must be "preview_runtime"`)
	}
	for i, output := range body.Outputs {
		switch output.Type {
		case "env":
			if len(output.Values) == 0 {
				errs = append(errs, "outputs["+strconv.Itoa(i)+"].values is required for env outputs")
			}
		case "file":
			if output.Path == "" {
				errs = append(errs, "outputs["+strconv.Itoa(i)+"].path is required for file outputs")
			}
			if !isSafePreviewSecretFilePath(output.Path) {
				errs = append(errs, "outputs["+strconv.Itoa(i)+"].path must be relative, stay inside the repo, and avoid .git")
			}
			switch output.Format {
			case "", "env", "json", "raw":
			default:
				errs = append(errs, "outputs["+strconv.Itoa(i)+"].format is not supported")
			}
		default:
			errs = append(errs, "outputs["+strconv.Itoa(i)+"].type is not supported")
		}
	}
	if len(errs) == 0 {
		if err := previewsvc.ValidatePreviewSecretBundle(body.Source, body.Outputs); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}

func isSafePreviewSecretFilePath(raw string) bool {
	clean := path.Clean(raw)
	if raw == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".git" {
			return false
		}
	}
	return true
}

func summarizePreviewSecretOutputs(outputs []models.PreviewSecretBundleOutput) []models.PreviewSecretOutputSummary {
	summaries := make([]models.PreviewSecretOutputSummary, 0, len(outputs))
	for _, output := range outputs {
		summary := models.PreviewSecretOutputSummary{
			Type:   output.Type,
			Path:   output.Path,
			Format: output.Format,
		}
		for key := range output.Values {
			summary.Env = append(summary.Env, key)
		}
		sort.Strings(summary.Env)
		summaries = append(summaries, summary)
	}
	return summaries
}
