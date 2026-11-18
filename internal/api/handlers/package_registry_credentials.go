package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/packagecredentials"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type packageRegistryCredentialService interface {
	Metadata(context.Context, uuid.UUID, uuid.UUID) ([]models.PackageRegistryCredentialSummary, error)
	Create(context.Context, uuid.UUID, uuid.UUID, string, string, string) (models.PackageRegistryCredentialSummary, error)
	Rotate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string) (models.PackageRegistryCredentialSummary, error)
	Delete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error
}

type PackageRegistryCredentialHandler struct {
	service packageRegistryCredentialService
	audit   *db.AuditEmitter
}

func NewPackageRegistryCredentialHandler(service packageRegistryCredentialService, audit *db.AuditEmitter) *PackageRegistryCredentialHandler {
	return &PackageRegistryCredentialHandler{service: service, audit: audit}
}

func (h *PackageRegistryCredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	rows, err := h.service.Metadata(r.Context(), orgID, repositoryID)
	if err != nil {
		writePackageRegistryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PackageRegistryCredentialSummary]{Data: rows})
}

func (h *PackageRegistryCredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		RegistryURL  string `json:"registry_url"`
		PackageScope string `json:"package_scope"`
		Token        string `json:"token"`
	}
	if !decodePackageRegistryRequest(w, r, &body) {
		return
	}
	row, err := h.service.Create(r.Context(), orgID, repositoryID, body.RegistryURL, body.PackageScope, body.Token)
	if err != nil {
		writePackageRegistryError(w, r, err)
		return
	}
	h.emitAudit(r, models.AuditActionCredentialUpdated, row)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PackageRegistryCredentialSummary]{Data: row})
}

func (h *PackageRegistryCredentialHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(w, r, "credential_id", "INVALID_PACKAGE_REGISTRY_CREDENTIAL_ID", "invalid package registry credential ID")
	if !ok {
		return
	}
	var body struct {
		ExpectedRevision uuid.UUID `json:"expected_revision"`
		Token            string    `json:"token"`
	}
	if !decodePackageRegistryRequest(w, r, &body) {
		return
	}
	if body.ExpectedRevision == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_REVISION", "expected_revision is required")
		return
	}
	row, err := h.service.Rotate(r.Context(), orgID, repositoryID, id, body.ExpectedRevision, body.Token)
	if err != nil {
		writePackageRegistryError(w, r, err)
		return
	}
	h.emitAudit(r, models.AuditActionCredentialUpdated, row)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PackageRegistryCredentialSummary]{Data: row})
}

func (h *PackageRegistryCredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseRepositoryIDParam(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(w, r, "credential_id", "INVALID_PACKAGE_REGISTRY_CREDENTIAL_ID", "invalid package registry credential ID")
	if !ok {
		return
	}
	revision, err := uuid.Parse(r.URL.Query().Get("expected_revision"))
	if err != nil || revision == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_REVISION", "expected_revision is required")
		return
	}
	if err := h.service.Delete(r.Context(), orgID, repositoryID, id, revision); err != nil {
		writePackageRegistryError(w, r, err)
		return
	}
	h.emitAudit(r, models.AuditActionCredentialDeleted, models.PackageRegistryCredentialSummary{ID: id, RepositoryID: repositoryID, Revision: revision})
	w.WriteHeader(http.StatusNoContent)
}

func (h *PackageRegistryCredentialHandler) emitAudit(r *http.Request, action models.AuditAction, row models.PackageRegistryCredentialSummary) {
	id := row.ID.String()
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{"kind": "package_registry", "repository_id": row.RepositoryID, "revision": row.Revision})
	emitUserAudit(h.audit, r, action, models.AuditResourceCredential, &id, details)
}

func decodePackageRegistryRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_CREDENTIAL", "invalid package registry credential request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_CREDENTIAL", "invalid package registry credential request")
		return false
	}
	return true
}

func writePackageRegistryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "PACKAGE_REGISTRY_CREDENTIAL_NOT_FOUND", "repository or package registry credential not found")
	case errors.Is(err, db.ErrPackageRegistryCredentialConflict):
		writeError(w, r, http.StatusConflict, "PACKAGE_REGISTRY_CREDENTIAL_CHANGED", "package registry credential changed; refresh and try again")
	case errors.Is(err, db.ErrPackageRegistryScopeConflict):
		writeError(w, r, http.StatusConflict, "PACKAGE_REGISTRY_CREDENTIAL_EXISTS", "this registry origin or package scope already has a credential for the repository")
	case errors.Is(err, packagecredentials.ErrEncryptionUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "PACKAGE_REGISTRY_ENCRYPTION_UNAVAILABLE", "package registry credential encryption is not configured")
	case errors.Is(err, packagecredentials.ErrInvalidCredential):
		writeError(w, r, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_CREDENTIAL", "use an HTTPS registry origin, a lowercase @npm scope, and a nonempty token without whitespace")
	default:
		writeError(w, r, http.StatusInternalServerError, "PACKAGE_REGISTRY_CREDENTIAL_FAILED", "package registry credential operation failed")
	}
}
