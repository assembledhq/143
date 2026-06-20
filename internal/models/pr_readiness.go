package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PRReadinessRunStatus string

const (
	PRReadinessRunStatusQueued   PRReadinessRunStatus = "queued"
	PRReadinessRunStatusRunning  PRReadinessRunStatus = "running"
	PRReadinessRunStatusPassed   PRReadinessRunStatus = "passed"
	PRReadinessRunStatusWarnings PRReadinessRunStatus = "warnings"
	PRReadinessRunStatusBlocked  PRReadinessRunStatus = "blocked"
	PRReadinessRunStatusFailed   PRReadinessRunStatus = "failed"
)

func (s PRReadinessRunStatus) Validate() error {
	switch s {
	case PRReadinessRunStatusQueued, PRReadinessRunStatusRunning, PRReadinessRunStatusPassed,
		PRReadinessRunStatusWarnings, PRReadinessRunStatusBlocked, PRReadinessRunStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessRunStatus: %q", s)
	}
}

type PRReadinessCheckStatus string

const (
	PRReadinessCheckStatusPassed  PRReadinessCheckStatus = "passed"
	PRReadinessCheckStatusWarning PRReadinessCheckStatus = "warning"
	PRReadinessCheckStatusFailed  PRReadinessCheckStatus = "failed"
	PRReadinessCheckStatusSkipped PRReadinessCheckStatus = "skipped"
	PRReadinessCheckStatusError   PRReadinessCheckStatus = "error"
)

func (s PRReadinessCheckStatus) Validate() error {
	switch s {
	case PRReadinessCheckStatusPassed, PRReadinessCheckStatusWarning, PRReadinessCheckStatusFailed, PRReadinessCheckStatusSkipped, PRReadinessCheckStatusError:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessCheckStatus: %q", s)
	}
}

type PRReadinessCheckType string

const (
	PRReadinessCheckTypeFreshness             PRReadinessCheckType = "freshness"
	PRReadinessCheckTypeAgentReviewClean      PRReadinessCheckType = "agent_review_clean"
	PRReadinessCheckTypeDiffCollected         PRReadinessCheckType = "diff_collected"
	PRReadinessCheckTypeTestEvidencePresent   PRReadinessCheckType = "test_evidence_present"
	PRReadinessCheckTypeRiskFlags             PRReadinessCheckType = "risk_flags"
	PRReadinessCheckTypeDependencyConfigRisk  PRReadinessCheckType = "dependency_config_risk"
	PRReadinessCheckTypeGeneratedFileChurn    PRReadinessCheckType = "generated_file_churn"
	PRReadinessCheckTypeContextComplete       PRReadinessCheckType = "context_complete"
	PRReadinessCheckTypeReviewPacketDraftable PRReadinessCheckType = "review_packet_draftable"
	PRReadinessCheckTypeCustomPrompt          PRReadinessCheckType = "custom_prompt"
)

func (t PRReadinessCheckType) Validate() error {
	switch t {
	case PRReadinessCheckTypeFreshness, PRReadinessCheckTypeAgentReviewClean, PRReadinessCheckTypeDiffCollected,
		PRReadinessCheckTypeTestEvidencePresent, PRReadinessCheckTypeRiskFlags, PRReadinessCheckTypeDependencyConfigRisk,
		PRReadinessCheckTypeGeneratedFileChurn, PRReadinessCheckTypeContextComplete, PRReadinessCheckTypeReviewPacketDraftable,
		PRReadinessCheckTypeCustomPrompt:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessCheckType: %q", t)
	}
}

type PRReadinessEnforcement string

const (
	PRReadinessEnforcementOff      PRReadinessEnforcement = "off"
	PRReadinessEnforcementAdvisory PRReadinessEnforcement = "advisory"
	PRReadinessEnforcementBlocking PRReadinessEnforcement = "blocking"
)

