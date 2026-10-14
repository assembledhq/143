package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
)

const CodeReviewWorkspacePreparationTimeout = 8 * time.Minute

// CodeReviewWorkspaceVerificationTimeout permits bounded platform-only checks
// of an already-prepared cache after its setup budget. It never authorizes
// repository recipes, arbitrary verifiers, network hydration, or repair.
const CodeReviewWorkspaceVerificationTimeout = time.Minute

// ErrCodeReviewSourceIntegrity identifies a trusted hash/object validation
// failure. It is terminal for this workspace and cannot request source hydration.
var ErrCodeReviewSourceIntegrity = errors.New("pinned review source object integrity validation failed; workspace must be recreated")

// ErrCodeReviewPreparationCleanup identifies a preparation command whose process
// termination could not be confirmed. The workspace cannot be reused or retried.
var ErrCodeReviewPreparationCleanup = errors.New("review preparation process cleanup could not be confirmed; workspace must be recreated")

const (
	CodeReviewPreparationStageClone           = "clone"
	CodeReviewPreparationStageHydration       = "hydration"
	CodeReviewPreparationStageCheckout        = "checkout"
	CodeReviewPreparationStageDependencySetup = "dependency_setup"
	CodeReviewPreparationStageReadiness       = "readiness"
)

// CodeReviewWorkspacePreparationError exposes only a stable stage and safe
// explanation. The original cause remains available for internal classification.
type CodeReviewWorkspacePreparationError struct {
	Stage string
	cause error
}

func NewCodeReviewWorkspacePreparationError(stage string, cause error) error {
	if cause == nil {
		return nil
	}
	var existing *CodeReviewWorkspacePreparationError
	if errors.As(cause, &existing) {
		if errors.Is(cause, ErrCodeReviewPreparationCleanup) || errors.Is(cause, ErrCodeReviewSourceIntegrity) {
			if _, direct := cause.(*CodeReviewWorkspacePreparationError); !direct {
				// Terminal cleanup or integrity can be joined outside an existing
				// stage error. Keep the entire tree under one safe message rather
				// than returning raw diagnostics or hiding its terminal cause.
				return &CodeReviewWorkspacePreparationError{Stage: existing.Stage, cause: cause}
			}
		}
		return cause
	}
	return &CodeReviewWorkspacePreparationError{Stage: stage, cause: cause}
}

func (e *CodeReviewWorkspacePreparationError) Unwrap() error { return e.cause }

func (e *CodeReviewWorkspacePreparationError) Error() string {
	if errors.Is(e.cause, ErrCodeReviewPreparationCleanup) {
		return "Review workspace preparation could not confirm process cleanup. Recreate the review workspace and retry. Review did not run."
	}
	if errors.Is(e.cause, ErrStalePullRequestHead) {
		return "Review workspace preparation observed a moved pull request head. Review did not run."
	}
	stage := "workspace readiness verification"
	switch e.Stage {
	case CodeReviewPreparationStageClone:
		stage = "repository clone"
	case CodeReviewPreparationStageHydration:
		stage = "pinned source hydration"
	case CodeReviewPreparationStageCheckout:
		stage = "pinned pull request checkout"
	case CodeReviewPreparationStageDependencySetup:
		stage = "declared dependency and bootstrap setup"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return fmt.Sprintf("Review workspace preparation exceeded its total eight-minute budget during %s. Review did not run.", stage)
	}
	if errors.Is(e.cause, context.Canceled) {
		return fmt.Sprintf("Review workspace preparation was cancelled during %s. Review did not run.", stage)
	}
	if errors.Is(e.cause, ErrCodeReviewSourceIntegrity) {
		return "Review workspace preparation could not verify pinned Git source object integrity. Recreate the review workspace and retry. Review did not run."
	}
	return fmt.Sprintf("Review workspace preparation failed during %s. Review did not run.", stage)
}

type codeReviewPreparationDeadlineKey struct{}

