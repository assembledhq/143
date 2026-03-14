package ingestion

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSentryAdapter_ParseWebhook(t *testing.T) {
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
			name: "created action parses all fields correctly",
			payload: json.RawMessage(`{
				"action": "created",
				"data": {
					"issue": {
						"id": "12345",
						"title": "TypeError: Cannot read property 'map' of undefined",
						"culprit": "app/components/UserList.tsx",
						"level": "error",
						"status": "unresolved",
						"count": "42",
						"userCount": 15,
						"firstSeen": "2024-01-15T10:30:00Z",
						"lastSeen": "2024-01-15T14:00:00Z",
						"metadata": {
							"type": "TypeError",
							"value": "Cannot read property 'map' of undefined"
						},
						"shortId": "APP-123",
						"project": {
							"id": "1",
							"name": "frontend",
							"slug": "frontend"
						}
					}
				}
			}`),
			expectErr: false,
			expectNil: false,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Equal(t, "12345", ni.ExternalID, "should parse external ID from sentry issue ID")
				require.Equal(t, "sentry", ni.Source, "source should be sentry")
				require.Equal(t, integrationID, ni.SourceIntegrationID, "should set integration ID")
				require.Equal(t, "TypeError: Cannot read property 'map' of undefined", ni.Title, "should parse issue title")
				require.Contains(t, ni.Description, "TypeError: Cannot read property 'map' of undefined", "description should contain metadata value")
				require.Contains(t, ni.Description, "Culprit: app/components/UserList.tsx", "description should contain culprit")
				require.Equal(t, "high", ni.Severity, "error level should map to high severity")
				require.Equal(t, 42, ni.OccurrenceCount, "should parse occurrence count from string")
				require.Equal(t, 15, ni.AffectedCustomerCount, "should parse user count")
				require.Contains(t, ni.Tags, "project:frontend", "tags should include project name")
			},
		},
		{
			name: "regression action parses fatal level as critical",
			payload: json.RawMessage(`{
				"action": "regression",
				"data": {
					"issue": {
						"id": "99999",
						"title": "NullPointerException",
						"level": "fatal",
						"count": "1",
						"userCount": 1,
						"firstSeen": "2024-01-15T10:30:00Z",
						"lastSeen": "2024-01-15T10:30:00Z",
						"metadata": {"type": "NullPointerException", "value": ""},
						"project": {"id": "1", "name": "api", "slug": "api"}
					}
				}
			}`),
			expectErr: false,
			expectNil: false,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Equal(t, "critical", ni.Severity, "fatal level should map to critical severity")
			},
		},
		{
			name: "resolved action is skipped and returns nil",
			payload: json.RawMessage(`{
				"action": "resolved",
				"data": {
					"issue": {
						"id": "12345",
						"title": "Some issue",
						"level": "error",
						"count": "1",
						"userCount": 0,
						"firstSeen": "2024-01-15T10:30:00Z",
						"lastSeen": "2024-01-15T10:30:00Z",
						"metadata": {},
						"project": {"id": "1", "name": "api", "slug": "api"}
					}
				}
			}`),
			expectErr: false,
			expectNil: true,
			checkResult: func(t *testing.T, ni *NormalizedIssue, integrationID uuid.UUID) {
				t.Helper()
				require.Nil(t, ni, "resolved events should be skipped")
			},
		},
		{
			name: "missing issue ID returns error",
			payload: json.RawMessage(`{
				"action": "created",
				"data": {
					"issue": {
						"title": "Missing ID issue"
					}
				}
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
			payload:   json.RawMessage(`not json`),
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

			adapter := NewSentryAdapter()
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

func TestMapSentryLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "fatal maps to critical", input: "fatal", expected: "critical"},
		{name: "error maps to high", input: "error", expected: "high"},
		{name: "warning maps to medium", input: "warning", expected: "medium"},
		{name: "info maps to low", input: "info", expected: "low"},
		{name: "unknown maps to medium", input: "unknown", expected: "medium"},
		{name: "empty string maps to medium", input: "", expected: "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, MapSentryLevel(tt.input), "MapSentryLevel should return expected severity")
		})
	}
}
