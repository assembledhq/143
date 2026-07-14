package preview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
)

// SuccessfulTurnVerifier binds successful coding turns to the worker-local
// preview lifecycle, session-owned browser, and durable evidence coordinator.
type SuccessfulTurnVerifier struct {
	manager     *Manager
	previews    *db.PreviewStore
	browser     *BrowserSessionService
	coordinator *VerificationCoordinator
}

func NewSuccessfulTurnVerifier(manager *Manager, previews *db.PreviewStore, browser *BrowserSessionService, runs VerificationRunWriter) *SuccessfulTurnVerifier {
	return &SuccessfulTurnVerifier{manager: manager, previews: previews, browser: browser, coordinator: NewVerificationCoordinator(runs)}
}

func (v *SuccessfulTurnVerifier) VerifySuccessfulTurn(ctx context.Context, input agent.SuccessfulTurnVerification) error {
	if input.Session == nil || input.Sandbox == nil {
		return fmt.Errorf("verify successful turn: session and sandbox are required")
	}
	if v.manager == nil || v.previews == nil || v.browser == nil || v.coordinator == nil {
		return fmt.Errorf("verify successful turn: preview verifier is not configured")
	}

	request := VerificationRequest{OrgID: input.Session.OrgID, SessionID: input.Session.ID, WorkspaceRevision: input.WorkspaceRevision, Diff: input.Diff}
	instance, cfg, err := v.resolvePreviewAndConfig(ctx, input, &request)
	if err != nil {
		return err
	}
	if cfg == nil {
		run, runErr := v.coordinator.Run(ctx, request)
		return verificationOutcomeError(run, runErr)
	}
	request.Config = cfg.Verification
	decision := PlanVerification(input.Diff, cfg.Verification)
	if !decision.Due {
		run, runErr := v.coordinator.Run(ctx, request)
		return verificationOutcomeError(run, runErr)
	}
	if inspector, ok := v.manager.Inspector().(SessionBrowserInspector); !ok || inspector == nil {
		request.SkipReason = "worker-local session browser is unavailable"
		run, runErr := v.coordinator.Run(ctx, request)
		return verificationOutcomeError(run, runErr)
	}

	if instance == nil {
		instance, err = v.startConfiguredPreview(ctx, input, cfg)
		if err != nil {
			request.SkipReason = fmt.Sprintf("preview startup failed: %v", err)
			run, runErr := v.coordinator.Run(ctx, request)
			return verificationOutcomeError(run, runErr)
		}
		request.PreviewInstanceID = &instance.ID
		request.ConfigDigest = instance.ConfigDigest
	} else if instance.RuntimeWorkspaceRevision == nil || *instance.RuntimeWorkspaceRevision < input.WorkspaceRevision {
		updatedAt := time.Now().UTC()
		err = v.manager.SoftRestartPreviewWithRevision(ctx, input.Session.OrgID, instance.ID, input.WorkspaceRevision, updatedAt)
		if errors.Is(err, ErrSoftRestartUnsupported) {
			err = v.manager.RecyclePreviewWithConfigAndRevision(ctx, input.Session.OrgID, instance.ID, cfg, input.WorkspaceRevision, updatedAt)
		}
		if err != nil {
			request.Observer = errorVerificationObserver{err: fmt.Errorf("update preview runtime: %w", err)}
		}
	}

	if request.Observer == nil {
		request.Observer = browserVerificationObserver{
			browser: v.browser, orgID: input.Session.OrgID, sessionID: input.Session.ID, previewID: instance.ID,
			policy: BrowserSessionPolicy{PersistSession: cfg.Browser.PersistSession, DefaultViewport: cfg.Browser.DefaultViewport, AllowedPaths: cfg.Browser.AllowedPaths},
		}
	}
	run, runErr := v.coordinator.Run(ctx, request)
	return verificationOutcomeError(run, runErr)
}

