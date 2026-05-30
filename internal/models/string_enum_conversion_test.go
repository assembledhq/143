package models

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertFirstEnumValidators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "role", validate: RoleAdmin.Validate},
		{name: "role invalid", validate: Role("owner").Validate, expectErr: true},
		{name: "invitation status", validate: InvitationStatusPending.Validate},
		{name: "invitation status invalid", validate: InvitationStatus("sent").Validate, expectErr: true},
		{name: "verified domain status", validate: VerifiedDomainStatusPending.Validate},
		{name: "verified domain status invalid", validate: VerifiedDomainStatus("active").Validate, expectErr: true},
		{name: "repository status", validate: RepositoryStatusActive.Validate},
		{name: "repository status paused", validate: RepositoryStatusPaused.Validate},
		{name: "repository status invalid", validate: RepositoryStatus("archived").Validate, expectErr: true},
		{name: "issue status", validate: IssueStatusOpen.Validate},
		{name: "issue status invalid", validate: IssueStatus("observing").Validate, expectErr: true},
		{name: "issue severity", validate: IssueSeverityHigh.Validate},
		{name: "issue severity empty compatibility", validate: IssueSeverity("").Validate},
		{name: "issue severity invalid", validate: IssueSeverity("urgent").Validate, expectErr: true},
		{name: "session token mode", validate: SessionTokenModeLow.Validate},
		{name: "session token mode invalid", validate: SessionTokenMode("medium").Validate, expectErr: true},
		{name: "git identity source", validate: GitIdentitySourceUser.Validate},
		{name: "git identity source invalid", validate: GitIdentitySource("bot").Validate, expectErr: true},
		{name: "pull request status", validate: PullRequestStatusOpen.Validate},
		{name: "pull request status invalid", validate: PullRequestStatus("draft").Validate, expectErr: true},
		{name: "pull request review status", validate: PullRequestReviewStatusPending.Validate},
		{name: "pull request review status invalid", validate: PullRequestReviewStatus("commented").Validate, expectErr: true},
		{name: "pull request CI status", validate: PullRequestCIStatusSuccess.Validate},
		{name: "pull request CI empty compatibility", validate: PullRequestCIStatus("").Validate},
		{name: "pull request CI invalid", validate: PullRequestCIStatus("green").Validate, expectErr: true},
		{name: "session question status", validate: SessionQuestionStatusPending.Validate},
		{name: "session question status invalid", validate: SessionQuestionStatus("closed").Validate, expectErr: true},
		{name: "session log level", validate: SessionLogLevelInfo.Validate},
		{name: "session log level invalid", validate: SessionLogLevel("trace").Validate, expectErr: true},
		{name: "thread file event type", validate: SessionThreadFileEventTypeCreated.Validate},
		{name: "thread file event type invalid", validate: SessionThreadFileEventType("renamed").Validate, expectErr: true},
		{name: "session diff source", validate: SessionDiffSourceTurnComplete.Validate},
		{name: "session diff source invalid", validate: SessionDiffSource("manual").Validate, expectErr: true},
		{name: "job status", validate: JobStatusPending.Validate},
		{name: "job status invalid", validate: JobStatus("queued").Validate, expectErr: true},
		{name: "automation execution mode", validate: AutomationExecutionModeSequential.Validate},
		{name: "automation execution mode invalid", validate: AutomationExecutionMode("fanout").Validate, expectErr: true},
		{name: "automation schedule type", validate: AutomationScheduleInterval.Validate},
		{name: "automation schedule type invalid", validate: AutomationScheduleType("event").Validate, expectErr: true},
		{name: "automation run status", validate: AutomationRunStatusPending.Validate},
		{name: "automation run status invalid", validate: AutomationRunStatus("queued").Validate, expectErr: true},
		{name: "automation triggered by", validate: AutomationTriggeredBySchedule.Validate},
		{name: "automation triggered by invalid", validate: AutomationTriggeredBy("webhook").Validate, expectErr: true},
		{name: "coding credential row status", validate: CodingCredentialRowStatusActive.Validate},
		{name: "coding credential row status invalid", validate: CodingCredentialRowStatus("expired").Validate, expectErr: true},
		{name: "coding credential scope", validate: CodingCredentialScopeOrg.Validate},
		{name: "coding credential scope invalid", validate: CodingCredentialScope("team").Validate, expectErr: true},
		{name: "credential status", validate: CredentialStatusActive.Validate},
		{name: "credential status invalid", validate: CredentialStatus("pending").Validate, expectErr: true},
		{name: "node status", validate: NodeStatusActive.Validate},
		{name: "node status invalid", validate: NodeStatus("offline").Validate, expectErr: true},
		{name: "node mode", validate: NodeModeWorker.Validate},
		{name: "node mode invalid", validate: NodeMode("scheduler").Validate, expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "validator should reject unknown enum values")
				return
			}
			require.NoError(t, err, "validator should accept known enum values")
		})
	}
}

