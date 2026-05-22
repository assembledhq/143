package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearAgentPromptedMessageStore_Reserve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(mock pgxmock.PgxPoolIface, orgID, rowID, sessionID uuid.UUID)
		expected bool
	}{
		{
			name: "returns true for first comment reservation",
			setup: func(mock pgxmock.PgxPoolIface, orgID, rowID, sessionID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO linear_agent_prompted_messages").
					WithArgs(orgID, rowID, sessionID, "comment_1").
					WillReturnRows(pgxmock.NewRows([]string{"inserted"}).AddRow(true))
			},
			expected: true,
		},
		{
			name: "returns false when comment was already recorded",
			setup: func(mock pgxmock.PgxPoolIface, orgID, rowID, sessionID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO linear_agent_prompted_messages").
					WithArgs(orgID, rowID, sessionID, "comment_1").
					WillReturnError(pgx.ErrNoRows)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create pgx mock")
			defer mock.Close()

			orgID := uuid.New()
			rowID := uuid.New()
			sessionID := uuid.New()
			tt.setup(mock, orgID, rowID, sessionID)

			inserted, err := NewLinearAgentPromptedMessageStore(mock).Reserve(context.Background(), orgID, rowID, sessionID, "comment_1")
			require.NoError(t, err, "Reserve should not error for first insert or duplicate comment")
			require.Equal(t, tt.expected, inserted, "Reserve should report whether the prompted comment was newly recorded")
			require.NoError(t, mock.ExpectationsWereMet(), "all prompted-message reservation expectations should be met")
		})
	}
}
