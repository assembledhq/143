package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalIssueCreator_Name(t *testing.T) {
	t.Parallel()
	c := NewInternalIssueCreator("token", "http://localhost")
	require.Equal(t, "issue", c.Name())
}

func TestInternalIssueCreator_CreateIssue_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/internal/issues", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var params CreateIssueParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&params))
		require.Equal(t, "Test Issue", params.Title)
		require.Equal(t, "Test description", params.Description)
		require.Equal(t, "warning", params.Severity)

		w.WriteHeader(http.StatusCreated)
		resp := CreateIssueResult{ID: "issue-123", Title: "Test Issue"}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	creator := NewInternalIssueCreator("test-token", server.URL)
	result, err := creator.CreateIssue(context.Background(), CreateIssueParams{
		Title:       "Test Issue",
		Description: "Test description",
		Severity:    "warning",
		Tags:        []string{"test"},
	})

	require.NoError(t, err)
	require.Equal(t, "issue-123", result.ID)
	require.Equal(t, "Test Issue", result.Title)
}

func TestInternalIssueCreator_CreateIssue_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer server.Close()

	creator := NewInternalIssueCreator("token", server.URL)
	_, err := creator.CreateIssue(context.Background(), CreateIssueParams{
		Title:       "Test",
		Description: "Desc",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "status 500")
}

func TestInternalIssueCreator_CreateIssue_InvalidURL(t *testing.T) {
	t.Parallel()

	creator := NewInternalIssueCreator("token", "http://localhost:0")
	_, err := creator.CreateIssue(context.Background(), CreateIssueParams{
		Title:       "Test",
		Description: "Desc",
	})

	require.Error(t, err)
}

func TestInternalIssueCreator_CreateIssue_WithSessionID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		sid := "session-456"
		resp := CreateIssueResult{ID: "issue-789", Title: "With Session", SessionID: &sid}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer server.Close()

	creator := NewInternalIssueCreator("token", server.URL)
	result, err := creator.CreateIssue(context.Background(), CreateIssueParams{
		Title:       "Test",
		Description: "Desc",
	})

	require.NoError(t, err)
	require.NotNil(t, result.SessionID)
	require.Equal(t, "session-456", *result.SessionID)
}
