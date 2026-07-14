package preview

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// SuccessfulTurnVerifier binds the adapter-independent agent completion hook
// to the worker-local preview runtime and durable verification coordinator.
// It deliberately uses the session-owned browser context rather than creating
// a second inspector context for automatic verification.
type SuccessfulTurnVerifier struct {
	manager     *Manager
	previews    *db.PreviewStore
	coordinator *VerificationCoordinator
}

func NewSuccessfulTurnVerifier(manager *Manager, previews *db.PreviewStore, runs VerificationRunWriter) *SuccessfulTurnVerifier {
	return &SuccessfulTurnVerifier{
		manager: manager, previews: previews, coordinator: NewVerificationCoordinator(runs),
	}
}

func (v *SuccessfulTurnVerifier) VerifySuccessfulTurn(ctx context.Context, input agent.SuccessfulTurnVerification) error {
	if input.Session == nil {
		return fmt.Errorf("verify successful turn: session is required")
	}
	request := VerificationRequest{
		OrgID: input.Session.OrgID, SessionID: input.Session.ID,
		WorkspaceRevision: input.WorkspaceRevision, Diff: input.Diff,
	}
	if v.manager == nil || v.previews == nil || v.coordinator == nil {
		return fmt.Errorf("verify successful turn: preview verifier is not configured")
	}

	instance, err := v.previews.GetActivePreviewForSession(ctx, input.Session.OrgID, input.Session.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup session preview for verification: %w", err)
		}
		request.Config.Auto = true
		request.SkipReason = "session has no active configured preview"
		_, err = v.coordinator.Run(ctx, request)
		return err
	}

	recycleInput, err := loadRecycleInput(instance)
	if err != nil {
		request.Config.Auto = true
		request.SkipReason = "active preview has no reusable repository configuration"
		request.PreviewInstanceID = &instance.ID
		request.ConfigDigest = instance.ConfigDigest
		_, runErr := v.coordinator.Run(ctx, request)
		if runErr != nil {
			return fmt.Errorf("record preview verification skip: %w", runErr)
		}
		return nil
	}
	request.Config = recycleInput.Config.Verification
	request.ConfigDigest = instance.ConfigDigest
	request.PreviewInstanceID = &instance.ID
	var refreshErr error
	if instance.RuntimeWorkspaceRevision == nil || *instance.RuntimeWorkspaceRevision < input.WorkspaceRevision {
		refreshErr = v.manager.SoftRestartPreviewWithRevision(
			ctx, input.Session.OrgID, instance.ID, input.WorkspaceRevision, input.Session.WorkspaceRevisionUpdatedAt,
		)
	}

	inspector, ok := v.manager.Inspector().(SessionBrowserInspector)
	if !ok || inspector == nil {
		request.SkipReason = "worker-local session browser is unavailable"
	} else {
		request.Observer = sessionVerificationObserver{
			inspector:  inspector,
			refreshErr: refreshErr,
			target: models.BrowserTarget{
				PreviewID: instance.ID.String(), SessionID: input.Session.ID.String(),
				ContextKey: input.Session.ID.String(),
			},
		}
	}
	_, err = v.coordinator.Run(ctx, request)
	if err != nil {
		return fmt.Errorf("run automatic preview verification: %w", err)
	}
	return nil
}

type sessionVerificationObserver struct {
	inspector  SessionBrowserInspector
	target     models.BrowserTarget
	refreshErr error
}

func (o sessionVerificationObserver) Observe(ctx context.Context, path string, viewport models.ViewportSpec) (*models.PreviewObservation, error) {
	if o.refreshErr != nil {
		return nil, fmt.Errorf("update preview runtime: %w", o.refreshErr)
	}
	return o.inspector.Observe(ctx, o.target, models.PreviewObservationOpts{
		ScreenshotOpts: models.ScreenshotOpts{Path: path, ViewportW: viewport.Width, ViewportH: viewport.Height},
		IncludeDOM:     true,
		ReadOnly:       true,
	})
}

var _ agent.SuccessfulTurnVerifier = (*SuccessfulTurnVerifier)(nil)
