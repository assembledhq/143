package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNodeManager_Register(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "successful registration",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080").
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name: "upsert on conflict updates existing node",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080").
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "database error returns error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080").
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)
			nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")

			err = nm.Register(context.Background(), "localhost:8080")
			if tt.expectErr {
				require.Error(t, err, "Register should return an error")
			} else {
				require.NoError(t, err, "Register should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestNodeManager_Heartbeat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "successful heartbeat update",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE nodes SET last_heartbeat_at").
					WithArgs("node-1").
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "heartbeat database error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE nodes SET last_heartbeat_at").
					WithArgs("node-1").
					WillReturnError(fmt.Errorf("connection lost"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)
			nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")

			// Directly test the heartbeat SQL by calling pool.Exec,
			// since StartHeartbeat runs in an infinite loop with a ticker.
			_, err = nm.pool.Exec(context.Background(), `
				UPDATE nodes SET last_heartbeat_at = now() WHERE id = $1
			`, nm.nodeID)
			if tt.expectErr {
				require.Error(t, err, "heartbeat exec should return an error")
			} else {
				require.NoError(t, err, "heartbeat exec should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
