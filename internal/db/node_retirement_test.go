package db

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestNodeRetirementSealValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		params SealNodeRetirementParams
		begin  bool
	}{
		{name: "missing node", params: SealNodeRetirementParams{DeployID: "attempt", BuildSHA: "sha"}},
		{name: "missing deployment", params: SealNodeRetirementParams{NodeID: "worker", BuildSHA: "sha"}},
		{name: "missing build", params: SealNodeRetirementParams{NodeID: "worker", DeployID: "attempt"}},
		{name: "unknown transaction", params: SealNodeRetirementParams{NodeID: "worker", DeployID: "attempt", BuildSHA: "sha"}, begin: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated retirement database mock")
			defer mock.Close()
			if tt.begin {
				mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))
			}
			seal, err := NewNodeRetirementStore(mock).Seal(context.Background(), tt.params)
			require.Error(t, err, "incomplete or unavailable evidence must refuse retirement")
			require.NotErrorIs(t, err, ErrNodeRetirementBusy, "unknown evidence must not be classified as verified busy")
			require.Equal(t, NodeRetirementSeal{}, seal, "refusal must never return a seal receipt")
			require.NoError(t, mock.ExpectationsWereMet(), "validation must not perform unexpected database work")
		})
	}
}
