package agent

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

// EnsureCodeReviewWorkspaceEvidenceForTest exposes the dormant evidence engine
// only to external-package acceptance tests. Production callers use the normal
// orchestrator lifecycle; this bridge is absent from production binaries.
func EnsureCodeReviewWorkspaceEvidenceForTest(ctx context.Context, orchestrator *Orchestrator, session *models.Session, sandbox *Sandbox) error {
	return orchestrator.ensureCodeReviewWorkspaceEvidence(ctx, session, sandbox, zerolog.Nop())
}
