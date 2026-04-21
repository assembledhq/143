	h.handleProvider(w, r, "linear")
}

var providerSignatureHeader = map[string]string{
	"sentry": "X-Sentry-Hook-Signature",
	"linear": "X-Linear-Signature",
		return
	}

	integrationIDStr := r.URL.Query().Get("integration_id")
	if integrationIDStr == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
		return
	}

	integration, err := h.integrationStore.GetByID(r.Context(), integrationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "integration not found")
		return
	}

	delivery := &models.WebhookDelivery{
		OrgID:         integration.OrgID,
		IntegrationID: integrationID,
		zerolog.Ctx(r.Context()).Error().Err(err).Msg("failed to record webhook delivery")
	}

	var normalized *ingestion.NormalizedIssue
	switch provider {
	case "sentry":
