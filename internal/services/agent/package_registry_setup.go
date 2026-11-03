package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type PackageRegistryCredentialResolver interface {
	Metadata(context.Context, uuid.UUID, uuid.UUID) ([]models.PackageRegistryCredentialSummary, error)
	Resolve(context.Context, uuid.UUID, uuid.UUID) ([]models.ResolvedPackageRegistryCredential, error)
}

type PackageSetupPullRequestTrust interface {
	ValidatePackageSetupPullRequest(context.Context, uuid.UUID, uuid.UUID, int, string) error
}

type packageSetupVerifyOnlyKey struct{}
type packageSetupPRIdentityKey struct{}
type packageSetupPRIdentity struct {
	Number int
	Head   string
}

func (o *Orchestrator) SetPackageSetupPullRequestTrust(trust PackageSetupPullRequestTrust) {
	o.packageSetupTrust = trust
}

type NPMDependencySetupProvider interface {
	PrepareNPMDependencies(context.Context, *Sandbox, []repoconfig.NPMSetup, string, func(context.Context) ([]models.ResolvedPackageRegistryCredential, error)) (string, error)
}

func packageCredentialRevision(repositoryID uuid.UUID, metadata []models.PackageRegistryCredentialSummary) (string, error) {
	entries := append([]models.PackageRegistryCredentialSummary(nil), metadata...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID.String() < entries[j].ID.String() })
	bindings := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.RepositoryID != repositoryID || entry.ID == uuid.Nil || entry.Revision == uuid.Nil {
			return "", errors.New("package credential repository binding is invalid")
		}
		bindings = append(bindings, entry.ID.String()+":"+entry.Revision.String()+":"+entry.RegistryURL+":"+entry.PackageScope)
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return "", errors.New("package credential revision unavailable")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (o *Orchestrator) preparePackageRegistryDependencies(ctx context.Context, session *models.Session, sandbox *Sandbox, cfg repoconfig.Config) (string, error) {
	if len(cfg.Setup.NPM) == 0 {
		return "", nil
	}
	if session != nil && session.Origin == models.SessionOriginCodeReview &&
		(cfg.Review == nil || !cfg.Review.InstallNPMDependencies) {
		return "", nil
	}
	if session == nil || session.OrgID == uuid.Nil || session.RepositoryID == nil || *session.RepositoryID == uuid.Nil || o.packageCredentials == nil {
		return "", errors.New("repository package setup requires scoped credential services")
	}
	if sandbox.OrgID != "" && sandbox.OrgID != session.OrgID.String() {
		return "", errors.New("package setup sandbox organization binding is invalid")
	}
	metadata, err := o.packageCredentials.Metadata(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		return "", errors.New("package credential metadata unavailable")
	}
	if len(metadata) > 0 && session.Origin == models.SessionOriginCodeReview {
		var trust struct {
			FromFork *bool `json:"from_fork"`
		}
		if json.Unmarshal(session.RevisionContext, &trust) != nil || trust.FromFork == nil || *trust.FromFork {
			return "", errors.New("package credentials are unavailable for fork or unverified reviews")
		}
	}
	revision, err := packageCredentialRevision(*session.RepositoryID, metadata)
	if err != nil {
		return "", err
	}
	provider, ok := o.provider.(NPMDependencySetupProvider)
	if !ok {
		return "", errors.New("sandbox provider does not support isolated npm dependency setup")
	}
	resolve := func(resolveCtx context.Context) ([]models.ResolvedPackageRegistryCredential, error) {
		if verificationOnly, _ := ctx.Value(packageSetupVerifyOnlyKey{}).(bool); verificationOnly {
			return nil, context.DeadlineExceeded
		}
		if len(metadata) > 0 {
			number, head := 0, ""
			requiresPRTrust := false
			if session.Origin == models.SessionOriginCodeReview {
				requiresPRTrust = true
				var revision codeReviewCheckoutContext
				if json.Unmarshal(session.RevisionContext, &revision) != nil {
					return nil, errors.New("package setup review revision unavailable")
				}
				number, head = revision.GitHubPRNumber, revision.HeadSHA
			} else if identity, ok := ctx.Value(packageSetupPRIdentityKey{}).(packageSetupPRIdentity); ok {
				requiresPRTrust = true
				number, head = identity.Number, identity.Head
			} else if state := automationTurnStateFromContext(ctx); state != nil {
				requiresPRTrust = true
				number, head = state.pullRequestNumber, state.headSHA
			}
			if requiresPRTrust && (number <= 0 || head == "") {
				return nil, errors.New("package setup pull request identity is incomplete")
			}
			if number > 0 {
				if o.packageSetupTrust == nil {
					return nil, errors.New("package setup pull request trust is unavailable")
				}
				if err := o.packageSetupTrust.ValidatePackageSetupPullRequest(resolveCtx, session.OrgID, *session.RepositoryID, number, head); err != nil {
					return nil, errors.New("package setup pull request trust could not be verified")
				}
			} else if session.Origin == models.SessionOriginCodeReview {
				return nil, errors.New("package setup review revision unavailable")
			}
		}

		credentials, resolveErr := o.packageCredentials.Resolve(resolveCtx, session.OrgID, *session.RepositoryID)
		if resolveErr != nil {
			return nil, errors.New("package credentials could not be resolved")
		}
		resolvedMetadata := make([]models.PackageRegistryCredentialSummary, len(credentials))
		for i, credential := range credentials {
			resolvedMetadata[i] = credential.Metadata
		}
		current, revisionErr := packageCredentialRevision(*session.RepositoryID, resolvedMetadata)
		if revisionErr != nil || current != revision {
			return nil, errors.New("package credentials changed during setup; retry preparation")
		}
		return credentials, nil
	}
	return provider.PrepareNPMDependencies(ctx, sandbox, cfg.Setup.NPM, revision, resolve)
}

func (o *Orchestrator) prepareSandboxRepositoryForSession(ctx context.Context, provider SandboxProvider, session *models.Session, sandbox *Sandbox, workDir string, mode repositoryPreparationMode, log zerolog.Logger) error {
	if mode == repositoryPreparationMinimalReview {
		return prepareSandboxRepositoryForSession(ctx, provider, session, sandbox, workDir, mode, log)
	}
	cfg, err := o.readPackageSetupConfig(ctx, sandbox, workDir)
	if err != nil {
		return err
	}
	if _, err := o.preparePackageRegistryDependencies(ctx, session, sandbox, cfg); err != nil {
		return err
	}
	return prepareSandboxRepositoryForSession(ctx, provider, session, sandbox, workDir, mode, log)
}

func (o *Orchestrator) readPackageSetupConfig(ctx context.Context, sandbox *Sandbox, workDir string) (repoconfig.Config, error) {
	raw, err := o.provider.ReadFile(ctx, sandbox, path.Join(workDir, repoconfig.ConfigPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isSandboxFileMissing(err) {
			return repoconfig.Config{}, nil
		}
		return repoconfig.Config{}, fmt.Errorf("read repository setup config: %w", err)
	}
	if len(raw) == 0 {
		return repoconfig.Config{}, nil
	}
	cfg, err := repoconfig.Parse(raw)
	if err != nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields["setup"] == nil {
			return repoconfig.Config{}, nil
		}
		return repoconfig.Config{}, errors.New("repository setup config is invalid")
	}
	return cfg, nil
}
