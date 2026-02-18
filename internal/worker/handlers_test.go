package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStores(t *testing.T) (*Stores, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	stores := &Stores{
		Issues:    db.NewIssueStore(mock),
		AgentRuns: db.NewAgentRunStore(mock),
		Jobs:      db.NewJobStore(mock),
	}
	return stores, mock
}

func TestIngestWebhookHandler_ValidPayload(t *testing.T) {
	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newIngestWebhookHandler(stores, logger)

	payload := json.RawMessage(`{"webhook_delivery_id":"abc-123","provider":"github"}`)
	err := handler(context.Background(), "ingest_webhook", payload)

	assert.NoError(t, err)
}

func TestIngestWebhookHandler_InvalidJSON(t *testing.T) {
	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newIngestWebhookHandler(stores, logger)

	payload := json.RawMessage(`{invalid json}`)
	err := handler(context.Background(), "ingest_webhook", payload)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestPrioritizeHandler_ValidPayload(t *testing.T) {
	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newPrioritizeHandler(stores, logger)

	issueID := uuid.New()
	payload := json.RawMessage(`{"issue_id":"` + issueID.String() + `"}`)
	err := handler(context.Background(), "prioritize", payload)

	assert.NoError(t, err)
}

func TestPrioritizeHandler_InvalidJSON(t *testing.T) {
	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newPrioritizeHandler(stores, logger)

	payload := json.RawMessage(`not json at all`)
	err := handler(context.Background(), "prioritize", payload)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestPrioritizeHandler_InvalidUUID(t *testing.T) {
	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newPrioritizeHandler(stores, logger)

	payload := json.RawMessage(`{"issue_id":"not-a-valid-uuid"}`)
	err := handler(context.Background(), "prioritize", payload)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse issue ID")
}

func TestWorker_Register(t *testing.T) {
	w := New(nil, zerolog.Nop(), "test-node")

	called := false
	handler := func(ctx context.Context, jobType string, payload json.RawMessage) error {
		called = true
		return nil
	}

	w.Register("test_job", handler)

	// Verify the handler is stored
	h, ok := w.handlers["test_job"]
	assert.True(t, ok)
	assert.NotNil(t, h)

	// Verify we can invoke it
	err := h(context.Background(), "test_job", nil)
	assert.NoError(t, err)
	assert.True(t, called)
}