func TestConvertFirstModelFieldsAreTypedEnums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     any
		field     string
		wantType  any
		wantIsPtr bool
	}{
		{name: "user role", model: User{}, field: "Role", wantType: Role("")},
		{name: "membership role", model: OrganizationMembership{}, field: "Role", wantType: Role("")},
		{name: "invitation role", model: Invitation{}, field: "Role", wantType: Role("")},
		{name: "invitation status", model: Invitation{}, field: "Status", wantType: InvitationStatus("")},
		{name: "verified domain status", model: VerifiedDomain{}, field: "Status", wantType: VerifiedDomainStatus("")},
		{name: "verified domain auto join role", model: VerifiedDomain{}, field: "AutoJoinRole", wantType: Role("")},
		{name: "repository status", model: Repository{}, field: "Status", wantType: RepositoryStatus("")},
		{name: "issue status", model: Issue{}, field: "Status", wantType: IssueStatus("")},
		{name: "issue severity", model: Issue{}, field: "Severity", wantType: IssueSeverity("")},
		{name: "session status", model: Session{}, field: "Status", wantType: SessionStatus("")},
		{name: "session autonomy", model: Session{}, field: "AutonomyLevel", wantType: SessionAutonomy("")},
		{name: "session token mode", model: Session{}, field: "TokenMode", wantType: SessionTokenMode("")},
		{name: "session sandbox", model: Session{}, field: "SandboxState", wantType: SandboxState("")},
		{name: "session git identity source", model: Session{}, field: "GitIdentitySource", wantType: GitIdentitySource(""), wantIsPtr: true},
		{name: "pull request status", model: PullRequest{}, field: "Status", wantType: PullRequestStatus("")},
		{name: "pull request review status", model: PullRequest{}, field: "ReviewStatus", wantType: PullRequestReviewStatus("")},
		{name: "pull request authorship", model: PullRequest{}, field: "AuthoredBy", wantType: GitIdentitySource("")},
		{name: "pull request CI status", model: PullRequest{}, field: "CIStatus", wantType: PullRequestCIStatus("")},
		{name: "session question status", model: SessionQuestion{}, field: "Status", wantType: SessionQuestionStatus("")},
		{name: "session log level", model: SessionLog{}, field: "Level", wantType: SessionLogLevel("")},
		{name: "thread file event type", model: SessionThreadFileEvent{}, field: "EventType", wantType: SessionThreadFileEventType("")},
		{name: "session result diff source", model: SessionResult{}, field: "DiffSource", wantType: SessionDiffSource("")},
		{name: "session diff snapshot source", model: SessionDiffSnapshot{}, field: "Source", wantType: SessionDiffSource("")},
		{name: "job status", model: Job{}, field: "Status", wantType: JobStatus("")},
		{name: "automation execution mode", model: Automation{}, field: "ExecutionMode", wantType: AutomationExecutionMode("")},
		{name: "automation schedule type", model: Automation{}, field: "ScheduleType", wantType: AutomationScheduleType("")},
		{name: "automation interval unit", model: Automation{}, field: "IntervalUnit", wantType: ScheduleUnit(""), wantIsPtr: true},
		{name: "automation run triggered by", model: AutomationRun{}, field: "TriggeredBy", wantType: AutomationTriggeredBy("")},
		{name: "automation run status", model: AutomationRun{}, field: "Status", wantType: AutomationRunStatus("")},
		{name: "coding credential status", model: CodingCredential{}, field: "Status", wantType: CodingCredentialRowStatus("")},
		{name: "decrypted coding credential status", model: DecryptedCodingCredential{}, field: "Status", wantType: CodingCredentialRowStatus("")},
		{name: "coding credential summary scope", model: CodingCredentialSummary{}, field: "Scope", wantType: CodingCredentialScope("")},
		{name: "create coding credential scope", model: CreateCodingCredentialInput{}, field: "Scope", wantType: CodingCredentialScope("")},
		{name: "update coding credential scope", model: UpdateCodingCredentialInput{}, field: "Scope", wantType: CodingCredentialScope("")},
		{name: "update coding credential status", model: UpdateCodingCredentialInput{}, field: "Status", wantType: CodingCredentialRowStatus(""), wantIsPtr: true},
		{name: "move coding credential scope", model: MoveCodingCredentialInput{}, field: "Scope", wantType: CodingCredentialScope("")},
		{name: "reorder coding credential scope", model: ReorderCodingCredentialsInput{}, field: "Scope", wantType: CodingCredentialScope("")},
		{name: "org credential status", model: OrgCredential{}, field: "Status", wantType: CredentialStatus("")},
		{name: "user credential status", model: UserCredential{}, field: "Status", wantType: CredentialStatus("")},
		{name: "node status", model: Node{}, field: "Status", wantType: NodeStatus("")},
		{name: "node mode", model: Node{}, field: "Mode", wantType: NodeMode("")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			field, ok := reflect.TypeOf(tt.model).FieldByName(tt.field)
			require.True(t, ok, "model should expose the audited field")

			want := reflect.TypeOf(tt.wantType)
			if tt.wantIsPtr {
				want = reflect.PointerTo(want)
			}
			require.Equal(t, want, field.Type, "audited field should use the typed enum")
		})
	}
}
