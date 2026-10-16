package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewBootstrapAcceptsOwnedHookAndLFSConfiguration(t *testing.T) {
	t.Parallel()
	for _, hooks := range []string{".githooks", ".husky", ".husky/_", ".git/hooks"} {
		t.Run(hooks, func(t *testing.T) {
			t.Parallel()
			bootstrap := strings.Join([]string{
				"git config core.hooksPath '" + hooks + "'",
				"git config filter.lfs.clean 'git-lfs clean -- %f'",
				"git config filter.lfs.smudge 'git-lfs smudge -- %f'",
				"git config filter.lfs.process 'git-lfs filter-process'",
				"git config filter.lfs.required true",
				"mkdir -p '" + hooks + "'",
				"printf '#!/bin/sh\\nprintf executed > bootstrap-hook-executed\\n' > '" + hooks + "/post-index-change'",
				"chmod +x '" + hooks + "/post-index-change'",
				"printf completed > bootstrap-completed",
			}, "\n")
			f := newReviewEvidenceGitFixture(t, bootstrap, false)
			reviewInputsWritableCleanup(t, f.workDir)
			o := f.orchestrator()
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "repository bootstrap must be able to install the finite owned hook and LFS configuration")
			require.FileExists(t, filepath.Join(f.workDir, "bootstrap-completed"), "acceptance must exercise the real repository recipe rather than only parsing config")
			session := reviewInputsSession(t, f.base, f.head)
			session.RepositoryID = f.session.RepositoryID
			require.NoError(t, o.ensureCodeReviewInputs(context.Background(), session, f.sandbox()), "captured input creation after repository bootstrap must accept standard configuration")
			require.NoError(t, o.ensureCodeReviewInputs(context.Background(), session, f.sandbox()), "warm captured evidence must remain reusable with installed bootstrap settings")
			expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
			require.NoError(t, o.ensureCodeReviewInputs(expired, session, f.sandbox()), "expired-budget verification must remain local with standard settings installed")
			require.NoFileExists(t, filepath.Join(f.workDir, "bootstrap-hook-executed"), "platform source verification must never execute the installed repository hook")
		})
	}
}
