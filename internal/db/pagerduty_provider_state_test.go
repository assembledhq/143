package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPagerDutyProviderStateStore_UpsertBySessionIssue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	linkID := uuid.New()
	store := NewPagerDutyProviderStateStore(mock)

	mock.ExpectQuery("WITH link AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"link_id"}).AddRow(linkID))

	err = store.UpsertBySessionIssue(context.Background(), orgID, sessionID, issueID, PagerDutyProviderState{
		IncidentID:       "PABC123",
		IncidentURL:      "https://example.pagerduty.com/incidents/PABC123",
		ServiceID:        "PSVC",
		ServiceName:      "API",
		TriggerEventID:   "evt-123",
		WritebackNoteIDs: []string{"note-1"},
	})

	require.NoError(t, err, "UpsertBySessionIssue should upsert state by the session issue link identity")
	require.NoError(t, mock.ExpectationsWereMet(), "all provider-state expectations should be met")
}