// WithCodeReviewWorkspacePreparationDeadline carries an absolute preparation
// deadline without shortening the later agent execution. Each preparation stage
// derives a cancellable context from this same deadline. The earliest deadline
// wins, including a caller's existing context deadline.
func WithCodeReviewWorkspacePreparationDeadline(ctx context.Context, deadline time.Time) context.Context {
	if existing, ok := ctx.Value(codeReviewPreparationDeadlineKey{}).(time.Time); ok && existing.Before(deadline) {
		deadline = existing
	}
	if existing, ok := ctx.Deadline(); ok && existing.Before(deadline) {
		deadline = existing
	}
	return context.WithValue(ctx, codeReviewPreparationDeadlineKey{}, deadline)
}

func withCodeReviewPreparationBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(codeReviewPreparationDeadlineKey{}).(time.Time); ok {
		return ctx
	}
	startedAt := time.Now()
	if active, _ := ctx.Value(reviewPreparationActivationKey{}).(bool); active {
		if createdAt, ok := jobctx.JobCreatedAtFromContext(ctx); ok && !createdAt.IsZero() {
			startedAt = createdAt
		}
	}
	return WithCodeReviewWorkspacePreparationDeadline(ctx, startedAt.Add(CodeReviewWorkspacePreparationTimeout))
}

func codeReviewPreparationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = withCodeReviewPreparationBudget(ctx)
	deadline := ctx.Value(codeReviewPreparationDeadlineKey{}).(time.Time)
	return context.WithDeadline(ctx, deadline)
}

func reviewPreparationDeadlineUnix(ctx context.Context) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, fmt.Errorf("review preparation deadline is unavailable")
	}
	if !time.Now().Before(deadline) {
		return 0, context.DeadlineExceeded
	}
	return float64(deadline.UnixNano()) / float64(time.Second), nil
}

// reviewPreparationExecutor lets Docker close a disconnected output stream;
// ordinary coding-session Exec retains its existing transport behavior.
type reviewPreparationExecutor interface {
	ExecForReviewPreparation(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error)
}

type reviewPreparationRestorer interface {
	RestoreForReviewPreparation(context.Context, *Sandbox, io.Reader) error
}

type reviewPreparationSandboxProvider struct{ SandboxProvider }

func (p reviewPreparationSandboxProvider) Restore(ctx context.Context, sandbox *Sandbox, reader io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if restorer, ok := p.SandboxProvider.(reviewPreparationRestorer); ok {
		return restorer.RestoreForReviewPreparation(ctx, sandbox, reader)
	}
	return p.SandboxProvider.Restore(ctx, sandbox, reader)
}

func (p reviewPreparationSandboxProvider) Create(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.SandboxProvider.Create(ctx, cfg)
}

func (p reviewPreparationSandboxProvider) Destroy(ctx context.Context, sandbox *Sandbox) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return p.SandboxProvider.Destroy(cleanupCtx, sandbox)
}

func sandboxProviderForPreparation(ctx context.Context, provider SandboxProvider) SandboxProvider {
	if _, reviewing := ctx.Value(codeReviewPreparationDeadlineKey{}).(time.Time); reviewing {
		return reviewPreparationSandboxProvider{provider}
	}
	return provider
}

// codeReviewSetupWindow is used sequentially by one continuation. Only an
// already reusable warm workspace may start one local verification allowance;
// later stages retain that same deadline, including when it has expired.
type codeReviewSetupWindow struct {
	parent       context.Context
	preparation  context.Context
	reusable     bool
	verification context.Context
	cancel       context.CancelFunc
}

func (w *codeReviewSetupWindow) Context() context.Context {
	if w.verification != nil {
		return w.verification
	}
	if w.reusable && errors.Is(w.preparation.Err(), context.DeadlineExceeded) && w.parent.Err() == nil {
		w.verification, w.cancel = context.WithTimeout(w.parent, CodeReviewWorkspaceVerificationTimeout)
		return w.verification
	}
	return w.preparation
}

func (w *codeReviewSetupWindow) Close() {
	if w.cancel != nil {
		w.cancel()
	}
}