func (e PRReadinessEnforcement) Validate() error {
	switch e {
	case PRReadinessEnforcementOff, PRReadinessEnforcementAdvisory, PRReadinessEnforcementBlocking:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessEnforcement: %q", e)
	}
}

type PRReadinessPolicy struct {
	Builder  map[PRReadinessCheckType]PRReadinessEnforcement `json:"builder,omitempty"`
	Engineer map[PRReadinessCheckType]PRReadinessEnforcement `json:"engineer,omitempty"`
	Admin    map[PRReadinessCheckType]PRReadinessEnforcement `json:"admin,omitempty"`
}

type PRReadinessEnforcementByRole struct {
	Builder  PRReadinessEnforcement `db:"enforcement_builder" json:"builder,omitempty"`
	Engineer PRReadinessEnforcement `db:"enforcement_engineer" json:"engineer,omitempty"`
	Admin    PRReadinessEnforcement `db:"enforcement_admin" json:"admin,omitempty"`
}

func (e PRReadinessEnforcementByRole) EnforcementFor(role Role) PRReadinessEnforcement {
	switch role {
	case RoleBuilder:
		return normalizePRReadinessEnforcement(e.Builder)
	case RoleMember:
		return normalizePRReadinessEnforcement(e.Engineer)
	case RoleAdmin:
		return normalizePRReadinessEnforcement(e.Admin)
	default:
		return PRReadinessEnforcementOff
	}
}

func (e PRReadinessEnforcementByRole) Validate() error {
	for _, value := range []PRReadinessEnforcement{e.Builder, e.Engineer, e.Admin} {
		if err := normalizePRReadinessEnforcement(value).Validate(); err != nil {
			return err
		}
	}
	return nil
}

func normalizePRReadinessEnforcement(value PRReadinessEnforcement) PRReadinessEnforcement {
	if value == "" {
		return PRReadinessEnforcementOff
	}
	return value
}

type PRReadinessCheckPolicy struct {
	Enforcement PRReadinessEnforcementByRole `json:"enforcement,omitempty"`
}

type PRReadinessBypassPolicy struct {
	Enabled             bool     `json:"enabled"`
	AllowedRoles        []Role   `json:"allowed_roles,omitempty"`
	Scopes              []string `json:"scopes,omitempty"`
	NonBypassableChecks []string `json:"non_bypassable_checks,omitempty"`
}

type PRReadinessAutoRunPolicy struct {
	AfterSessionCompletion bool `json:"after_session_completion"`
	OnCreatePR             bool `json:"on_create_pr"`
}

type PRReadinessPolicyConfig struct {
	EnabledForBuilders        bool                                            `json:"enabled_for_builders"`
	Checks                    map[PRReadinessCheckType]PRReadinessCheckPolicy `json:"checks,omitempty"`
	Bypass                    PRReadinessBypassPolicy                         `json:"bypass,omitempty"`
	AutoRun                   PRReadinessAutoRunPolicy                        `json:"auto_run,omitempty"`
	SensitivePaths            []string                                        `json:"sensitive_paths,omitempty"`
	GeneratedFileAllowedPaths []string                                        `json:"generated_file_allowed_paths,omitempty"`
	LargeDiffFileThreshold    int                                             `json:"large_diff_file_threshold,omitempty"`
	LargeDiffLineThreshold    int                                             `json:"large_diff_line_threshold,omitempty"`
}

const (
	DefaultPRReadinessLargeDiffFileThreshold = 25
	DefaultPRReadinessLargeDiffLineThreshold = 500
	PRReadinessBypassScopeCompletedBlocking  = "completed_blocking_checks"
)

func DefaultPRReadinessPolicy() PRReadinessPolicy {
	return DefaultPRReadinessPolicyConfig().EffectivePolicy()
}

