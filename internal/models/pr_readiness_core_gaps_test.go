package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPRReadinessCheckStatusValidateIncludesError(t *testing.T) {
	t.Parallel()

	require.NoError(t, PRReadinessCheckStatusError.Validate(), "custom prompt execution failures should be representable as readiness check errors")
}

func TestPRReadinessPolicyConfigResolution(t *testing.T) {
	t.Parallel()

	orgPolicy := DefaultPRReadinessPolicyConfig()
	orgPolicy.Checks[PRReadinessCheckTypeTestEvidencePresent] = PRReadinessCheckPolicy{
		Enforcement: PRReadinessEnforcementByRole{
			Builder:  PRReadinessEnforcementBlocking,
			Engineer: PRReadinessEnforcementAdvisory,
			Admin:    PRReadinessEnforcementAdvisory,
		},
	}
	repoPolicy := DefaultPRReadinessPolicyConfig()
	repoPolicy.Checks[PRReadinessCheckTypeRiskFlags] = PRReadinessCheckPolicy{
		Enforcement: PRReadinessEnforcementByRole{
			Builder:  PRReadinessEnforcementBlocking,
			Engineer: PRReadinessEnforcementAdvisory,
			Admin:    PRReadinessEnforcementAdvisory,
		},
	}
	repoPolicy.AutoRun.OnCreatePR = true

	resolved := ResolvePRReadinessPolicyConfig(&orgPolicy, &repoPolicy, nil)
	require.Equal(t, PRReadinessEnforcementBlocking, resolved.EffectivePolicy().EnforcementFor(RoleBuilder, PRReadinessCheckTypeRiskFlags), "repository policy should override org policy for the same repository")
	require.Equal(t, PRReadinessEnforcementAdvisory, resolved.EffectivePolicy().EnforcementFor(RoleBuilder, PRReadinessCheckTypeTestEvidencePresent), "repository policy should replace org check overrides rather than merge stale org settings")
	require.True(t, resolved.AutoRun.OnCreatePR, "repository policy should carry repository auto-run settings")

	legacyDisabled := false
	resolved = ResolvePRReadinessPolicyConfig(nil, nil, &legacyDisabled)
	require.False(t, resolved.EnabledForBuilders, "legacy builder review disable setting should keep builder blocking off until an explicit readiness policy exists")
	require.Equal(t, PRReadinessEnforcementOff, resolved.EffectivePolicy().EnforcementFor(RoleBuilder, PRReadinessCheckTypeAgentReviewClean), "disabled legacy compatibility should make builder checks advisory/off for enforcement")
}

func TestPRReadinessCheckEffectiveEnforcement(t *testing.T) {
	t.Parallel()

	check := PRReadinessCheck{
		CheckType: PRReadinessCheckTypeAgentReviewClean,
		Status:    PRReadinessCheckStatusFailed,
		EnforcementByRole: PRReadinessEnforcementByRole{
			Builder:  PRReadinessEnforcementBlocking,
			Engineer: PRReadinessEnforcementAdvisory,
			Admin:    PRReadinessEnforcementOff,
		},
	}

	builder := check.WithEffectiveRole(RoleBuilder)
	require.Equal(t, PRReadinessEnforcementBlocking, builder.EffectiveEnforcement, "builders should see blocking enforcement for builder-blocking checks")
	require.True(t, builder.BlocksCurrentRole(), "failed blocking checks should block the current role")

	engineer := check.WithEffectiveRole(RoleMember)
	require.Equal(t, PRReadinessEnforcementAdvisory, engineer.EffectiveEnforcement, "engineers should see advisory enforcement for the same factual check")
	require.False(t, engineer.BlocksCurrentRole(), "advisory failures should not block the current role")
}

func TestPRReadinessRunUnbypassedBlockingChecks(t *testing.T) {
	t.Parallel()

	run := PRReadinessRun{
		Checks: []PRReadinessCheck{
			{
				CheckKey:  "agent_review_clean",
				CheckType: PRReadinessCheckTypeAgentReviewClean,
				Status:    PRReadinessCheckStatusFailed,
				EnforcementByRole: PRReadinessEnforcementByRole{
					Builder: PRReadinessEnforcementBlocking,
				},
			},
			{
				CheckKey:  "risk_flags",
				CheckType: PRReadinessCheckTypeRiskFlags,
				Status:    PRReadinessCheckStatusWarning,
				EnforcementByRole: PRReadinessEnforcementByRole{
					Builder: PRReadinessEnforcementBlocking,
				},
			},
		},
		Bypasses: []PRReadinessBypass{
			{BypassedChecks: []string{"agent_review_clean"}},
		},
	}

	require.Empty(t, run.UnbypassedBlockingCheckKeys(RoleBuilder), "bypassed completed blockers should not block builder PR creation")

	run.Bypasses = nil
	require.Equal(t, []string{"agent_review_clean"}, run.UnbypassedBlockingCheckKeys(RoleBuilder), "unbypassed failed blocking checks should still block builder PR creation")
}

