package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCollectPullRequestFeedbackHandlerShadowMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   any
		setupMock func(pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name:    "observes pending items without claiming them",
			payload: collectPullRequestFeedbackPayload{OrgID: uuid.New(), PullRequestID: uuid.New()},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT COUNT.*pull_request_feedback_items").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"pending", "needs_attention"}).AddRow(2, 1))
			},
		},
		{name: "rejects invalid JSON", payload: json.RawMessage(`{`), setupMock: func(pgxmock.PgxPoolIface) {}, expectErr: true},
		{name: "rejects missing identifiers", payload: collectPullRequestFeedbackPayload{}, setupMock: func(pgxmock.PgxPoolIface) {}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			defer mock.Close()
			tt.setupMock(mock)
			var payload []byte
			if raw, ok := tt.payload.(json.RawMessage); ok {
				payload = raw
			} else {
				payload, err = json.Marshal(tt.payload)
				require.NoError(t, err, "test payload should marshal")
			}
			handler := newCollectPullRequestFeedbackHandler(&Stores{PullRequestFeedback: db.NewPullRequestFeedbackStore(mock)}, nil, zerolog.Nop())
			err = handler(context.Background(), "collect_pull_request_feedback", payload)
			if tt.expectErr {
				require.Error(t, err, "invalid shadow collection input should fail")
				return
			}
			require.NoError(t, err, "shadow collection should observe pending items")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
