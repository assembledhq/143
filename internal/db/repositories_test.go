package db

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStore_DisconnectByGitHubID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)

	mock.ExpectExec("UPDATE repositories SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.DisconnectByGitHubID(context.Background(), 12345, 67890)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
