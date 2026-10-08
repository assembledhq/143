package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewExternalContextResolver produces exactly the integration skills
// text injected into a code-review turn. It fails closed on uncertain credential
// or settings reads, so capture cannot mistake unavailable tools for no tools.
type CodeReviewExternalContextResolver struct {
	Credentials CredentialProvider
	Orgs        interface {
		GetByID(context.Context, uuid.UUID) (models.Organization, error)
	}
	GitHubTools    bool
	SessionTools   bool
	MemoryInjected bool
}

var ErrCodeReviewExternalContextUnfingerprinted = errors.New("code review external context is not fingerprinted")

func (r CodeReviewExternalContextResolver) ResolveCodeReviewExternalContext(ctx context.Context, orgID uuid.UUID) (string, string, error) {
	if r.Credentials == nil || r.Orgs == nil || orgID == uuid.Nil {
		return "", "", errors.New("code review external context dependencies are unavailable")
	}
	if r.MemoryInjected {
		return "", "", ErrCodeReviewExternalContextUnfingerprinted
	}
	var ic integrationCredentials
	var credentials map[models.ProviderName]*models.DecryptedCredential
	if batch, ok := r.Credentials.(integrationCredentialProvider); ok {
		creds, err := batch.GetAllIntegrations(ctx, orgID, integrationProviderNames)
		if err != nil {
			return "", "", fmt.Errorf("resolve code review integrations: %w", err)
		}
		ic.apply(creds)
		credentials = creds
	} else {
		creds := make(map[models.ProviderName]*models.DecryptedCredential, len(integrationProviderNames))
		for _, provider := range integrationProviderNames {
			cred, err := r.Credentials.Get(ctx, orgID, provider)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", "", fmt.Errorf("resolve code review integration %s: %w", provider, err)
			}
			if cred != nil {
				creds[provider] = cred
			}
		}
		ic.apply(creds)
		credentials = creds
	}
	org, err := r.Orgs.GetByID(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("resolve code review org settings: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return "", "", fmt.Errorf("parse code review org settings: %w", err)
	}
	doc := renderCodeReviewIntegrationSkills(ic, r.GitHubTools, r.SessionTools, settings)
	// Credential configuration is secret, so persist only a digest of stable
	// row versions alongside the exact rendered prompt. A token/scope rotation
	// changes UpdatedAt even when the list of available tools stays the same.
	type credentialVersion struct {
		Provider  models.ProviderName
		ID        uuid.UUID
		Status    models.CredentialStatus
		Priority  int
		UpdatedAt time.Time
	}
	versions := make([]credentialVersion, 0, len(credentials))
	for provider, credential := range credentials {
		if credential == nil {
			continue
		}
		versions = append(versions, credentialVersion{provider, credential.ID, credential.Status, credential.Priority, credential.UpdatedAt})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Provider < versions[j].Provider })
	serialized, err := json.Marshal(struct {
		Document string
		Versions []credentialVersion
	}{doc, versions})
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(serialized)
	return doc, hex.EncodeToString(digest[:]), nil
}

func renderCodeReviewIntegrationSkills(ic integrationCredentials, githubTools, sessionTools bool, settings models.OrgSettings) string {
	reg := integration.NewRegistry()
	if ic.Sentry != nil && ic.Sentry.AccessToken != "" {
		reg.RegisterErrorTracker(integration.NewSentryErrorTracker(integration.SentryTrackerConfig{AuthToken: ic.Sentry.AccessToken, OrgSlug: ic.Sentry.OrgSlug}))
	}
	if ic.Linear != nil && ic.Linear.AccessToken != "" {
		reg.RegisterTaskManager(integration.NewLinearTaskManager(integration.LinearManagerConfig{AuthToken: ic.Linear.AccessToken}))
	}
	if ic.Notion != nil && ic.Notion.AccessToken != "" {
		reg.RegisterDocumentStore(integration.NewNotionDocumentStore(integration.NotionDocumentStoreConfig{AuthToken: ic.Notion.AccessToken}))
	}
	if ic.CircleCI != nil && ic.CircleCI.AuthToken != "" && ic.CircleCI.ProjectSlug != "" {
		reg.RegisterCITestInsights(integration.NewCircleCITestInsights(integration.CircleCIConfig{AuthToken: ic.CircleCI.AuthToken, ProjectSlug: ic.CircleCI.ProjectSlug}))
	}
	if githubTools {
		reg.RegisterCodeReviewSource(&integration.StubCodeReviewSource{ProviderName: "github"})
	}
	if sessionTools {
		reg.RegisterPullRequestCreator(&integration.StubPullRequestCreator{ProviderName: "session"})
		reg.RegisterMessageSender(&integration.StubMessageSender{ProviderName: "slack"})
		if settings.EffectiveCodingAgentTabToolsEnabled() {
			reg.RegisterSessionTabManager(&integration.StubSessionTabManager{ProviderName: "session_tabs"})
		}
	}
	if !reg.HasAny() {
		return ""
	}
	return mcp.GenerateSkillsDoc(mcp.NewToolRegistry(reg))
}
