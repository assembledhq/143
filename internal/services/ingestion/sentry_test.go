package ingestion

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentryAdapter_ParseWebhook_Created(t *testing.T) {
	adapter := NewSentryAdapter()
	integrationID := uuid.New()

	payload := json.RawMessage(`{
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
	}`)

	ni, err := adapter.ParseWebhook(integrationID, payload)
	require.NoError(t, err)
	require.NotNil(t, ni)

	assert.Equal(t, "12345", ni.ExternalID)
	assert.Equal(t, "sentry", ni.Source)
	assert.Equal(t, integrationID, ni.SourceIntegrationID)
	assert.Equal(t, "TypeError: Cannot read property 'map' of undefined", ni.Title)
	assert.Contains(t, ni.Description, "TypeError: Cannot read property 'map' of undefined")
	assert.Contains(t, ni.Description, "Culprit: app/components/UserList.tsx")
	assert.Equal(t, "high", ni.Severity) // error -> high
	assert.Equal(t, 42, ni.OccurrenceCount)
	assert.Equal(t, 15, ni.AffectedCustomerCount)
	assert.Contains(t, ni.Tags, "project:frontend")
}

func TestSentryAdapter_ParseWebhook_Regression(t *testing.T) {
	adapter := NewSentryAdapter()

	payload := json.RawMessage(`{
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
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	require.NoError(t, err)
	require.NotNil(t, ni)
	assert.Equal(t, "critical", ni.Severity) // fatal -> critical
}

func TestSentryAdapter_ParseWebhook_SkipsResolvedAction(t *testing.T) {
	adapter := NewSentryAdapter()

	payload := json.RawMessage(`{
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
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	assert.NoError(t, err)
	assert.Nil(t, ni, "resolved events should be skipped")
}

func TestSentryAdapter_ParseWebhook_MissingIssueID(t *testing.T) {
	adapter := NewSentryAdapter()

	payload := json.RawMessage(`{
		"action": "created",
		"data": {
			"issue": {
				"title": "Missing ID issue"
			}
		}
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	assert.Error(t, err)
	assert.Nil(t, ni)
	assert.Contains(t, err.Error(), "missing issue ID")
}

func TestSentryAdapter_ParseWebhook_InvalidJSON(t *testing.T) {
	adapter := NewSentryAdapter()

	ni, err := adapter.ParseWebhook(uuid.New(), json.RawMessage(`not json`))
	assert.Error(t, err)
	assert.Nil(t, ni)
}

func TestMapSentryLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"fatal", "critical"},
		{"error", "high"},
		{"warning", "medium"},
		{"info", "low"},
		{"unknown", "medium"},
		{"", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapSentryLevel(tt.input))
		})
	}
}
