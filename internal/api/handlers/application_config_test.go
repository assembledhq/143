package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
)

func TestApplicationConfigHandlerGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
	}{
		{name: "capsules enabled", enabled: true},
		{name: "legacy renderer rollback", enabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewApplicationConfigHandler(tt.enabled)
			recorder := httptest.NewRecorder()
			handler.Get(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/application-config", nil))

			var response struct {
				Data ApplicationConfigResponse `json:"data"`
			}
			require.Equal(t, http.StatusOK, recorder.Code, "application config should return success")
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "application config should return valid JSON")
			require.Equal(t, tt.enabled, response.Data.SessionActivityCapsulesEnabled, "application config should expose the configured rendering switch")
			require.NotEmpty(t, response.Data.Revision, "application config should expose an opaque revision")
			require.False(t, response.Data.UpdatedAt.IsZero(), "application config should expose its load timestamp")
		})
	}
}

func TestApplicationConfigHandlerRecordSessionActivityEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "accepts bounded privacy safe dimensions", body: `{"event":"capsule_expanded","detail":"compact","status":"completed","reason":"final_response","trigger":"manual","viewport_class":"desktop","tool_count_bucket":"2-5","duration_bucket":"1-5m"}`, want: http.StatusNoContent},
		{name: "accepts bounded product measurement", body: `{"event":"completed_phase_rendered","detail":"compact","status":"completed","viewport_class":"mobile","value_bucket":"48-95px"}`, want: http.StatusNoContent},
		{name: "rejects transcript content fields through unknown event values", body: `{"event":"prompt text here"}`, want: http.StatusBadRequest},
		{name: "rejects unknown transcript content fields", body: `{"event":"capsule_expanded","transcript":"secret prompt"}`, want: http.StatusBadRequest},
		{name: "rejects unbounded attributes", body: `{"event":"capsule_expanded","trigger":"/secret/file/path"}`, want: http.StatusBadRequest},
		{name: "rejects unbounded measurement values", body: `{"event":"transcript_window_rendered","value_bucket":"437"}`, want: http.StatusBadRequest},
		{name: "rejects multiple JSON objects", body: `{"event":"capsule_expanded"}{"event":"capsule_collapsed"}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewApplicationConfigHandler(true)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/session-activity-events", strings.NewReader(tt.body))
			req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
			recorder := httptest.NewRecorder()
			handler.RecordSessionActivityEvent(recorder, req)
			require.Equal(t, tt.want, recorder.Code, "event endpoint should enforce the bounded telemetry contract")
		})
	}
}
