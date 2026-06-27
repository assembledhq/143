// Package identity resolves the GitHub token and commit-author attribution
// for a session. It is the single source of truth for "who acts as whom"
// across the codebase — the PR-creation flow and the per-session sandbox
// credential helper both call into the same Resolver, so an agent's pushes
// and the user's "Create PR" click produce identical attribution.
//
// The resolver tries the triggering user's GitHub App user-to-server token
// first (via AppUserAuthService, which transparently refreshes within a
// 5-minute window). If no user token is available — or if the user can't
// access the repo and the org allows app fallback — it falls back to the
// GitHub App installation token.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

const defaultAPIBaseURL = "https://api.github.com"

// ErrUserAuthRequired signals that org policy (or an explicit "user" mode)
// requires a GitHub user token, but the triggering user is not connected or
// has no valid credential. Callers translate this into an actionable error
// for the UI ("connect your GitHub account").
var ErrUserAuthRequired = errors.New("github user auth required")

// ErrUserAuthRepoAccessDenied signals that the triggering user has a valid
// GitHub credential, but it cannot access the target repository — typically a
// private repo the user has not been granted on.
var ErrUserAuthRepoAccessDenied = errors.New("github user auth cannot access repository")

// Source identifies which authority issued the resolved token.
type Source string

const (
	SourceUser Source = "user"
	SourceApp  Source = "app"
)

// Resolution carries the resolved token along with enough context for callers
// to attribute commits, audit which identity was used, and decide whether to
// append a Co-authored-by trailer for app-token fallbacks.
type Resolution struct {
	Token  string
	Source Source
	// User is populated when Source == SourceUser. It is also populated when
	// Source == SourceApp and the resolver was able to look up the human
	// triggerer of the run, so callers can produce a Co-authored-by trailer
	// without an extra DB round trip.
	User *models.User
	// ExpiresAt is best-effort: zero for installation tokens (the underlying
	// service hides expiry behind a cache) and the OAuth-reported expiry for
	// user tokens. Never trust this for hard cutoffs — always re-resolve.
	ExpiresAt time.Time
}

// IsUserToken reports whether the resolution authenticates as the triggering
// user rather than as the GitHub App installation.
func (r *Resolution) IsUserToken() bool {
	return r != nil && r.Source == SourceUser
}

// AuthoredBy returns the persisted "authored_by" string used by audit columns
// (pull_requests.authored_by, session_execution_metadata.git_identity_source).
func (r *Resolution) AuthoredBy() string {
	if r != nil && r.Source == SourceUser {
		return "user"
	}
	return "app"
}

// InstallationTokenSource issues short-lived GitHub App installation tokens.
// Implemented by *github.Service.
type InstallationTokenSource interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// AppUserAuthService exchanges and refreshes GitHub App user-to-server
// tokens. Implemented by *github.AppUserAuthService.
type AppUserAuthService interface {
	GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}

// IntegrationStore reads GitHub integrations to recover an installation_id
// when the repository row is missing it or has a stale value.
type IntegrationStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Integration, error)
}

