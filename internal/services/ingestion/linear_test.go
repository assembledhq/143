package ingestion

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLinearAdapter_ParseWebhook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		payload     json.RawMessage
		expectErr   bool
		errSubstr   string
		expectNil   bool
		checkResult func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID)
	}{
		{
			name: "create action parses all fields correctly",
			payload: json.RawMessage(`{
				"action": "create",
				"type": "Issue",
				"data": {
					"id": "lin-123",
					"identifier": "ENG-456",
					"title": "Fix authentication timeout",
					"description": "Users report session expiring too quickly",
					"priority": 1,
					"state": {"name": "In Progress", "type": "started"},
					"labels": [{"name": "bug"}, {"name": "auth"}],
					"createdAt": "2024-01-15T10:30:00Z",
					"updatedAt": "2024-01-15T11:00:00Z",
					"team": {"key": "ENG", "name": "Engineering"}
				}
			}`),
			expectErr: false,
			expectNil: false,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Equal(t, "lin-123", ni.ExternalID, "should parse external ID from linear issue ID")
				require.Equal(t, "linear", ni.Source, "source should be linear")
				require.Equal(t, integrationID, ni.SourceIntegrationID, "should set integration ID")
				require.Equal(t, "ENG-456: Fix authentication timeout", ni.Title, "should format title with identifier prefix")
				require.Equal(t, "Users report session expiring too quickly", ni.Description, "should parse description")
				require.Equal(t, "critical", ni.Severity, "priority 1 should map to critical severity")
				require.Equal(t, 1, ni.OccurrenceCount, "occurrence count should default to 1")
				require.Contains(t, ni.Tags, "bug", "tags should include label names")
				require.Contains(t, ni.Tags, "auth", "tags should include label names")
				require.Contains(t, ni.Tags, "team:ENG", "tags should include team key")
			},
		},
		{
			name: "update action parses priority correctly",
			payload: json.RawMessage(`{
				"action": "update",
				"type": "Issue",
				"data": {
					"id": "lin-789",
					"identifier": "ENG-789",
					"title": "Performance regression",
					"description": "API response times increased by 3x",
					"priority": 2,
					"state": {"name": "Todo", "type": "unstarted"},
					"labels": [],
					"createdAt": "2024-01-15T10:30:00Z",
					"updatedAt": "2024-01-15T11:00:00Z",
					"team": {"key": "ENG", "name": "Engineering"}
				}
			}`),
			expectErr: false,
			expectNil: false,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Equal(t, "high", ni.Severity, "priority 2 should map to high severity")
			},
		},
		{
			name: "non-Issue type is skipped and returns nil",
			payload: json.RawMessage(`{
				"action": "create",
				"type": "Comment",
				"data": {"id": "comment-123"}
			}`),
			expectErr: false,
			expectNil: true,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Nil(t, ni, "non-Issue types should be skipped")
			},
		},
		{
			name: "remove action is skipped and returns nil",
			payload: json.RawMessage(`{
				"action": "remove",
				"type": "Issue",
				"data": {"id": "lin-123", "title": "Deleted issue"}
			}`),
			expectErr: false,
			expectNil: true,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Nil(t, ni, "delete actions should be skipped")
			},
		},
		{
			name: "missing issue ID returns error",
			payload: json.RawMessage(`{
				"action": "create",
				"type": "Issue",
				"data": {"title": "Missing ID"}
			}`),
			expectErr: true,
			errSubstr: "missing issue ID",
			expectNil: true,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Nil(t, ni, "result should be nil when issue ID is missing")
			},
		},
		{
			name:      "invalid JSON returns error",
			payload:   json.RawMessage(`{broken`),
			expectErr: true,
			expectNil: true,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Nil(t, ni, "result should be nil for invalid JSON")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := NewLinearAdapter()
			integrationID := uuid.New()

			ni, err := adapter.ParseWebhook(integrationID, tt.payload)

			if tt.expectErr {
				require.Error(t, err, "ParseWebhook should return an error")
				if tt.errSubstr != "" {
					require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
				}
			} else {
				require.NoError(t, err, "ParseWebhook should not return an error")
			}

			if tt.expectNil {
				require.Nil(t, ni, "result should be nil")
			} else {
				require.NotNil(t, ni, "result should not be nil")
			}

			tt.checkResult(t, ni, integrationID)
		})
	}
}

func TestMapLinearPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected string
	}{
		{name: "priority 0 (none) maps to medium", input: 0, expected: "medium"},
		{name: "priority 1 (urgent) maps to critical", input: 1, expected: "critical"},
		{name: "priority 2 maps to high", input: 2, expected: "high"},
		{name: "priority 3 maps to medium", input: 3, expected: "medium"},
		{name: "priority 4 maps to low", input: 4, expected: "low"},
		{name: "unknown priority maps to medium", input: 5, expected: "medium"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("priority_%d", tt.input), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, mapLinearPriority(tt.input), "mapLinearPriority should return expected severity")
		})
	}
}