func TestPRReadinessPolicyConfigRequiresRoleReadiness(t *testing.T) {
	t.Parallel()

	cfg := DefaultPRReadinessPolicyConfig()
	require.True(t, cfg.RequiresRoleReadiness(RoleBuilder), "default builder policy should require readiness because at least one builder check blocks")
	require.True(t, cfg.RequiresRoleReadiness(RoleMember), "engineers should still have advisory readiness by default")

	legacyDisabled := false
	cfg = ResolvePRReadinessPolicyConfig(nil, nil, &legacyDisabled)
	require.False(t, cfg.RequiresRoleReadiness(RoleBuilder), "legacy-disabled builder policy should not require a readiness run")
	require.True(t, cfg.RequiresRoleReadiness(RoleMember), "legacy builder compatibility should not disable engineer advisory readiness")

	cfg = DefaultPRReadinessPolicyConfig()
	for checkType, check := range cfg.Checks {
		check.Enforcement.Builder = PRReadinessEnforcementOff
		check.Enforcement.Engineer = PRReadinessEnforcementOff
		check.Enforcement.Admin = PRReadinessEnforcementOff
		cfg.Checks[checkType] = check
	}
	require.False(t, cfg.RequiresRoleReadiness(RoleBuilder), "all-off policies should not require readiness for builders")
	require.False(t, cfg.ShouldEvaluateCheck(PRReadinessCheckTypeAgentReviewClean), "checks with all roles off should not be evaluated")
}

func TestPRReadinessPolicyConfigBypassPolicy(t *testing.T) {
	t.Parallel()

	cfg := DefaultPRReadinessPolicyConfig()
	cfg.Bypass = PRReadinessBypassPolicy{
		Enabled:             true,
		AllowedRoles:        []Role{RoleAdmin, RoleMember},
		Scopes:              []string{"completed_blocking_checks"},
		NonBypassableChecks: []string{"freshness", "security_scan"},
	}

	require.False(t, cfg.BypassAllowedFor(RoleBuilder), "builder bypass should be denied when builder is not in the configured role list")
	require.True(t, cfg.BypassAllowedFor(RoleMember), "engineer bypass should be allowed when the role is configured")
	require.True(t, cfg.IsCheckNonBypassable("freshness", PRReadinessCheckTypeFreshness), "configured built-in checks should be non-bypassable")
	require.True(t, cfg.IsCheckNonBypassable("security_scan", PRReadinessCheckTypeCustomPrompt), "configured custom check keys should be non-bypassable")
	require.False(t, cfg.IsCheckNonBypassable("agent_review_clean", PRReadinessCheckTypeAgentReviewClean), "checks not listed in policy should remain bypassable")

	cfg.Bypass.Enabled = false
	require.False(t, cfg.BypassAllowedFor(RoleAdmin), "disabled bypass policy should deny all roles")
}

func TestPRReadinessPolicyConfigValidateRejectsInvalidPolicyShape(t *testing.T) {
	t.Parallel()

	cfg := DefaultPRReadinessPolicyConfig()
	cfg.Checks[PRReadinessCheckType("unknown_check")] = PRReadinessCheckPolicy{
		Enforcement: PRReadinessEnforcementByRole{Builder: PRReadinessEnforcementAdvisory},
	}
	require.Error(t, cfg.Validate(), "policy validation should reject unknown readiness check keys")

	cfg = DefaultPRReadinessPolicyConfig()
	cfg.Checks[PRReadinessCheckTypeFreshness] = PRReadinessCheckPolicy{
		Enforcement: PRReadinessEnforcementByRole{Builder: PRReadinessEnforcement("invalid")},
	}
	require.Error(t, cfg.Validate(), "policy validation should reject invalid role enforcement values")

	cfg = DefaultPRReadinessPolicyConfig()
	cfg.Bypass.AllowedRoles = []Role{RoleViewer}
	require.Error(t, cfg.Validate(), "policy validation should reject bypass roles that cannot write pull requests")

	cfg = DefaultPRReadinessPolicyConfig()
	cfg.Bypass.Scopes = []string{"future_scope"}
	require.Error(t, cfg.Validate(), "policy validation should reject unknown bypass scopes")
}