// UserStore looks up the human triggerer of a session for commit-author
// attribution and Co-authored-by trailers.
type UserStore interface {
	GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

// Resolver picks the right GitHub identity for a session. It is safe for
// concurrent use; all dependencies are read-only or thread-safe.
type Resolver struct {
	tokens       InstallationTokenSource
	appUserAuth  AppUserAuthService
	users        UserStore
	integrations IntegrationStore
	httpClient   *http.Client
	apiBaseURL   string
	logger       zerolog.Logger
}

// NewResolver constructs a Resolver with sensible HTTP defaults. The
// installation-token source is required; everything else is optional and can
// be wired later via the Set* methods.
func NewResolver(tokens InstallationTokenSource, logger zerolog.Logger) *Resolver {
	return &Resolver{
		tokens:     tokens,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiBaseURL: defaultAPIBaseURL,
		logger:     logger,
	}
}

// SetAppUserAuth wires the refresh-aware GitHub App user auth service. When
// nil (the zero value), Resolve always returns app-installation tokens.
func (r *Resolver) SetAppUserAuth(auth AppUserAuthService) { r.appUserAuth = auth }

// SetUsers wires the user store used to attach the triggering user to a
// resolution (so commit identity and Co-authored-by trailers don't need an
// extra DB lookup at the call site).
func (r *Resolver) SetUsers(users UserStore) { r.users = users }

// SetIntegrations wires the GitHub integration store used to recover an
// installation_id when the repository row is missing or stale.
func (r *Resolver) SetIntegrations(integrations IntegrationStore) { r.integrations = integrations }

// SetHTTPClient overrides the HTTP client used for repo-access probes
// (testing).
func (r *Resolver) SetHTTPClient(c *http.Client) { r.httpClient = c }

// SetAPIBaseURL overrides the GitHub API base URL (testing).
func (r *Resolver) SetAPIBaseURL(u string) { r.apiBaseURL = strings.TrimRight(u, "/") }

// HasAppUserAuth reports whether a GitHub App user auth service has been
// wired. Exposed for wiring tests and conditional-init paths.
func (r *Resolver) HasAppUserAuth() bool { return r.appUserAuth != nil }

// Resolve picks the right GitHub token for a session.
//
// Order of preference:
//  1. mode == "app" — explicit caller override; skip user-token lookup.
//  2. orgSettings.PRAuthorship == app_only — same.
//  3. The triggering user's GitHub App user token, if it exists and can
//     access the target repo. (When the user is connected but lacks repo
//     access, fall through to App fallback unless the org requires user
//     auth or the caller passed mode == "user".)
//  4. The GitHub App installation token, with integration-table fallback
//     when the repository row's installation_id is missing or stale.
//
// Returns an error when org policy requires user auth but no usable user
// token was found.
func (r *Resolver) Resolve(ctx context.Context, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings, mode string) (*Resolution, error) {
	if mode == "app" {
		return r.installationTokenResolution(ctx, run.OrgID, repo, true)
	}
	if orgSettings.PRAuthorship == models.PRAuthorshipAppOnly {
		return r.installationTokenResolution(ctx, run.OrgID, repo, true)
	}

	if run.TriggeredByUserID != nil && r.appUserAuth != nil {
		cfg, err := r.appUserAuth.GetValidCredential(ctx, run.OrgID, *run.TriggeredByUserID)
		if err == nil && cfg != nil && cfg.AccessToken != "" {
			canAccess, accessErr := r.userTokenCanAccessRepo(ctx, cfg.AccessToken, repo.FullName)
			if accessErr != nil {
				return nil, fmt.Errorf("check user github access: %w", accessErr)
			}
			if canAccess {
				resolution := &Resolution{
					Token:     cfg.AccessToken,
					Source:    SourceUser,
					ExpiresAt: cfg.ExpiresAt,
				}
				if r.users != nil {
					if user, userErr := r.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID); userErr == nil {
						resolution.User = &user
					} else {
						// Non-fatal: the token still works for the push, we just
						// won't be able to set git author or emit a Co-author trailer.
						r.logger.Warn().Err(userErr).Str("user_id", run.TriggeredByUserID.String()).Msg("identity: failed to attach user to resolution")
					}
				}
				return resolution, nil
			}
			if orgSettings.PRAuthorship == models.PRAuthorshipUserRequired || mode == "user" {
				return nil, fmt.Errorf("%w: user GitHub auth cannot access repo %s", ErrUserAuthRepoAccessDenied, repo.FullName)
			}
		}
	}

	if orgSettings.PRAuthorship == models.PRAuthorshipUserRequired || mode == "user" {
		return nil, fmt.Errorf("%w: org requires user GitHub auth, but no valid user token found", ErrUserAuthRequired)
	}

	return r.installationTokenResolution(ctx, run.OrgID, repo, true)
}

// InstallationTokenForRepo issues an App installation token for repo without
// any user-token resolution. Useful for callers that always want an App
// token (webhook-triggered jobs, system-initiated syncs) and want
// installation-table fallback for free.
//
// When triggeringUserID is non-nil and the user store is wired, the returned
// resolution includes the user record so callers can produce a
// Co-authored-by trailer without an extra DB round trip.
func (r *Resolver) InstallationTokenForRepo(ctx context.Context, orgID uuid.UUID, repo *models.Repository, triggeringUserID *uuid.UUID) (*Resolution, error) {
	res, err := r.installationTokenResolution(ctx, orgID, repo, false)
	if err != nil {
		return nil, err
	}
	if triggeringUserID != nil && r.users != nil {
		if user, userErr := r.users.GetByID(ctx, orgID, *triggeringUserID); userErr == nil {
			res.User = &user
		}
	}
	return res, nil
}

// installationTokenResolution wraps the raw installation-token call into a
// Resolution. The wrapErr flag controls whether the error is wrapped with the
// "get installation token" prefix — wrap when this is the terminal call in a
// public Resolve path, leave bare when the caller adds its own context.
func (r *Resolver) installationTokenResolution(ctx context.Context, orgID uuid.UUID, repo *models.Repository, wrapErr bool) (*Resolution, error) {
	token, err := r.installationToken(ctx, orgID, repo)
	if err != nil {
		if wrapErr {
			return nil, fmt.Errorf("get installation token: %w", err)
		}
		return nil, err
	}
	return &Resolution{Token: token, Source: SourceApp}, nil
}

