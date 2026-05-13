package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// errRepoLookupFailed marks a repo lookup error so callers can map it to a 500
// rather than degrading to the generic /workspace path.
var errRepoLookupFailed = errors.New("session workspace: repo lookup for sandbox WorkDir failed")

type sessionWorkspaceResolver struct {
	sessionStore  *db.SessionStore
	repoStore     *db.RepositoryStore
	fileReader    sandbox.FileReader
	snapshotCache *workspace.SnapshotCache
	logger        zerolog.Logger

	repoWorkDirCache sync.Map // map[uuid.UUID]string
}

func newSessionWorkspaceResolver(
	sessionStore *db.SessionStore,
	repoStore *db.RepositoryStore,
	fileReader sandbox.FileReader,
	snapshotCache *workspace.SnapshotCache,
	logger zerolog.Logger,
) *sessionWorkspaceResolver {
	return &sessionWorkspaceResolver{
		sessionStore:     sessionStore,
		repoStore:        repoStore,
		fileReader:       fileReader,
		snapshotCache:    snapshotCache,
		logger:           logger,
		repoWorkDirCache: sync.Map{},
	}
}

func (r *sessionWorkspaceResolver) loadSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if r == nil || r.sessionStore == nil {
		return models.Session{}, errors.New("session workspace resolver is not configured")
	}
	return r.sessionStore.GetByID(ctx, orgID, sessionID)
}

func (r *sessionWorkspaceResolver) resolveSandboxWorkDir(ctx context.Context, session *models.Session) (string, error) {
	defaults := agent.DefaultSandboxConfig()
	if session == nil {
		return "", errors.New("session is required")
	}
	if session.RepositoryID == nil || r.repoStore == nil {
		return defaults.WorkDir, nil
	}
	repoID := *session.RepositoryID
	if cached, ok := r.repoWorkDirCache.Load(repoID); ok {
		return cached.(string), nil
	}
	repo, err := r.repoStore.GetByID(ctx, session.OrgID, repoID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errRepoLookupFailed, err)
	}
	slug := agent.SlugForRepo(repo.FullName)
	if slug == "" {
		return defaults.WorkDir, nil
	}
	resolved := defaults.HomeDir + "/" + slug
	r.repoWorkDirCache.Store(repoID, resolved)
	return resolved, nil
}

func (r *sessionWorkspaceResolver) resolveReaderForSession(ctx context.Context, session *models.Session) (workspace.Reader, string, error) {
	if session == nil {
		return nil, "", errors.New("session is required")
	}
	workDir, err := r.resolveSandboxWorkDir(ctx, session)
	if err != nil {
		return nil, "", err
	}

	if session.ContainerID != nil && *session.ContainerID != "" && r.fileReader != nil {
		return workspace.NewLiveContainerReader(r.fileReader, *session.ContainerID, workDir), workDir, nil
	}

	if session.SnapshotKey != nil && *session.SnapshotKey != "" && r.snapshotCache != nil {
		workspaceRel := strings.TrimPrefix(workDir, "/")
		return workspace.NewSnapshotReader(r.snapshotCache, *session.SnapshotKey, workspaceRel), workDir, nil
	}

	return nil, workDir, workspace.ErrSnapshotMissing
}
