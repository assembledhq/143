package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

var ErrVerificationHumanIntervention = errors.New("preview verification requires human intervention")

type VerificationRunWriter interface {
	Create(ctx context.Context, orgID uuid.UUID, run models.PreviewVerificationRun) (models.PreviewVerificationRun, error)
	Complete(ctx context.Context, orgID, runID uuid.UUID, status models.PreviewVerificationStatus, attempt int, steps, artifacts json.RawMessage, consoleErrors int, summary, failureReason string) (models.PreviewVerificationRun, error)
}

type VerificationObserver interface {
	Observe(ctx context.Context, path string, viewport models.ViewportSpec) (*models.PreviewObservation, error)
}

type VerificationFixer interface {
	FixVerificationFailure(ctx context.Context, attempt int, failure string) error
}

type VerificationRequest struct {
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	PreviewInstanceID *uuid.UUID
	WorkspaceRevision int64
	ConfigDigest      string
	Diff              string
	Config            models.PreviewVerificationConfig
	Observer          VerificationObserver
	Fixer             VerificationFixer
}

type VerificationCoordinator struct{ runs VerificationRunWriter }

func NewVerificationCoordinator(runs VerificationRunWriter) *VerificationCoordinator {
	return &VerificationCoordinator{runs: runs}
}

func (c *VerificationCoordinator) Run(ctx context.Context, request VerificationRequest) (models.PreviewVerificationRun, error) {
	decision := PlanVerification(request.Diff, request.Config)
	plan := decision.Plan
	if plan == nil {
		// A nil slice marshals to JSON null, which violates the plan jsonb array
		// CHECK constraint; the common skip path has no plan, so normalize to [].
		plan = []models.PreviewVerificationPlanStep{}
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("marshal preview verification plan: %w", err)
	}
	maxAttempts := request.Config.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	status := models.PreviewVerificationStatusRunning
	var completedAt *time.Time
	if !decision.Due {
		status = models.PreviewVerificationStatusSkipped
		now := time.Now().UTC()
		completedAt = &now
	}
	run, err := c.runs.Create(ctx, request.OrgID, models.PreviewVerificationRun{
		SessionID: request.SessionID, PreviewInstanceID: request.PreviewInstanceID,
		WorkspaceRevision: request.WorkspaceRevision, ConfigDigest: request.ConfigDigest,
		Trigger: models.PreviewVerificationTriggerAutomatic, Status: status, Attempt: 1,
		MaxAttempts: maxAttempts, Plan: planJSON, SkipReason: decision.SkipReason,
		Summary: decision.SkipReason, CompletedAt: completedAt,
	})
	if err != nil || !decision.Due {
		return run, err
	}
	if request.Observer == nil {
		return c.completeFailure(ctx, request.OrgID, run, 1, nil, "preview observer is unavailable")
	}
	if request.Config.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(request.Config.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	allSteps := make([]models.PreviewVerificationStep, 0, len(decision.Plan)*maxAttempts)
	var lastFailure string
	lastAttempt := 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastAttempt = attempt
		steps, failure, humanRequired := executeVerificationPlan(ctx, request.Observer, decision.Plan, request.Config.FailOnConsoleError, attempt)
		allSteps = append(allSteps, steps...)
		lastFailure = failure
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			lastFailure = fmt.Sprintf("preview verification timed out after %d seconds", request.Config.TimeoutSeconds)
			break
		}
		if humanRequired {
			return c.complete(ctx, request.OrgID, run, models.PreviewVerificationStatusHumanInterventionRequired, attempt, allSteps, failure)
		}
		if failure == "" {
			return c.complete(ctx, request.OrgID, run, models.PreviewVerificationStatusPassed, attempt, allSteps, "")
		}
		if attempt == maxAttempts || request.Fixer == nil {
			break
		}
		if fixErr := request.Fixer.FixVerificationFailure(ctx, attempt, failure); fixErr != nil {
			if errors.Is(fixErr, ErrVerificationHumanIntervention) {
				return c.complete(ctx, request.OrgID, run, models.PreviewVerificationStatusHumanInterventionRequired, attempt, allSteps, fixErr.Error())
			}
			lastFailure = fmt.Sprintf("%s; fix attempt failed: %v", failure, fixErr)
			break
		}
	}
	return c.completeFailure(ctx, request.OrgID, run, lastAttempt, allSteps, lastFailure)
}

func executeVerificationPlan(ctx context.Context, observer VerificationObserver, plan []models.PreviewVerificationPlanStep, failOnConsole bool, attempt int) ([]models.PreviewVerificationStep, string, bool) {
	steps := make([]models.PreviewVerificationStep, 0, len(plan))
	for index, planned := range plan {
		observation, err := observer.Observe(ctx, planned.Path, planned.Viewport)
		step := models.PreviewVerificationStep{Index: index, Attempt: attempt, Path: planned.Path, Viewport: planned.Viewport, Outcome: "passed"}
		if err != nil {
			step.Outcome, step.Error = "failed", err.Error()
			steps = append(steps, step)
			return steps, fmt.Sprintf("%s at %s", err, planned.Path), errors.Is(err, ErrVerificationHumanIntervention)
		}
		if observation.Screenshot != nil {
			step.Artifact = observation.Screenshot.Artifact
		}
		for _, message := range observation.Console {
			if message.Level == "error" {
				step.ConsoleCount++
			}
		}
		if !observation.Ready {
			step.Outcome, step.Error = "failed", "preview readiness check failed"
		} else if failOnConsole && step.ConsoleCount > 0 {
			step.Outcome, step.Error = "failed", "new console errors were observed"
		}
		steps = append(steps, step)
		if step.Outcome == "failed" {
			return steps, fmt.Sprintf("%s at %s", step.Error, planned.Path), false
		}
	}
	return steps, "", false
}

func (c *VerificationCoordinator) completeFailure(ctx context.Context, orgID uuid.UUID, run models.PreviewVerificationRun, attempt int, steps []models.PreviewVerificationStep, failure string) (models.PreviewVerificationRun, error) {
	return c.complete(ctx, orgID, run, models.PreviewVerificationStatusFailed, attempt, steps, failure)
}

func (c *VerificationCoordinator) complete(ctx context.Context, orgID uuid.UUID, run models.PreviewVerificationRun, status models.PreviewVerificationStatus, attempt int, steps []models.PreviewVerificationStep, failure string) (models.PreviewVerificationRun, error) {
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("marshal preview verification steps: %w", err)
	}
	artifacts := make([]models.PreviewArtifact, 0, len(steps))
	consoleErrors := 0
	for _, step := range steps {
		consoleErrors += step.ConsoleCount
		if step.Artifact != nil {
			artifacts = append(artifacts, *step.Artifact)
		}
	}
	artifactsJSON, err := json.Marshal(artifacts)
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("marshal preview verification artifacts: %w", err)
	}
	summary := fmt.Sprintf("Verified %d preview checks", len(steps))
	if status != models.PreviewVerificationStatusPassed {
		summary = failure
	}
	return c.runs.Complete(ctx, orgID, run.ID, status, attempt, stepsJSON, artifactsJSON, consoleErrors, summary, failure)
}
