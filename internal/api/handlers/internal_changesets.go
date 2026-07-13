package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// InternalChangesetHandler exposes the same audited changeset control plane to
// the session-scoped 143-tools token without granting access to another
// session or repository.
type InternalChangesetHandler struct {
	sessions *db.SessionStore
	delegate *SessionHandler
	secret   string
}

func NewInternalChangesetHandler(sessions *db.SessionStore, delegate *SessionHandler, secret string) *InternalChangesetHandler {
	return &InternalChangesetHandler{sessions: sessions, delegate: delegate, secret: secret}
}

func (h *InternalChangesetHandler) wrap(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, session, ok := authorizeInternalSession(w, r, h.secret, h.sessions)
		if !ok {
			return
		}
		requested, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil || claims.SessionID == nil || requested != *claims.SessionID {
			writeError(w, r, http.StatusForbidden, "SESSION_MISMATCH", "sandbox token is not authorized for this session")
			return
		}
		userID := uuid.Nil
		if session.TriggeredByUserID != nil {
			userID = *session.TriggeredByUserID
		}
		ctx := middleware.WithOrgID(r.Context(), claims.OrgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: claims.OrgID, Role: models.RoleMember})
		handler(w, r.WithContext(ctx))
	}
}

func (h *InternalChangesetHandler) List(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.ListChangesets)(w, r)
}
func (h *InternalChangesetHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.GetChangesetSplitStatus)(w, r)
}
func (h *InternalChangesetHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.CreateChangeset)(w, r)
}
func (h *InternalChangesetHandler) Materialize(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.MaterializeChangeset)(w, r)
}
func (h *InternalChangesetHandler) Verify(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.VerifyChangesetSplit)(w, r)
}
func (h *InternalChangesetHandler) Diff(w http.ResponseWriter, r *http.Request) {
	h.wrap(h.delegate.GetDiff)(w, r)
}
