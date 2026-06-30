package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/demoseed"
)

type DemoHandler struct {
	cfg *config.Config
}

func NewDemoHandler(cfg *config.Config) *DemoHandler {
	return &DemoHandler{cfg: cfg}
}

type DemoManifestResponse struct {
	Org         DemoManifestOrg         `json:"org"`
	Primary     DemoManifestPrimary     `json:"primary"`
	PullRequest DemoManifestPullRequest `json:"pull_request"`
	Routes      DemoManifestRoutes      `json:"routes"`
	ReadOnly    bool                    `json:"read_only"`
}

type DemoManifestOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DemoManifestPrimary struct {
	SessionID       string `json:"session_id"`
	PreviewGroupID  string `json:"preview_group_id"`
	PreviewTargetID string `json:"preview_target_id"`
}

type DemoManifestPullRequest struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
}

type DemoManifestRoutes struct {
	Demo           string `json:"demo"`
	Sessions       string `json:"sessions"`
	PrimarySession string `json:"primary_session"`
	PrimaryPreview string `json:"primary_preview"`
	PullRequest    string `json:"pull_request"`
}

func (h *DemoHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil || !h.cfg.DemoMode {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "demo manifest is not available")
		return
	}

	response := DemoManifestResponse{
		Org: DemoManifestOrg{
			ID:   demoseed.DemoOrgID,
			Name: demoseed.DemoOrgName,
		},
		Primary: DemoManifestPrimary{
			SessionID:       demoseed.PrimarySessionID,
			PreviewGroupID:  demoseed.PreviewGroupID,
			PreviewTargetID: demoseed.PreviewTargetID,
		},
		PullRequest: DemoManifestPullRequest{
			ID:         demoseed.DemoPullRequestID,
			Repository: demoseed.DemoRepository,
			Number:     demoseed.DemoPRNumber,
			URL:        demoseed.DemoPullRequestURL,
		},
		Routes: DemoManifestRoutes{
			Demo:           "/demo",
			Sessions:       "/sessions",
			PrimarySession: "/sessions/" + demoseed.PrimarySessionID,
			PrimaryPreview: "/previews/" + demoseed.PreviewTargetID,
			PullRequest:    demoseed.DemoPullRequestURL,
		},
		ReadOnly: h.cfg.DemoReadOnly,
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}