func DefaultPRReadinessPolicyConfig() PRReadinessPolicyConfig {
	advisory := map[PRReadinessCheckType]PRReadinessEnforcement{
		PRReadinessCheckTypeAgentReviewClean:      PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeDiffCollected:         PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeTestEvidencePresent:   PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeRiskFlags:             PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeDependencyConfigRisk:  PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeGeneratedFileChurn:    PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeContextComplete:       PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeReviewPacketDraftable: PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeFreshness:             PRReadinessEnforcementAdvisory,
	}
	builder := clonePRReadinessPolicyMap(advisory)
	builder[PRReadinessCheckTypeAgentReviewClean] = PRReadinessEnforcementBlocking
	builder[PRReadinessCheckTypeFreshness] = PRReadinessEnforcementBlocking
	return PRReadinessPolicyConfig{
		EnabledForBuilders: true,
		Checks:             policyConfigChecksFromPolicy(PRReadinessPolicy{Builder: builder, Engineer: advisory, Admin: clonePRReadinessPolicyMap(advisory)}),
		Bypass: PRReadinessBypassPolicy{
			Enabled:      true,
			AllowedRoles: []Role{RoleAdmin, RoleMember, RoleBuilder},
			Scopes:       []string{PRReadinessBypassScopeCompletedBlocking},
		},
		AutoRun:                   PRReadinessAutoRunPolicy{},
		SensitivePaths:            defaultPRReadinessSensitivePaths(),
		GeneratedFileAllowedPaths: []string{},
		LargeDiffFileThreshold:    DefaultPRReadinessLargeDiffFileThreshold,
		LargeDiffLineThreshold:    DefaultPRReadinessLargeDiffLineThreshold,
	}
}

func defaultPRReadinessSensitivePaths() []string {
	return []string{
		"*auth*",
		"*security*",
		"*billing*",
		".github/workflows/**",
		"deploy/**",
		"infra/**",
		"terraform/**",
	}
}

func policyConfigChecksFromPolicy(policy PRReadinessPolicy) map[PRReadinessCheckType]PRReadinessCheckPolicy {
	checks := make(map[PRReadinessCheckType]PRReadinessCheckPolicy)
	for _, checkType := range allPRReadinessCheckTypes() {
		checks[checkType] = PRReadinessCheckPolicy{
			Enforcement: PRReadinessEnforcementByRole{
				Builder:  policy.EnforcementFor(RoleBuilder, checkType),
				Engineer: policy.EnforcementFor(RoleMember, checkType),
				Admin:    policy.EnforcementFor(RoleAdmin, checkType),
			},
		}
	}
	return checks
}

func allPRReadinessCheckTypes() []PRReadinessCheckType {
	return []PRReadinessCheckType{
		PRReadinessCheckTypeFreshness,
		PRReadinessCheckTypeAgentReviewClean,
		PRReadinessCheckTypeDiffCollected,
		PRReadinessCheckTypeTestEvidencePresent,
		PRReadinessCheckTypeRiskFlags,
		PRReadinessCheckTypeDependencyConfigRisk,
		PRReadinessCheckTypeGeneratedFileChurn,
		PRReadinessCheckTypeContextComplete,
		PRReadinessCheckTypeReviewPacketDraftable,
		PRReadinessCheckTypeCustomPrompt,
	}
}

func ResolvePRReadinessPolicyConfig(orgPolicy, repoPolicy *PRReadinessPolicyConfig, legacyRequireReviewBeforePR *bool) PRReadinessPolicyConfig {
	switch {
	case repoPolicy != nil:
		return repoPolicy.normalized()
	case orgPolicy != nil:
		return orgPolicy.normalized()
	default:
		cfg := DefaultPRReadinessPolicyConfig()
		if legacyRequireReviewBeforePR != nil && !*legacyRequireReviewBeforePR {
			cfg.EnabledForBuilders = false
			for checkType, check := range cfg.Checks {
				check.Enforcement.Builder = PRReadinessEnforcementOff
				cfg.Checks[checkType] = check
			}
		}
		return cfg.normalized()
	}
}

