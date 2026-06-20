package models

import "fmt"

// AuditActorType identifies who or what performed an audited action.
type AuditActorType string

const (
	AuditActorUser    AuditActorType = "user"
	AuditActorAgent   AuditActorType = "agent"
	AuditActorSystem  AuditActorType = "system"
	AuditActorWebhook AuditActorType = "webhook"
	AuditActorAPI     AuditActorType = "api_client"
)

func (t AuditActorType) Validate() error {
	switch t {
	case AuditActorUser, AuditActorAgent, AuditActorSystem, AuditActorWebhook, AuditActorAPI:
		return nil
	default:
		return fmt.Errorf("invalid AuditActorType: %q", t)
	}
}

// AuditAction identifies the specific action that was performed.
// Follows a resource.verb naming convention.
type AuditAction string

const (
	// Session actions
	AuditActionSessionCreated              AuditAction = "session.created"
	AuditActionSessionStarted              AuditAction = "session.started"
	AuditActionSessionCompleted            AuditAction = "session.completed"
	AuditActionSessionFailed               AuditAction = "session.failed"
	AuditActionSessionCancelled            AuditAction = "session.cancelled"
	AuditActionSessionStatusChanged        AuditAction = "session.status_changed"
	AuditActionSessionQuestionCreated      AuditAction = "session.question.created"
	AuditActionSessionQuestionAnswered     AuditAction = "session.question.answered"
	AuditActionSessionHumanInputAnswered   AuditAction = "session.human_input.answered"
	AuditActionSessionHumanInputCancelled  AuditAction = "session.human_input.cancelled"
	AuditActionSessionResumedLocally       AuditAction = "session.resumed_locally"
	AuditActionSessionReviewCommentCreated AuditAction = "session.review_comment.created"
	AuditActionSessionReviewCommentUpdated AuditAction = "session.review_comment.updated"
	AuditActionSessionReviewCommentDeleted AuditAction = "session.review_comment.deleted"
	AuditActionSessionPRRequested          AuditAction = "session.pr_requested"
	AuditActionSessionBranchRequested      AuditAction = "session.branch_requested"
	AuditActionSessionPRPushRequested      AuditAction = "session.pr_push_requested"
	AuditActionSessionRetried              AuditAction = "session.retried"
	AuditActionSessionArchived             AuditAction = "session.archived"
	AuditActionSessionUnarchived           AuditAction = "session.unarchived"
	AuditActionSessionPreviewLifetimeSet   AuditAction = "session.preview_lifetime_set"
	// AuditActionSessionThreadInboxReplayed is emitted when an operator
	// forces an unknown_delivery inbox entry back into the delivery loop —
	// the entry may already have reached the runtime, so the replay is a
	// dual-write decision worth a paper trail.
	AuditActionSessionThreadInboxReplayed       AuditAction = "session.thread.inbox_replayed"
	AuditActionSessionThreadCreatedByAgentTool  AuditAction = "session.thread.created_by_agent_tool"
	AuditActionSessionThreadMessagedByAgentTool AuditAction = "session.thread.messaged_by_agent_tool"

	// Project actions
	AuditActionProjectCreated        AuditAction = "project.created"
	AuditActionProjectUpdated        AuditAction = "project.updated"
	AuditActionProjectDeleted        AuditAction = "project.deleted"
	AuditActionProjectStarted        AuditAction = "project.started"
	AuditActionProjectCompleted      AuditAction = "project.completed"
	AuditActionProjectArchived       AuditAction = "project.archived"
	AuditActionProjectUnarchived     AuditAction = "project.unarchived"
	AuditActionProjectRunTriggered   AuditAction = "project.run_triggered"
	AuditActionProjectCycleCompleted AuditAction = "project.cycle_completed"
	AuditActionProjectTaskCreated    AuditAction = "project.task.created"
	AuditActionProjectTaskUpdated    AuditAction = "project.task.updated"
	AuditActionProjectTaskDeleted    AuditAction = "project.task.deleted"
	AuditActionProjectTaskRetried    AuditAction = "project.task.retried"

	// Automation actions
	AuditActionAutomationCreated                  AuditAction = "automation.created"
	AuditActionAutomationUpdated                  AuditAction = "automation.updated"
	AuditActionAutomationDeleted                  AuditAction = "automation.deleted"
	AuditActionAutomationPaused                   AuditAction = "automation.paused"
	AuditActionAutomationResumed                  AuditAction = "automation.resumed"
	AuditActionAutomationRunTriggered             AuditAction = "automation.run_triggered"
	AuditActionAutomationGoalImprovementRequested AuditAction = "automation.goal_improvement.requested"
	AuditActionAutomationGoalImprovementCompleted AuditAction = "automation.goal_improvement.completed"
	AuditActionAutomationGoalImprovementFailed    AuditAction = "automation.goal_improvement.failed"
	AuditActionAutomationGoalImprovementApplied   AuditAction = "automation.goal_improvement.applied"
	AuditActionAutomationGoalImprovementCanceled  AuditAction = "automation.goal_improvement.canceled"

	// Issue actions
	AuditActionIssueCreated       AuditAction = "issue.created"
	AuditActionIssueReprioritized AuditAction = "issue.reprioritized"

	// PM actions
	AuditActionPMAnalysisTriggered  AuditAction = "pm.analysis_triggered"
	AuditActionPMPlanCreated        AuditAction = "pm.plan_created"
	AuditActionPMDecisionMade       AuditAction = "pm.decision_made"
	AuditActionPMBootstrapTriggered AuditAction = "pm.bootstrap_triggered"
	AuditActionPMRefreshTriggered   AuditAction = "pm.refresh_triggered"
	AuditActionPMRefreshAccepted    AuditAction = "pm.refresh_accepted"
	AuditActionPMRefreshRejected    AuditAction = "pm.refresh_rejected"
	AuditActionPMDocumentUpdated    AuditAction = "pm_document.updated"
	AuditActionPMDocumentRestored   AuditAction = "pm_document.restored"
	AuditActionPMDocumentSetPinned  AuditAction = "pm_document_set.pinned"

	// Team & settings actions
	AuditActionSettingsUpdated           AuditAction = "settings.updated"
	AuditActionTeamMemberInvited         AuditAction = "team.member_invited"
	AuditActionTeamMemberRoleChanged     AuditAction = "team.member_role_changed"
	AuditActionTeamMemberRemoved         AuditAction = "team.member_removed"
	AuditActionTeamInvitationRevoked     AuditAction = "team.invitation_revoked"
	AuditActionTeamInvitationAccepted    AuditAction = "team.invitation_accepted"
	AuditActionTeamInvitationDeclined    AuditAction = "team.invitation_declined"
	AuditActionTeamInvitationClaimFailed AuditAction = "team.invitation_claim_failed"

	// Verified-domain / auto-join actions
	AuditActionTeamDomainAdded               AuditAction = "team.domain_added"
	AuditActionTeamDomainVerified            AuditAction = "team.domain_verified"
	AuditActionTeamDomainUpdated             AuditAction = "team.domain_updated"
	AuditActionTeamDomainRemoved             AuditAction = "team.domain_removed"
	AuditActionTeamMemberAutoJoined          AuditAction = "team.member_auto_joined"
	AuditActionTeamGitHubOrgAutoJoinEnabled  AuditAction = "team.github_org_auto_join_enabled"
	AuditActionTeamGitHubOrgAutoJoinDisabled AuditAction = "team.github_org_auto_join_disabled"

	// Organization actions
	AuditActionOrganizationCreated AuditAction = "organization.created"

	// Integration & credential actions
	AuditActionIntegrationConnected    AuditAction = "integration.connected"
	AuditActionIntegrationUpdated      AuditAction = "integration.updated"
	AuditActionIntegrationDisconnected AuditAction = "integration.disconnected"
	AuditActionIntegrationWriteback    AuditAction = "integration.writeback"
	AuditActionCredentialUpdated       AuditAction = "credential.updated" // #nosec G101 -- not a credential
	AuditActionCredentialDeleted       AuditAction = "credential.deleted" // #nosec G101 -- not a credential

	// Preview secret bundle actions
	AuditActionPreviewSecretBundleUpdated  AuditAction = "preview_secret_bundle.updated"  // #nosec G101 -- not a credential
	AuditActionPreviewSecretBundleDeleted  AuditAction = "preview_secret_bundle.deleted"  // #nosec G101 -- not a credential
	AuditActionPreviewSecretBundleRevealed AuditAction = "preview_secret_bundle.revealed" // #nosec G101 -- not a credential
	AuditActionPreviewSecretBundleResolved AuditAction = "preview_secret_bundle.resolved" // #nosec G101 -- not a credential
	AuditActionPreviewSecretBundleFailed   AuditAction = "preview_secret_bundle.failed"   // #nosec G101 -- not a credential
	AuditActionPreviewPolicyUpdated        AuditAction = "preview_policy.updated"

	// Auth actions
	AuditActionAuthLogin    AuditAction = "auth.login"
	AuditActionAuthLogout   AuditAction = "auth.logout"
	AuditActionAuthRegister AuditAction = "auth.register"

	// CLI auth + local agent gateway actions
	AuditActionAuthCLILogin        AuditAction = "auth.cli_login"
	AuditActionAuthCLILogout       AuditAction = "auth.cli_logout"
	AuditActionOrgJoinTokenCreated AuditAction = "org.join_token_created" // #nosec G101 -- audit action name
	AuditActionOrgJoinTokenRevoked AuditAction = "org.join_token_revoked" // #nosec G101 -- audit action name
	AuditActionOrgJoinTokenUsed    AuditAction = "org.join_token_used"    // #nosec G101 -- audit action name
	AuditActionCLIToolInvoked      AuditAction = "cli.tool_invoked"

	// Eval actions
	AuditActionEvalTaskCreated  AuditAction = "eval_task.created"
	AuditActionEvalTaskUpdated  AuditAction = "eval_task.updated"
	AuditActionEvalTaskArchived AuditAction = "eval_task.archived"
	AuditActionEvalRunStarted   AuditAction = "eval_run.started"
	AuditActionEvalRunCompleted AuditAction = "eval_run.completed"
	AuditActionEvalBatchStarted AuditAction = "eval_batch.started"

	// API client and token actions
	AuditActionAPIClientCreated  AuditAction = "api_client.created"
	AuditActionAPIClientUpdated  AuditAction = "api_client.updated"
	AuditActionAPIClientDisabled AuditAction = "api_client.disabled"
	AuditActionAPITokenCreated   AuditAction = "api_token.created" // #nosec G101 -- audit action name
	AuditActionAPITokenRevoked   AuditAction = "api_token.revoked" // #nosec G101 -- audit action name
	AuditActionAPITokenUsed      AuditAction = "api_token.used"    // #nosec G101 -- audit action name
)

