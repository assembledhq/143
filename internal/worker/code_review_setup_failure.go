package worker

import (
	"errors"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// This versioned record is persisted in the preparation job's last_error. Only
// platform-generated, allowlisted codes cross the job boundary; npm output does
// not. Human-readable review failures come from codeReviewTerminalFailureStatus.
const codeReviewSetupFailurePrefix = "code_review_npm_setup_failure:v1:"

var errCodeReviewWorkspaceCancellationUnconfirmed = errors.New("preparation may still be running")

type codeReviewSetupFailure struct {
	code  string
	cause error
}

func (e *codeReviewSetupFailure) Error() string { return codeReviewSetupFailurePrefix + e.code }
func (e *codeReviewSetupFailure) Unwrap() error { return e.cause }

func codeReviewPreparationFailure(err error) error {
	if !agent.IsTerminalNPMSetupError(err) {
		return err
	}
	var setup *agent.NPMSetupError
	if !errors.As(err, &setup) {
		return err
	}
	return &FatalError{Err: &codeReviewSetupFailure{code: setup.Code, cause: err}}
}

func codeReviewPersistedSetupFailure(status models.JobStatus, failure *string) error {
	if status != models.JobStatusDeadLetter || failure == nil || !strings.HasPrefix(*failure, codeReviewSetupFailurePrefix) {
		return nil
	}
	code := strings.TrimPrefix(*failure, codeReviewSetupFailurePrefix)
	setup := &agent.NPMSetupError{Category: "permanent", Code: code}
	if !agent.IsTerminalNPMSetupError(setup) {
		return nil
	}
	return codeReviewPreparationFailure(setup)
}