func (c PRReadinessPolicyConfig) normalized() PRReadinessPolicyConfig {
	defaults := DefaultPRReadinessPolicyConfig()
	if c.Checks == nil {
		c.Checks = defaults.Checks
	}
	if c.LargeDiffFileThreshold <= 0 {
		c.LargeDiffFileThreshold = defaults.LargeDiffFileThreshold
	}
	if c.LargeDiffLineThreshold <= 0 {
		c.LargeDiffLineThreshold = defaults.LargeDiffLineThreshold
	}
	if c.SensitivePaths == nil {
		c.SensitivePaths = defaults.SensitivePaths
	}
	if !c.Bypass.Enabled && len(c.Bypass.AllowedRoles) == 0 && len(c.Bypass.Scopes) == 0 && len(c.Bypass.NonBypassableChecks) == 0 {
		c.Bypass = defaults.Bypass
	}
	return c
}

func (c PRReadinessPolicyConfig) Validate() error {
	c = c.normalized()
	for checkType, check := range c.Checks {
		if err := checkType.Validate(); err != nil {
			return fmt.Errorf("checks.%s: %w", checkType, err)
		}
		if err := check.Enforcement.Validate(); err != nil {
			return fmt.Errorf("checks.%s.enforcement: %w", checkType, err)
		}
	}
	for i, role := range c.Bypass.AllowedRoles {
		switch role {
		case RoleAdmin, RoleMember, RoleBuilder:
		default:
			return fmt.Errorf("bypass.allowed_roles[%d]: invalid bypass role %q", i, role)
		}
	}
	for i, scope := range c.Bypass.Scopes {
		if strings.TrimSpace(scope) != PRReadinessBypassScopeCompletedBlocking {
			return fmt.Errorf("bypass.scopes[%d]: invalid bypass scope %q", i, scope)
		}
	}
	for i, check := range c.Bypass.NonBypassableChecks {
		if strings.TrimSpace(check) == "" {
			return fmt.Errorf("bypass.non_bypassable_checks[%d]: check key is required", i)
		}
	}
	for i, pattern := range c.SensitivePaths {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("sensitive_paths[%d]: path pattern is required", i)
		}
	}
	for i, pattern := range c.GeneratedFileAllowedPaths {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("generated_file_allowed_paths[%d]: path pattern is required", i)
		}
	}
	return nil
}

func (c PRReadinessPolicyConfig) EffectivePolicy() PRReadinessPolicy {
	c = c.normalized()
	policy := PRReadinessPolicy{
		Builder:  make(map[PRReadinessCheckType]PRReadinessEnforcement, len(c.Checks)),
		Engineer: make(map[PRReadinessCheckType]PRReadinessEnforcement, len(c.Checks)),
		Admin:    make(map[PRReadinessCheckType]PRReadinessEnforcement, len(c.Checks)),
	}
	for checkType, check := range c.Checks {
		policy.Builder[checkType] = check.Enforcement.EnforcementFor(RoleBuilder)
		policy.Engineer[checkType] = check.Enforcement.EnforcementFor(RoleMember)
		policy.Admin[checkType] = check.Enforcement.EnforcementFor(RoleAdmin)
	}
	if !c.EnabledForBuilders {
		for checkType := range policy.Builder {
			policy.Builder[checkType] = PRReadinessEnforcementOff
		}
	}
	return policy
}

func (c PRReadinessPolicyConfig) RequiresRoleReadiness(role Role) bool {
	c = c.normalized()
	policy := c.EffectivePolicy()
	for checkType := range c.Checks {
		if policy.EnforcementFor(role, checkType) != PRReadinessEnforcementOff {
			return true
		}
	}
	return false
}

func (c PRReadinessPolicyConfig) ShouldEvaluateCheck(checkType PRReadinessCheckType) bool {
	c = c.normalized()
	check, ok := c.Checks[checkType]
	if !ok {
		return false
	}
	return check.Enforcement.EnforcementFor(RoleBuilder) != PRReadinessEnforcementOff ||
		check.Enforcement.EnforcementFor(RoleMember) != PRReadinessEnforcementOff ||
		check.Enforcement.EnforcementFor(RoleAdmin) != PRReadinessEnforcementOff
}

