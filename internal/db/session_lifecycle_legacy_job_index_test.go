package db

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleLegacyJobIndexMatchesConsumerPolicy(t *testing.T) {
	t.Parallel()
	migration, err := os.ReadFile("../../migrations/000379_legacy_input_consumer_jobs_index.up.sql")
	require.NoError(t, err, "read the actual standalone concurrent index migration rather than duplicating its installed policy")
	var statements []string
	for line := range strings.SplitSeq(string(migration), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			statements = append(statements, line)
		}
	}
	types := "job_type IN ('run_agent','continue_session')"
	statuses := "status IN " + jobNonterminalInputConsumerStatusesSQL
	// The tenancy audit reads each SQL literal, so the production query and
	// migration inline this key; this test is their single policy coupling.
	lifecycleConsumerLegacyInputTenantSQL := "(CASE WHEN " + types + " AND " + statuses + " THEN org_id END)"
	expected := "CREATE INDEX CONCURRENTLY idx_jobs_lifecycle_nonterminal_input_consumers ON jobs(" + lifecycleConsumerLegacyInputTenantSQL + ",id) WHERE " + lifecycleConsumerLegacyInputTenantSQL + " IS NOT NULL;"
	require.Equal(t, strings.Join(strings.Fields(expected), " "), strings.Join(strings.Fields(strings.Join(statements, "\n")), " "),
		"the partial index must stay coupled to the exact two input job types and the shared nonterminal status policy, including parked consumers")
	require.Contains(t, strings.Join(strings.Fields(lifecycleConsumerLegacyJobsQuery), " "), "WHERE "+lifecycleConsumerLegacyInputTenantSQL+"=$1 ORDER BY id LIMIT $2",
		"eligible selection must constrain only the exact indexed eligible-tenant key, leaving no history index to combine")
	require.Contains(t, lifecycleConsumerLegacyInputTenantSQL, "THEN org_id END",
		"the indexed key must equal the tenant only for eligible rows")
	require.Contains(t, lifecycleConsumerLegacyJobsQuery, types,
		"the actual production legacy query must select exactly the job types supported by the partial index")
	require.Contains(t, lifecycleConsumerLegacyJobsQuery, statuses,
		"the actual production legacy query and migration must share the same retained-consumer status policy")
}