// Validate checks that the action is a known value.
func (a AuditAction) Validate() error {
	switch a {
	case AuditActionSessionCreated, AuditActionSessionStarted, AuditActionSessionCompleted,
		AuditActionSessionFailed, AuditActionSessionCancelled, AuditActionSessionStatusChanged,
		AuditActionSessionQuestionCreated, AuditActionSessionQuestionAnswered,
		AuditActionSessionHumanInputAnswered, AuditActionSessionHumanInputCancelled,
		AuditActionSessionResumedLocally,
		AuditActionSessionReviewCommentCreated, AuditActionSessionReviewCommentUpdated, AuditActionSessionReviewCommentDeleted,
		AuditActionSessionPRRequested, AuditActionSessionBranchRequested, AuditActionSessionPRPushRequested, AuditActionSessionRetried,
		AuditActionSessionArchived, AuditActionSessionUnarchived, AuditActionSessionPreviewLifetimeSet,
		AuditActionSessionThreadInboxReplayed, AuditActionSessionThreadCreatedByAgentTool, AuditActionSessionThreadMessagedByAgentTool,
		AuditActionProjectCreated, AuditActionProjectUpdated, AuditActionProjectDeleted,
		AuditActionProjectStarted, AuditActionProjectCompleted, AuditActionProjectArchived,
		AuditActionProjectUnarchived, AuditActionProjectRunTriggered,
		AuditActionProjectCycleCompleted, AuditActionProjectTaskCreated, AuditActionProjectTaskUpdated,
		AuditActionProjectTaskDeleted, AuditActionProjectTaskRetried,
		AuditActionAutomationCreated, AuditActionAutomationUpdated, AuditActionAutomationDeleted,
		AuditActionAutomationPaused, AuditActionAutomationResumed, AuditActionAutomationRunTriggered,
		AuditActionAutomationGoalImprovementRequested, AuditActionAutomationGoalImprovementCompleted,
		AuditActionAutomationGoalImprovementFailed, AuditActionAutomationGoalImprovementApplied, AuditActionAutomationGoalImprovementCanceled,
		AuditActionIssueCreated, AuditActionIssueReprioritized,
		AuditActionPMAnalysisTriggered, AuditActionPMPlanCreated, AuditActionPMDecisionMade,
		AuditActionPMBootstrapTriggered, AuditActionPMRefreshTriggered,
		AuditActionPMRefreshAccepted, AuditActionPMRefreshRejected,
		AuditActionPMDocumentUpdated, AuditActionPMDocumentRestored, AuditActionPMDocumentSetPinned,
		AuditActionSettingsUpdated, AuditActionTeamMemberInvited, AuditActionTeamMemberRoleChanged,
		AuditActionTeamMemberRemoved, AuditActionTeamInvitationRevoked, AuditActionTeamInvitationAccepted,
		AuditActionTeamInvitationDeclined, AuditActionTeamInvitationClaimFailed,
		AuditActionTeamDomainAdded, AuditActionTeamDomainVerified, AuditActionTeamDomainUpdated,
		AuditActionTeamDomainRemoved, AuditActionTeamMemberAutoJoined,
		AuditActionTeamGitHubOrgAutoJoinEnabled, AuditActionTeamGitHubOrgAutoJoinDisabled,
		AuditActionOrganizationCreated,
		AuditActionIntegrationConnected, AuditActionIntegrationUpdated, AuditActionIntegrationDisconnected, AuditActionIntegrationWriteback,
		AuditActionCredentialUpdated, AuditActionCredentialDeleted,
		AuditActionPreviewSecretBundleUpdated, AuditActionPreviewSecretBundleDeleted,
		AuditActionPreviewSecretBundleRevealed, AuditActionPreviewSecretBundleResolved, AuditActionPreviewSecretBundleFailed,
		AuditActionPreviewPolicyUpdated,
		AuditActionAuthLogin, AuditActionAuthLogout, AuditActionAuthRegister,
		AuditActionAuthCLILogin, AuditActionAuthCLILogout,
		AuditActionOrgJoinTokenCreated, AuditActionOrgJoinTokenRevoked, AuditActionOrgJoinTokenUsed,
		AuditActionCLIToolInvoked,
		AuditActionEvalTaskCreated, AuditActionEvalTaskUpdated, AuditActionEvalTaskArchived,
		AuditActionEvalRunStarted, AuditActionEvalRunCompleted, AuditActionEvalBatchStarted,
		AuditActionAPIClientCreated, AuditActionAPIClientUpdated, AuditActionAPIClientDisabled,
		AuditActionAPITokenCreated, AuditActionAPITokenRevoked, AuditActionAPITokenUsed:
		return nil
	default:
		return fmt.Errorf("invalid AuditAction: %q", a)
	}
}

