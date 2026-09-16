package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type codeReviewScheduler interface {
	SchedulingEnabled() bool
	ReconcileSchedule(context.Context, models.CodeReviewScheduleWake) error
}

func newCodeReviewScheduleHandler(services *Services) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var wake models.CodeReviewScheduleWake
		if err := json.Unmarshal(payload, &wake); err != nil {
			return err
		}
		if wake.OrgID == uuid.Nil || wake.PullRequestID == uuid.Nil {
			return fmt.Errorf("org_id and pull_request_id required")
		}
		scheduler, ok := services.CodeReviewLifecycle.(codeReviewScheduler)
		if !ok || !scheduler.SchedulingEnabled() {
			return fmt.Errorf("review scheduling unavailable")
		}
		return scheduler.ReconcileSchedule(ctx, wake)
	}
}
