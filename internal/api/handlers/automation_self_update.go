package handlers

import (
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func (h *AutomationHandler) UpdatePersonalBaseBranch(w http.ResponseWriter, r *http.Request, automationID uuid.UUID, actor models.AutomationActionActor, baseBranch string) {
	orgID := middleware.OrgIDFromContext(r.Context())
	before, after, err := h.automationStore.UpdatePersonalBaseBranch(r.Context(), orgID, automationID, actor, baseBranch)
	if errors.Is(err, db.ErrAutomationSelfUpdateUnauthorized) {
		writeError(w, r, http.StatusForbidden, "IDENTITY_SCOPE_FORBIDDEN", "personal automation base updates require its executing automation attempt")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update automation base branch", err)
		return
	}
	if changes := automationAuditDiff(&before, &after); len(changes) > 0 {
		id := automationID.String()
		emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationUpdated, models.AuditResourceAutomation, &id, &actor.SessionID, nil,
			marshalAuditDetails(h.logger, map[string]any{"name": after.Name, "changes": changes}))
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: after})
}