// installationToken returns an App installation token for repo, falling back
// to the integration's stored installation_id when the repo row is missing
// or stale (404 from the installations API).
func (r *Resolver) installationToken(ctx context.Context, orgID uuid.UUID, repo *models.Repository) (string, error) {
	tryInstallation := func(installationID int64) (string, error) {
		if installationID <= 0 {
			return "", fmt.Errorf("repository %s has no github installation_id", repo.FullName)
		}
		token, err := r.tokens.GetInstallationToken(ctx, installationID)
		if err != nil {
			return "", fmt.Errorf("get installation token for installation %d: %w", installationID, err)
		}
		return token, nil
	}

	var primaryErr error
	if repo.InstallationID > 0 {
		token, err := tryInstallation(repo.InstallationID)
		if err == nil {
			return token, nil
		}
		primaryErr = err
		if !shouldRetryWithIntegrationInstallation(err) {
			return "", err
		}
	} else {
		primaryErr = fmt.Errorf("repository %s has no github installation_id", repo.FullName)
	}

	if r.integrations == nil || repo.IntegrationID == uuid.Nil {
		return "", primaryErr
	}

	integration, err := r.integrations.GetByID(ctx, repo.IntegrationID)
	if err != nil {
		return "", primaryErr
	}

	fallbackInstallationID, err := integrationInstallationID(&integration)
	if err != nil {
		return "", primaryErr
	}
	if fallbackInstallationID == repo.InstallationID {
		return "", primaryErr
	}

	token, err := tryInstallation(fallbackInstallationID)
	if err != nil {
		return "", err
	}

	r.logger.Warn().
		Str("org_id", orgID.String()).
		Str("repo", repo.FullName).
		Str("integration_id", repo.IntegrationID.String()).
		Int64("repo_installation_id", repo.InstallationID).
		Int64("fallback_installation_id", fallbackInstallationID).
		Msg("using GitHub integration installation fallback")

	return token, nil
}

// shouldRetryWithIntegrationInstallation matches errors that surface as a 404
// from the GitHub installation-token API — indicating the repo's recorded
// installation_id no longer exists. Uses a structural check (HTTPStatus
// method) so we don't have to import the github package's concrete error type.
func shouldRetryWithIntegrationInstallation(err error) bool {
	var sc interface{ HTTPStatus() int }
	if errors.As(err, &sc) && sc.HTTPStatus() == http.StatusNotFound {
		return true
	}
	return false
}

func integrationInstallationID(integration *models.Integration) (int64, error) {
	if integration == nil || len(integration.Config) == 0 {
		return 0, fmt.Errorf("github integration config missing installation_id")
	}
	var cfg struct {
		InstallationID int64 `json:"installation_id"`
	}
	if err := json.Unmarshal(integration.Config, &cfg); err != nil {
		return 0, fmt.Errorf("parse github integration config: %w", err)
	}
	if cfg.InstallationID <= 0 {
		return 0, fmt.Errorf("github integration config missing installation_id")
	}
	return cfg.InstallationID, nil
}

// userTokenCanAccessRepo probes /repos/{owner}/{repo} with the user token to
// distinguish "user has GitHub but isn't a collaborator" (403/404 — fall
// through) from real errors (500, network — propagate).
func (r *Resolver) userTokenCanAccessRepo(ctx context.Context, token, fullName string) (bool, error) {
	owner, repo := splitRepo(fullName)
	if owner == "" || repo == "" {
		return false, fmt.Errorf("invalid repo full name %q", fullName)
	}
	url := r.apiBaseURL + fmt.Sprintf("/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("build repo access request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("repo access request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("github repo access check returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func splitRepo(fullName string) (owner, repo string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// CommitIdentity returns the git author name and email for a resolution.
// When a user is attached, prefers their GitHub-attribution noreply email
// (so commits link to the GitHub profile) and falls back to their contact
// email. With no user attached, returns the bot identity.
func CommitIdentity(res *Resolution) (name, email string) {
	if res != nil && res.IsUserToken() && res.User != nil {
		return res.User.Name, GitEmail(res.User)
	}
	return "143 Agent", "noreply@143.dev"
}

// GitEmail returns the email address to attribute commits to so GitHub will
// link them back to the user's profile. Prefers GitHubNoreplyEmail (the
// user-id-prefixed `{id}+{login}@users.noreply.github.com` form, which
// GitHub matches against their account) and falls back to the contact email.
func GitEmail(u *models.User) string {
	if u == nil {
		return ""
	}
	if u.GitHubNoreplyEmail != nil && *u.GitHubNoreplyEmail != "" {
		return *u.GitHubNoreplyEmail
	}
	return u.Email
}

// CoAuthorTrailer returns a `Co-authored-by:` line for user, suitable for
// appending to a commit message body. Returns "" when user is nil so callers
// can unconditionally concatenate.
func CoAuthorTrailer(user *models.User) string {
	if user == nil {
		return ""
	}
	return fmt.Sprintf("Co-authored-by: %s <%s>", user.Name, GitEmail(user))
}
