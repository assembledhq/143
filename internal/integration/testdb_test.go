//go:build integration

package integration

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTruncatedTablesIncludesAgentCapabilityTables(t *testing.T) {
	t.Parallel()

	policyIndex := slices.Index(truncatedTables, "agent_capability_policies")
	grantIndex := slices.Index(truncatedTables, "agent_capability_policy_grants")

	require.NotEqual(t, -1, policyIndex, "integration reset should explicitly truncate agent capability policies")
	require.NotEqual(t, -1, grantIndex, "integration reset should explicitly truncate agent capability policy grants")
	require.Less(t, grantIndex, policyIndex, "integration reset should truncate capability grants before policies")
}