func (c PRReadinessPolicyConfig) BypassAllowedFor(role Role) bool {
	c = c.normalized()
	if !c.Bypass.Enabled {
		return false
	}
	hasCompletedBlockingScope := false
	for _, scope := range c.Bypass.Scopes {
		if strings.TrimSpace(scope) == PRReadinessBypassScopeCompletedBlocking {
			hasCompletedBlockingScope = true
			break
		}
	}
	if !hasCompletedBlockingScope {
		return false
	}
	for _, allowed := range c.Bypass.AllowedRoles {
		if allowed == role {
			return true
		}
	}
	return false
}

func (c PRReadinessPolicyConfig) IsCheckNonBypassable(checkKey string, checkType PRReadinessCheckType) bool {
	c = c.normalized()
	checkKey = strings.TrimSpace(checkKey)
	for _, configured := range c.Bypass.NonBypassableChecks {
		configured = strings.TrimSpace(configured)
		if configured == "" {
			continue
		}
		if configured == checkKey || configured == string(checkType) {
			return true
		}
	}
	return false
}

func clonePRReadinessPolicyMap(in map[PRReadinessCheckType]PRReadinessEnforcement) map[PRReadinessCheckType]PRReadinessEnforcement {
	out := make(map[PRReadinessCheckType]PRReadinessEnforcement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (p PRReadinessPolicy) EnforcementFor(role Role, checkType PRReadinessCheckType) PRReadinessEnforcement {
	var values map[PRReadinessCheckType]PRReadinessEnforcement
	switch role {
	case RoleBuilder:
		values = p.Builder
	case RoleMember:
		values = p.Engineer
	case RoleAdmin:
		values = p.Admin
	default:
		return PRReadinessEnforcementOff
	}
	if len(values) == 0 {
		return DefaultPRReadinessPolicy().EnforcementFor(role, checkType)
	}
	if value, ok := values[checkType]; ok {
		return value
	}
	return PRReadinessEnforcementOff
}

func (p PRReadinessPolicy) ShouldEvaluateCheck(checkType PRReadinessCheckType) bool {
	return p.EnforcementFor(RoleBuilder, checkType) != PRReadinessEnforcementOff ||
		p.EnforcementFor(RoleMember, checkType) != PRReadinessEnforcementOff ||
		p.EnforcementFor(RoleAdmin, checkType) != PRReadinessEnforcementOff
}

type PRReadinessRun struct {
	ID                         uuid.UUID            `db:"id" json:"id"`
	OrgID                      uuid.UUID            `db:"org_id" json:"org_id"`
	SessionID                  uuid.UUID            `db:"session_id" json:"session_id"`
	RepositoryID               *uuid.UUID           `db:"repository_id" json:"repository_id,omitempty"`
	Status                     PRReadinessRunStatus `db:"status" json:"status"`
	EvaluatedWorkspaceRevision int64                `db:"evaluated_workspace_revision" json:"evaluated_workspace_revision"`
	EvaluatedSnapshotKey       *string              `db:"evaluated_snapshot_key" json:"evaluated_snapshot_key,omitempty"`
	Summary                    string               `db:"summary" json:"summary,omitempty"`
	ReviewPacket               json.RawMessage      `db:"review_packet" json:"review_packet,omitempty"`
	TriggeredByUserID          *uuid.UUID           `db:"triggered_by_user_id" json:"triggered_by_user_id,omitempty"`
	StartedAt                  time.Time            `db:"started_at" json:"started_at"`
	CompletedAt                *time.Time           `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt                  time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt                  time.Time            `db:"updated_at" json:"updated_at"`
	Checks                     []PRReadinessCheck   `db:"-" json:"checks,omitempty"`
	Bypasses                   []PRReadinessBypass  `db:"-" json:"bypasses,omitempty"`
}

type PRReadinessCheck struct {
	ID                   uuid.UUID                    `db:"id" json:"id"`
	OrgID                uuid.UUID                    `db:"org_id" json:"org_id"`
	RunID                uuid.UUID                    `db:"run_id" json:"run_id"`
	SessionID            uuid.UUID                    `db:"session_id" json:"session_id"`
	CheckKey             string                       `db:"check_key" json:"check_key,omitempty"`
	CheckType            PRReadinessCheckType         `db:"check_type" json:"check_type"`
	Status               PRReadinessCheckStatus       `db:"status" json:"status"`
	Enforcement          PRReadinessEnforcement       `db:"enforcement" json:"enforcement"`
	EnforcementByRole    PRReadinessEnforcementByRole `db:"-" json:"enforcement_by_role,omitempty"`
	EnforcementBuilder   PRReadinessEnforcement       `db:"enforcement_builder" json:"-"`
	EnforcementEngineer  PRReadinessEnforcement       `db:"enforcement_engineer" json:"-"`
	EnforcementAdmin     PRReadinessEnforcement       `db:"enforcement_admin" json:"-"`
	EffectiveEnforcement PRReadinessEnforcement       `db:"-" json:"effective_enforcement,omitempty"`
	Provenance           string                       `db:"provenance" json:"provenance,omitempty"`
	Source               string                       `db:"source" json:"source,omitempty"`
	Title                string                       `db:"title" json:"title"`
	Summary              string                       `db:"summary" json:"summary"`
	Details              json.RawMessage              `db:"details" json:"details,omitempty"`
	Action               string                       `db:"action" json:"action,omitempty"`
	CreatedAt            time.Time                    `db:"created_at" json:"created_at"`
}

func (c PRReadinessCheck) WithEffectiveRole(role Role) PRReadinessCheck {
	if c.EnforcementByRole == (PRReadinessEnforcementByRole{}) {
		c.EnforcementByRole = PRReadinessEnforcementByRole{
			Builder:  firstNonZeroEnforcement(c.EnforcementBuilder, c.Enforcement),
			Engineer: c.EnforcementEngineer,
			Admin:    c.EnforcementAdmin,
		}
	}
	c.EffectiveEnforcement = c.EnforcementByRole.EnforcementFor(role)
	if c.EffectiveEnforcement == PRReadinessEnforcementOff && role == RoleBuilder && c.Enforcement != "" {
		c.EffectiveEnforcement = c.Enforcement
	}
	return c
}

func (c PRReadinessCheck) BlocksCurrentRole() bool {
	return c.EffectiveEnforcement == PRReadinessEnforcementBlocking &&
		(c.Status == PRReadinessCheckStatusFailed || c.Status == PRReadinessCheckStatusError)
}

func (r PRReadinessRun) UnbypassedBlockingCheckKeys(role Role) []string {
	bypassed := map[string]struct{}{}
	for _, bypass := range r.Bypasses {
		for _, checkKey := range bypass.BypassedChecks {
			bypassed[checkKey] = struct{}{}
		}
	}
	blocking := make([]string, 0)
	for _, check := range r.Checks {
		checkKey := check.CheckKey
		if checkKey == "" {
			checkKey = string(check.CheckType)
		}
		if _, ok := bypassed[checkKey]; ok {
			continue
		}
		enforcement := check.EnforcementByRole.EnforcementFor(role)
		if enforcement == PRReadinessEnforcementOff && role == RoleBuilder && check.Enforcement != "" {
			enforcement = check.Enforcement
		}
		if enforcement == PRReadinessEnforcementBlocking &&
			(check.Status == PRReadinessCheckStatusFailed || check.Status == PRReadinessCheckStatusError) {
			blocking = append(blocking, checkKey)
		}
	}
	return blocking
}

func firstNonZeroEnforcement(values ...PRReadinessEnforcement) PRReadinessEnforcement {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return PRReadinessEnforcementOff
}

type PRReadinessResponse struct {
	Latest *PRReadinessRun `json:"latest,omitempty"`
}

type PRReadinessPolicyRecord struct {
	ID              uuid.UUID               `db:"id" json:"id"`
	OrgID           uuid.UUID               `db:"org_id" json:"org_id"`
	RepositoryID    *uuid.UUID              `db:"repository_id" json:"repository_id,omitempty"`
	Config          PRReadinessPolicyConfig `db:"-" json:"config"`
	Active          bool                    `db:"active" json:"active"`
	CreatedByUserID *uuid.UUID              `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time               `db:"created_at" json:"created_at"`
}

type PRReadinessResolvedPolicy struct {
	Config       PRReadinessPolicyConfig  `json:"config"`
	Source       string                   `json:"source"`
	Policy       *PRReadinessPolicyRecord `json:"policy,omitempty"`
	BypassCounts *PRReadinessBypassCounts `json:"bypass_counts,omitempty"`
}

type PRReadinessBypass struct {
	ID               uuid.UUID  `db:"id" json:"id"`
	OrgID            uuid.UUID  `db:"org_id" json:"org_id"`
	ReadinessRunID   uuid.UUID  `db:"readiness_run_id" json:"readiness_run_id"`
	SessionID        uuid.UUID  `db:"session_id" json:"session_id"`
	RepositoryID     *uuid.UUID `db:"repository_id" json:"repository_id,omitempty"`
	PullRequestID    *uuid.UUID `db:"pull_request_id" json:"pull_request_id,omitempty"`
	BypassedByUserID uuid.UUID  `db:"bypassed_by_user_id" json:"bypassed_by_user_id"`
	Reason           string     `db:"reason" json:"reason"`
	BypassedChecks   []string   `db:"-" json:"bypassed_checks"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
}

type PRReadinessBypassCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type PRReadinessBypassCounts struct {
	Total        int64                    `json:"total"`
	ByRepository []PRReadinessBypassCount `json:"by_repository,omitempty"`
	ByUser       []PRReadinessBypassCount `json:"by_user,omitempty"`
	ByCheck      []PRReadinessBypassCount `json:"by_check,omitempty"`
}

type PRReadinessPathFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type PRReadinessCustomCheckSource string

const (
	PRReadinessCustomCheckSourceOrgSettings PRReadinessCustomCheckSource = "org_settings"
	PRReadinessCustomCheckSourceRepoConfig  PRReadinessCustomCheckSource = "repo_config"
)

type PRReadinessCustomCheck struct {
	ID              uuid.UUID                    `db:"id" json:"id"`
	OrgID           uuid.UUID                    `db:"org_id" json:"org_id"`
	RepositoryID    *uuid.UUID                   `db:"repository_id" json:"repository_id,omitempty"`
	CheckKey        string                       `db:"check_key" json:"check_key"`
	Name            string                       `db:"name" json:"name"`
	Prompt          string                       `db:"prompt" json:"prompt"`
	PathFilters     PRReadinessPathFilter        `db:"-" json:"paths,omitempty"`
	Enforcement     PRReadinessEnforcementByRole `db:"-" json:"enforcement,omitempty"`
	Source          PRReadinessCustomCheckSource `db:"source" json:"source"`
	Active          bool                         `db:"active" json:"active"`
	CreatedByUserID *uuid.UUID                   `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time                    `db:"created_at" json:"created_at"`
}

type PRReadinessContext struct {
	OrgID           uuid.UUID  `db:"org_id" json:"org_id"`
	SessionID       uuid.UUID  `db:"session_id" json:"session_id"`
	IssueLessReason string     `db:"issue_less_reason" json:"issue_less_reason"`
	CreatedByUserID *uuid.UUID `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	UpdatedByUserID *uuid.UUID `db:"updated_by_user_id" json:"updated_by_user_id,omitempty"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}