func (v *SuccessfulTurnVerifier) resolvePreviewAndConfig(ctx context.Context, input agent.SuccessfulTurnVerification, request *VerificationRequest) (*models.PreviewInstance, *models.PreviewConfig, error) {
	instance, err := v.previews.GetActivePreviewForSession(ctx, input.Session.OrgID, input.Session.ID)
	if err == nil {
		recycleInput, loadErr := loadRecycleInput(instance)
		if loadErr != nil {
			request.Config.Auto = true
			request.SkipReason = "active preview has no reusable repository configuration"
			request.PreviewInstanceID = &instance.ID
			request.ConfigDigest = instance.ConfigDigest
			return instance, nil, nil
		}
		request.PreviewInstanceID = &instance.ID
		request.ConfigDigest = instance.ConfigDigest
		return instance, recycleInput.Config, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("lookup session preview for verification: %w", err)
	}
	if v.manager.sandboxProvider == nil {
		return nil, nil, fmt.Errorf("load preview config: sandbox provider is unavailable")
	}
	data, readErr := v.manager.sandboxProvider.ReadFile(ctx, input.Sandbox, repoconfig.ConfigPath)
	if readErr != nil {
		request.Config.Auto = true
		request.SkipReason = fmt.Sprintf("repository preview configuration is unavailable: %v", readErr)
		return nil, nil, nil
	}
	cfg, parseErr := ParseConfig(data)
	if parseErr != nil {
		request.Config.Auto = true
		request.SkipReason = fmt.Sprintf("repository preview configuration is invalid: %v", parseErr)
		return nil, nil, nil
	}
	request.ConfigDigest = computeConfigDigest(cfg)
	return nil, cfg, nil
}

func (v *SuccessfulTurnVerifier) startConfiguredPreview(ctx context.Context, input agent.SuccessfulTurnVerification, cfg *models.PreviewConfig) (*models.PreviewInstance, error) {
	userID := uuid.Nil
	if input.Session.TriggeredByUserID != nil {
		userID = *input.Session.TriggeredByUserID
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("session has no initiating user for preview ownership")
	}
	return v.manager.StartPreview(ctx, StartPreviewInput{
		SessionID: input.Session.ID, OrgID: input.Session.OrgID, UserID: userID,
		Sandbox: input.Sandbox, Config: cfg, RepositoryID: uuidPointerValue(input.Session.RepositoryID),
		BaseCommitSHA: stringPointerValue(input.Session.BaseCommitSHA), WorkspaceRevision: input.WorkspaceRevision,
		WorkspaceRevisionUpdatedAt: time.Now().UTC(), Initiator: "automatic_verification",
	})
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func verificationOutcomeError(run models.PreviewVerificationRun, err error) error {
	if err != nil {
		return err
	}
	switch run.Status {
	case models.PreviewVerificationStatusFailed:
		return fmt.Errorf("preview verification failed: %s", run.FailureReason)
	case models.PreviewVerificationStatusHumanInterventionRequired:
		return fmt.Errorf("%w: %s", ErrVerificationHumanIntervention, run.FailureReason)
	default:
		return nil
	}
}

type browserVerificationObserver struct {
	browser                     *BrowserSessionService
	orgID, sessionID, previewID uuid.UUID
	policy                      BrowserSessionPolicy
}

func (o browserVerificationObserver) Observe(ctx context.Context, path string, viewport models.ViewportSpec) (*models.PreviewObservation, error) {
	return o.browser.Observe(ctx, o.orgID, o.sessionID, o.previewID, o.policy, models.PreviewObservationOpts{
		ScreenshotOpts: models.ScreenshotOpts{Path: path, ViewportW: viewport.Width, ViewportH: viewport.Height}, IncludeDOM: true,
	})
}

type errorVerificationObserver struct{ err error }

func (o errorVerificationObserver) Observe(context.Context, string, models.ViewportSpec) (*models.PreviewObservation, error) {
	return nil, o.err
}

var _ agent.SuccessfulTurnVerifier = (*SuccessfulTurnVerifier)(nil)
