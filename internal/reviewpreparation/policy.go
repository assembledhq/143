// Package reviewpreparation defines the process-wide activation boundary for
// mandatory code-review workspace preparation. Repository names are deployment
// configuration, never compiled into this policy.
package reviewpreparation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// Policy is immutable after construction. Its zero value and nil are disabled.
type Policy struct {
	enabled      bool
	repositories map[string]struct{}
}

var fullNamePattern = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)

func repositoryName(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !fullNamePattern.MatchString(value) {
		return "", fmt.Errorf("review preparation repository identity is missing or invalid")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("review preparation repository identity is invalid")
		}
	}
	return value, nil
}

// NewPolicy copies and validates configuration. An empty repository list keeps
// preparation disabled even when the global switch is enabled.
func NewPolicy(enabled bool, repositories []string) (*Policy, error) {
	policy := &Policy{enabled: enabled, repositories: make(map[string]struct{}, len(repositories))}
	for _, repository := range repositories {
		name, err := repositoryName(repository)
		if err != nil {
			return nil, err
		}
		policy.repositories[name] = struct{}{}
	}
	return policy, nil
}

func (p *Policy) Enabled() bool { return p != nil && p.enabled && len(p.repositories) != 0 }

type RepositoryStore interface {
	GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error)
}

// ForSession resolves the canonical repository through its tenant-scoped ID.
// Once configured, lookup and identity failures are errors, never an opt-out.
func (p *Policy) ForSession(ctx context.Context, store RepositoryStore, session *models.Session) (bool, error) {
	if !p.Enabled() || session == nil || session.Origin != models.SessionOriginCodeReview {
		return false, nil
	}
	if session.RepositoryID == nil || *session.RepositoryID == uuid.Nil || session.OrgID == uuid.Nil {
		return false, fmt.Errorf("review preparation session repository identity is missing")
	}
	if store == nil {
		return false, fmt.Errorf("review preparation repository store unavailable")
	}
	repository, err := store.GetByID(ctx, session.OrgID, *session.RepositoryID)
	if err != nil {
		return false, fmt.Errorf("load review preparation repository: %w", err)
	}
	return p.ForRepository(session, repository)
}

// ForRepository validates all available canonical identities before matching.
func (p *Policy) ForRepository(session *models.Session, repository models.Repository) (bool, error) {
	if !p.Enabled() || session == nil || session.Origin != models.SessionOriginCodeReview {
		return false, nil
	}
	if session.RepositoryID == nil || repository.ID == uuid.Nil || repository.ID != *session.RepositoryID || session.OrgID == uuid.Nil || repository.OrgID != session.OrgID {
		return false, fmt.Errorf("review preparation repository does not match the session tenant and identity")
	}
	name, err := repositoryName(repository.FullName)
	if err != nil {
		return false, err
	}
	_, active := p.repositories[name]
	var revision struct {
		GitHubRepo string `json:"github_repo"`
	}
	if err := json.Unmarshal(session.RevisionContext, &revision); err != nil {
		if active {
			return false, fmt.Errorf("parse review preparation revision identity: %w", err)
		}
	} else if revision.GitHubRepo != "" || active {
		revisionName, err := repositoryName(revision.GitHubRepo)
		if err != nil {
			return false, err
		}
		if revisionName != name {
			return false, fmt.Errorf("review preparation repository and revision identities conflict")
		}
	}
	clone := strings.TrimSpace(repository.CloneURL)
	if strings.HasPrefix(strings.ToLower(clone), "git@github.com:") {
		clone = "ssh://git@github.com/" + clone[len("git@github.com:"):]
	}
	if parsed, parseErr := url.Parse(clone); parseErr == nil && parsed.Hostname() != "" && (parsed.Scheme == "https" || parsed.Scheme == "http" || parsed.Scheme == "ssh" || parsed.Scheme == "git") {
		cloneName, nameErr := repositoryName(strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git"))
		if nameErr != nil || cloneName != name {
			return false, fmt.Errorf("review preparation repository and clone identities conflict")
		}
	}
	return active, nil
}
