package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/demoseed"
	"github.com/stretchr/testify/require"
)

func TestDemoHandler_Manifest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          *config.Config
		expectedCode int
	}{
		{
			name:         "returns seeded manifest in demo mode",
			cfg:          &config.Config{DemoMode: true, DemoReadOnly: true},
			expectedCode: http.StatusOK,
		},
		{
			name:         "404 outside demo mode",
			cfg:          &config.Config{},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewDemoHandler(tt.cfg)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/demo/manifest", nil)
			w := httptest.NewRecorder()

			handler.Manifest(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "Manifest should return expected status")
			if tt.expectedCode != http.StatusOK {
				return
			}

			var resp struct {
				Data DemoManifestResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "Manifest response should be JSON")
			require.Equal(t, demoseed.DemoOrgID, resp.Data.Org.ID, "Manifest should return seeded org id")
			require.Equal(t, demoseed.PrimarySessionID, resp.Data.Primary.SessionID, "Manifest should return primary seeded session")
			require.Equal(t, demoseed.PreviewGroupID, resp.Data.Primary.PreviewGroupID, "Manifest should return seeded preview group")
			require.Equal(t, "/previews/"+demoseed.PreviewTargetID, resp.Data.Routes.PrimaryPreview, "Manifest should return frontend preview detail route")
			require.Equal(t, demoseed.DemoPRNumber, resp.Data.PullRequest.Number, "Manifest should return seeded PR number")
			require.True(t, resp.Data.ReadOnly, "Manifest should report read-only mode")
		})
	}
}