// AuditResourceType identifies the type of resource an action targets.
type AuditResourceType string

const (
	AuditResourceSession              AuditResourceType = "session"
	AuditResourceProject              AuditResourceType = "project"
	AuditResourceProjectTask          AuditResourceType = "project_task"
	AuditResourceIssue                AuditResourceType = "issue"
	AuditResourcePMPlan               AuditResourceType = "pm_plan"
	AuditResourcePMDecision           AuditResourceType = "pm_decision"
	AuditResourceSettings             AuditResourceType = "settings"
	AuditResourceTeamMember           AuditResourceType = "team_member"
	AuditResourceInvitation           AuditResourceType = "invitation"
	AuditResourceIntegration          AuditResourceType = "integration"
	AuditResourceCredential           AuditResourceType = "credential"
	AuditResourceUser                 AuditResourceType = "user"
	AuditResourceSessionReviewComment AuditResourceType = "session_review_comment"
	AuditResourcePMDocument           AuditResourceType = "pm_document"
	AuditResourcePMDocumentSet        AuditResourceType = "pm_document_set"
	AuditResourceEvalTask             AuditResourceType = "eval_task"
	AuditResourceEvalRun              AuditResourceType = "eval_run"
	AuditResourceEvalBatch            AuditResourceType = "eval_batch"
	AuditResourceAutomation           AuditResourceType = "automation"
	AuditResourceOrganization         AuditResourceType = "organization"
	AuditResourcePreviewSecretBundle  AuditResourceType = "preview_secret_bundle" // #nosec G101 -- not a credential
	AuditResourcePreviewPolicy        AuditResourceType = "preview_policy"
	AuditResourceAPIClient            AuditResourceType = "api_client"
	AuditResourceAPIToken             AuditResourceType = "api_token"      // #nosec G101 -- audit resource type
	AuditResourceCLIToken             AuditResourceType = "cli_token"      // #nosec G101 -- audit resource type
	AuditResourceOrgJoinToken         AuditResourceType = "org_join_token" // #nosec G101 -- audit resource type
	AuditResourceCLITool              AuditResourceType = "cli_tool"
	AuditResourceOrgDomain            AuditResourceType = "organization_domain"
)

func (t AuditResourceType) Validate() error {
	switch t {
	case AuditResourceSession, AuditResourceProject, AuditResourceProjectTask,
		AuditResourceIssue, AuditResourcePMPlan, AuditResourcePMDecision,
		AuditResourceSettings, AuditResourceTeamMember, AuditResourceInvitation,
		AuditResourceIntegration, AuditResourceCredential, AuditResourceUser,
		AuditResourceSessionReviewComment, AuditResourcePMDocument, AuditResourcePMDocumentSet,
		AuditResourceEvalTask, AuditResourceEvalRun, AuditResourceEvalBatch,
		AuditResourceAutomation, AuditResourceOrganization, AuditResourcePreviewSecretBundle, AuditResourcePreviewPolicy,
		AuditResourceAPIClient, AuditResourceAPIToken,
		AuditResourceCLIToken, AuditResourceOrgJoinToken, AuditResourceCLITool,
		AuditResourceOrgDomain:
		return nil
	default:
		return fmt.Errorf("invalid AuditResourceType: %q", t)
	}
}
