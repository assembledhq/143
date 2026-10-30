package worker

import (
	"context"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type codeReviewTerminalOutcomeKey struct{}

// committedCodeReviewTerminalOutcome is a postcommit hook boundary, not a
// discovery or recovery path. Direct hook invocation and supplied-tx provisional
// results cannot authorize effects or reconstruct a latest review owner.
func committedCodeReviewTerminalOutcome(ctx context.Context, orgID uuid.UUID, jobType string) (db.CodeReviewTerminalOutcome, db.CodeReviewTerminalScope, bool) {
	if ctx == nil || orgID == uuid.Nil {
		return db.CodeReviewTerminalOutcome{}, db.CodeReviewTerminalScope{}, false
	}
	outcome, ok := ctx.Value(codeReviewTerminalOutcomeKey{}).(db.CodeReviewTerminalOutcome)
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	token, hasToken := jobctx.LockTokenFromContext(ctx)
	if !ok || !hasJob || !hasToken || !outcome.Matches(orgID, jobID, token) {
		return db.CodeReviewTerminalOutcome{}, db.CodeReviewTerminalScope{}, false
	}
	scope, valid := outcome.Scope()
	if !valid || scope.JobType != jobType || scope.OrgID != orgID || scope.JobID != jobID || scope.LockToken != token {
		return db.CodeReviewTerminalOutcome{}, db.CodeReviewTerminalScope{}, false
	}
	return outcome, scope, true
}

func codeReviewTerminalScopeMatchesFull(scope db.CodeReviewTerminalScope, job runCodeReviewPayload) bool {
	return job.OrgID != uuid.Nil && job.SessionID != uuid.Nil && job.MetadataID != uuid.Nil &&
		job.RepositoryID != uuid.Nil && job.PullRequestID != uuid.Nil && job.PolicyID != uuid.Nil && job.HeadSHA != "" && job.OutputKey != "" &&
		scope.JobType == models.JobTypeRunCodeReview && scope.ReviewScope == models.CodeReviewScopeFull &&
		scope.OrgID == job.OrgID && scope.SessionID == job.SessionID && scope.MetadataID == job.MetadataID &&
		scope.RepositoryID == job.RepositoryID && scope.PullRequestID == job.PullRequestID && scope.PolicyID == job.PolicyID &&
		scope.HeadSHA == job.HeadSHA && scope.OutputKey == job.OutputKey
}

func codeReviewTerminalScopeMatchesAssessment(scope db.CodeReviewTerminalScope, assessment models.CodeReviewAssessment) bool {
	return assessment.ID != uuid.Nil && assessment.OrgID != uuid.Nil && assessment.SessionID != uuid.Nil && assessment.MetadataID != uuid.Nil &&
		assessment.RepositoryID != uuid.Nil && assessment.PullRequestID != uuid.Nil && assessment.PolicyID != uuid.Nil &&
		assessment.HeadSHA != "" && assessment.PublicationKey != "" && assessment.ReviewScope.Validate() == nil &&
		scope.JobType == models.JobTypeRunCodeReviewRecheck && scope.ReviewScope == assessment.ReviewScope &&
		scope.OrgID == assessment.OrgID && scope.AssessmentID == assessment.ID &&
		scope.SessionID == assessment.SessionID && scope.MetadataID == assessment.MetadataID &&
		scope.RepositoryID == assessment.RepositoryID && scope.PullRequestID == assessment.PullRequestID &&
		scope.PolicyID == assessment.PolicyID && scope.HeadSHA == assessment.HeadSHA && scope.OutputKey == assessment.PublicationKey
}
