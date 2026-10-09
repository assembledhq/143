package db

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestIntegrationStoreConnectedProviderScan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fail    bool
		invalid bool
	}{
		{name: "provider and connected statuses are constrained"},
		{name: "query errors propagate", fail: true},
		{name: "invalid provider rejected", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated integration scan database")
			defer mock.Close()
			orgID := uuid.New()
			provider := models.IntegrationProviderLinear
			if tt.invalid {
				provider = "invalid"
			} else {
				expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT org_id FROM integrations WHERE provider = @provider AND status IN ('active', 'error') ORDER BY org_id")).WithArgs(pgx.NamedArgs{"provider": provider})
				if tt.fail {
					expectation.WillReturnError(errors.New("database unavailable"))
				} else {
					expectation.WillReturnRows(pgxmock.NewRows([]string{"org_id"}).AddRow(orgID))
				}
			}
			ids, err := NewIntegrationStore(mock).ListOrgsWithConnectedProvider(context.Background(), provider)
			if tt.fail || tt.invalid {
				require.Error(t, err, "invalid scans must not silently finish the daily health probe")
			} else {
				require.NoError(t, err, "connected provider scan should succeed")
				require.Equal(t, []uuid.UUID{orgID}, ids, "scan should return the matching organization identity")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "query must constrain provider and both connected statuses")
		})
	}
}

func TestIntegrationStoreConnectedProviderPostgres(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	maintenanceExec(t, pool, `ALTER TABLE integrations ADD COLUMN provider text NOT NULL DEFAULT 'github', ADD COLUMN status text NOT NULL DEFAULT 'active'`)
	activeID, errorID, githubID, inactiveID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{activeID, errorID, githubID, inactiveID} {
		maintenanceExec(t, pool, `INSERT INTO organizations(id) VALUES($1)`, id)
	}
	for _, row := range []struct {
		id               uuid.UUID
		provider, status string
	}{{activeID, "linear", "active"}, {activeID, "linear", "error"}, {errorID, "linear", "error"}, {githubID, "github", "active"}, {inactiveID, "linear", "inactive"}} {
		maintenanceExec(t, pool, `INSERT INTO integrations(id,org_id,provider,status) VALUES($1,$2,$3,$4)`, uuid.New(), row.id, row.provider, row.status)
	}
	ids, err := NewIntegrationStore(pool).ListOrgsWithConnectedProvider(context.Background(), models.IntegrationProviderLinear)
	require.NoError(t, err, "real SQL should select connected Linear organizations")
	expected := []uuid.UUID{activeID, errorID}
	sort.Slice(expected, func(i, j int) bool { return expected[i].String() < expected[j].String() })
	require.Equal(t, expected, ids, "active and errored Linear must appear once; GitHub-only and disconnected organizations must be excluded")
}
