package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalAutomationManager_CreateAutomation(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should be valid JSON")
		w.WriteHeader(http.StatusCreated)
		_, err := w.Write([]byte(`{"data":{"id":"automation-1","name":"Nightly"}}`))
		require.NoError(t, err, "test server should write response")
	}))
	defer server.Close()

	manager := NewInternalAutomationManager("test-token", server.URL)
	raw, err := manager.CreateAutomation(context.Background(), json.RawMessage(`{"name":"Nightly","goal":"Run cleanup"}`))

	require.NoError(t, err, "CreateAutomation should not return an error")
	require.JSONEq(t, `{"data":{"id":"automation-1","name":"Nightly"}}`, string(raw), "CreateAutomation should return the raw API response")
	require.Equal(t, "/automations", gotPath, "CreateAutomation should call the internal automations endpoint")
	require.Equal(t, "Bearer test-token", gotAuth, "CreateAutomation should send bearer auth")
	require.Equal(t, "Nightly", gotBody["name"], "CreateAutomation should forward the payload")
}

func TestInternalAutomationManager_RunAutomation(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"data":{"status":"queued"}}`))
		require.NoError(t, err, "test server should write response")
	}))
	defer server.Close()

	manager := NewInternalAutomationManager("test-token", server.URL)
	raw, err := manager.RunAutomation(context.Background(), "automation-1")

	require.NoError(t, err, "RunAutomation should not return an error")
	require.JSONEq(t, `{"data":{"status":"queued"}}`, string(raw), "RunAutomation should return the raw API response")
	require.Equal(t, "/automations/automation-1/run", gotPath, "RunAutomation should call the run endpoint")
}
