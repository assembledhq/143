package ingestion

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinearAdapter_ParseWebhook_Create(t *testing.T) {
	adapter := NewLinearAdapter()
	integrationID := uuid.New()

	payload := json.RawMessage(`{
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
	}`)

	ni, err := adapter.ParseWebhook(integrationID, payload)
	require.NoError(t, err)
	require.NotNil(t, ni)

	assert.Equal(t, "lin-123", ni.ExternalID)
	assert.Equal(t, "linear", ni.Source)
	assert.Equal(t, integrationID, ni.SourceIntegrationID)
	assert.Equal(t, "ENG-456: Fix authentication timeout", ni.Title)
	assert.Equal(t, "Users report session expiring too quickly", ni.Description)
	assert.Equal(t, "critical", ni.Severity) // priority 1 -> critical
	assert.Equal(t, 1, ni.OccurrenceCount)
	assert.Contains(t, ni.Tags, "bug")
	assert.Contains(t, ni.Tags, "auth")
	assert.Contains(t, ni.Tags, "team:ENG")
}

func TestLinearAdapter_ParseWebhook_Update(t *testing.T) {
	adapter := NewLinearAdapter()

	payload := json.RawMessage(`{
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
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	require.NoError(t, err)
	require.NotNil(t, ni)
	assert.Equal(t, "high", ni.Severity) // priority 2 -> high
}

func TestLinearAdapter_ParseWebhook_SkipsNonIssueType(t *testing.T) {
	adapter := NewLinearAdapter()

	payload := json.RawMessage(`{
		"action": "create",
		"type": "Comment",
		"data": {"id": "comment-123"}
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	assert.NoError(t, err)
	assert.Nil(t, ni, "non-Issue types should be skipped")
}

func TestLinearAdapter_ParseWebhook_SkipsDeleteAction(t *testing.T) {
	adapter := NewLinearAdapter()

	payload := json.RawMessage(`{
		"action": "remove",
		"type": "Issue",
		"data": {"id": "lin-123", "title": "Deleted issue"}
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	assert.NoError(t, err)
	assert.Nil(t, ni, "delete actions should be skipped")
}

func TestLinearAdapter_ParseWebhook_MissingIssueID(t *testing.T) {
	adapter := NewLinearAdapter()

	payload := json.RawMessage(`{
		"action": "create",
		"type": "Issue",
		"data": {"title": "Missing ID"}
	}`)

	ni, err := adapter.ParseWebhook(uuid.New(), payload)
	assert.Error(t, err)
	assert.Nil(t, ni)
	assert.Contains(t, err.Error(), "missing issue ID")
}

func TestLinearAdapter_ParseWebhook_InvalidJSON(t *testing.T) {
	adapter := NewLinearAdapter()

	ni, err := adapter.ParseWebhook(uuid.New(), json.RawMessage(`{broken`))
	assert.Error(t, err)
	assert.Nil(t, ni)
}

func TestMapLinearPriority(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "medium"},   // No priority
		{1, "critical"}, // Urgent
		{2, "high"},
		{3, "medium"},
		{4, "low"},
		{5, "medium"}, // unknown
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("priority_%d", tt.input), func(t *testing.T) {
			assert.Equal(t, tt.expected, mapLinearPriority(tt.input))
		})
	}
}
