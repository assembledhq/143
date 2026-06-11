# Design: PR Creation Revamp — User-Authored PRs, Template Support, and Issueless Sessions

> **Status:** Implemented | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** PR template detection and caching implemented (`pr_templates` store). The on-demand `Create PR` authorization flow uses encrypted `github_app_user` credentials with refresh-aware validation, app fallback, and frontend auto-resume after callback. The implementation reuses `/api/v1/users/me/github/*` routes for PR authorship instead of introducing separate `/github-app/*` endpoints. The session UI distinguishes stale GitHub PR resume tokens from snapshot-unavailable PR creation states instead of collapsing both into a generic "session expired" banner. Issueless session support is tracked separately and is no longer considered part of this doc's scope.
> PR content generation also includes bounded summaries from all visible session threads so a recently-created review loop is treated as supporting context, not as the whole PR story. The diff and files changed remain the reviewer-facing source of truth; issue/session title and thread summaries help explain intent and validation.

**Depends on**: [08-pr-and-ship.md](08-pr-and-ship.md), [13-repository-onboarding.md](13-repository-onboarding.md), [34-personal-team-coding-agents.md](34-personal-team-coding-agents.md)

---

## 1. Problem Statement

The current PR creation system has three limitations:

1. **Bot-authored PRs** — All PRs are created as the 143 GitHub App. Reviewers see a bot as the author, which reduces trust and breaks workflows that rely on CODEOWNERS, required reviewers, or "who wrote this" attribution. Every top AI coding tool (Codex, Claude Code, Cursor) creates PRs as the user.

2. **No repo PR template support** — We generate a hardcoded markdown body with issue metadata and validation tables. Repos that have `.github/pull_request_template.md` expect PRs to follow that structure. Our PRs look foreign.

3. **Issue-required assumption** — `CreatePR` requires `run.IssueID` to fetch title, severity, source, etc. Manually created sessions (the growing majority of usage) may not have a meaningful issue attached, causing the PR body to be filled with empty/irrelevant fields.

---

## 2. Goals

- Let users create PRs **as themselves** via GitHub App user access tokens
- Fall back to the GitHub App when a user hasn't connected GitHub or for automated/unattended sessions
- Detect and use the repo's existing PR template, filling it in with session context
- Fall back to a minimal default template when no repo template exists
- Support sessions with and without attached issues
- Keep the change backward-compatible — existing orgs see no disruption

---

## 3. User GitHub Auth for PR Creation

### 3.1 Current State

Today we have two GitHub auth mechanisms:

| Mechanism | Purpose | Scopes |
|-----------|---------|--------|
| **GitHub OAuth App** | User identity (sign-in) | `read:user`, `user:email` |
| **GitHub App installation** | Repo access (clone, push, create PRs) | Contents R/W, PRs R/W, Checks R, Deployments R |

The GitHub OAuth token remains useful for identity, but it should not be the source of repo-write authority. Creating a PR "as the user" should use a GitHub App **user access token** (user-to-server auth), not a broad OAuth app token with `repo` scope.

### 3.2 Chosen Approach: GitHub App User Access Tokens

GitHub App user access tokens are the right fit for PR authorship because they are constrained by three things at once:

- the user's own access to the repo
- the GitHub App installation's access to the repo
- the GitHub App's granted permissions

That matches 143's trust model better than reusing GitHub login OAuth:

- login proves identity
- the app installation proves repo connectivity
- user access tokens authorize "act on behalf of this human in this installed repo context"

This also keeps all repo-mutating behavior aligned with the GitHub App model the rest of the product already uses.

### 3.3 Why We Are Not Expanding Login OAuth

The rejected alternative is to expand the login OAuth app from `read:user,user:email` to `read:user,user:email,repo`.

That would work technically, but it is the wrong long-term security shape:

- `repo` is broader than needed
- it grants repo access outside the app-installation boundary
- it conflates authentication for login with authorization for repo writes

The product should treat these as two distinct user consents:

- "sign me in with GitHub"
- "let the 143 GitHub App create PRs on my behalf"

### 3.4 Token Storage Model

Store GitHub App user tokens in `user_credentials` as a new provider distinct from login OAuth:

```sql
INSERT INTO user_credentials (user_id, org_id, provider, config, status)
VALUES ($1, $2, 'github_app_user', encrypt({
    "access_token": "ghu_xxxx",
    "refresh_token": "ghr_xxxx",
    "token_type": "bearer",
    "expires_at": "2026-04-21T22:10:00Z",
    "refresh_token_expires_at": "2026-10-18T22:10:00Z"
}), 'active');
```

Proposed model additions in `internal/models/credentials.go`:

```go
const (
    ProviderGitHubOAuth   ProviderName = "github_oauth"
    ProviderGitHubAppUser ProviderName = "github_app_user"
)

type GitHubAppUserConfig struct {
    AccessToken           string     `json:"access_token"`
    RefreshToken          string     `json:"refresh_token,omitempty"`
    TokenType             string     `json:"token_type"`
    ExpiresAt             *time.Time `json:"expires_at,omitempty"`
    RefreshTokenExpiresAt *time.Time `json:"refresh_token_expires_at,omitempty"`
}
```

Provider split:

- `github_oauth`: login identity
- `github_app_user`: repo actions on behalf of the user

### 3.5 Auth Flow Changes

Add a dedicated GitHub App authorization flow for PR authorship. This is separate from `/api/v1/auth/github/*`.

Routes used for PR authorship:

```text
GET  /api/v1/users/me/github/connect
GET  /api/v1/users/me/github/callback
POST /api/v1/users/me/github/disconnect
GET  /api/v1/users/me/github-status
```

Flow:

1. User clicks `Create PR`
2. 143 checks whether the triggering user already has a valid `github_app_user` credential
3. If yes, PR creation continues immediately
4. If no, the backend returns a typed "authorization required" response with a connect URL
5. The frontend opens a lightweight modal and sends the user through GitHub App authorization
6. GitHub redirects back with an authorization `code`
7. 143 exchanges the code for a GitHub App user access token and stores it under `provider = github_app_user`
8. 143 redirects the user back to the same session page with enough state to auto-resume PR creation
9. Future PR creation attempts reuse the stored credential and refresh it silently when possible
10. Concurrent auth flows in multiple tabs must bind the PR `resume_token` to the OAuth `state` so callbacks resume the correct session

Handler shape:

```go
func (h *GitHubAppUserHandler) HandleConnectCallback(w http.ResponseWriter, r *http.Request) {
    code, ok := validateOAuthCallback(w, r, githubAppUserStateCookie)
    if !ok {
        return
    }

    user := middleware.UserFromContext(r.Context())
    orgID := middleware.OrgIDFromContext(r.Context())

    tokenResp, err := h.githubAppAuth.ExchangeUserCode(r.Context(), code)
    if err != nil {
        writeError(w, r, http.StatusBadGateway, "TOKEN_EXCHANGE_FAILED", "failed to exchange GitHub App user code", err)
        return
    }

    cfg := models.GitHubAppUserConfig{
        AccessToken:           tokenResp.AccessToken,
        RefreshToken:          tokenResp.RefreshToken,
        TokenType:             tokenResp.TokenType,
        ExpiresAt:             tokenResp.ExpiresAt,
        RefreshTokenExpiresAt: tokenResp.RefreshTokenExpiresAt,
    }
    if err := h.userCredentials.Upsert(r.Context(), user.ID, orgID, cfg, false); err != nil {
        writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store GitHub App user credential", err)
        return
    }

    // sessionID/return target comes from signed state or a short-lived cookie
    // captured when PR creation initiated the connect flow.
    http.Redirect(w, r, h.frontendURL+"/sessions/"+sessionID+"?github_pr=connected&resume_pr=1", http.StatusTemporaryRedirect)
}
```

### 3.6 Token Refresh Lifecycle

Unlike login OAuth, GitHub App user tokens may expire and require refresh. We therefore need a small token manager in `internal/services/github/`.

Concrete service shape:

```go
type AppUserAuthService interface {
    ExchangeCode(ctx context.Context, code string) (*models.GitHubAppUserConfig, error)
    HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error)
    GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}
```

Behavior:

1. Load `github_app_user` credential
2. If `expires_at` is comfortably in the future, return the access token
3. Otherwise refresh using `refresh_token`
4. Persist the rotated token pair
5. If refresh fails because the user revoked access or the refresh token expired:
   - disable the credential
   - return a typed auth error so the UI can prompt reconnect

We only need lazy refresh during PR creation for v1. The intended UX is "authorize once when you first try to create a PR, then stay connected unless GitHub requires reauthorization later."

### 3.7 Token Resolution Order

When creating a PR, resolve the token in this order:

```text
1. Triggering user's GitHub App user token
2. GitHub App installation token
```

Updated `resolveToken` shape:

```go
func (s *PRService) resolveToken(ctx context.Context, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings) (*tokenResolution, error) {
    if orgSettings.PRAuthorship != models.PRAuthorshipAppOnly && run.TriggeredByUserID != nil {
        token, user, err := s.githubAppUserTokens.GetValidToken(ctx, run.OrgID, *run.TriggeredByUserID)
        if err == nil {
            return &tokenResolution{Token: token, IsUserToken: true, User: user}, nil
        }
        if orgSettings.PRAuthorship == models.PRAuthorshipUserRequired {
            return nil, err
        }
        s.logger.Warn().Err(err).Msg("falling back to installation token for PR creation")
    }

    token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
    if err != nil {
        return nil, fmt.Errorf("get installation token: %w", err)
    }
    return &tokenResolution{Token: token, IsUserToken: false}, nil
}
```

Before using a user token, 143 should validate that:

- the GitHub App is installed on the repo owner/account
- the user token can access that installation
- the user still has access to the repo

If any of those checks fail:

- `user_preferred`: fall back to installation token
- `user_required`: block PR creation and prompt reconnect

### 3.8 Commit Author Attribution

When using a GitHub App user access token:

- set local git author name/email to the triggering user
- push and PR creation use the user token
- GitHub will attribute the action to the user, with the GitHub App badge overlay

When using the installation token (fallback), keep the current bot flow and add a `Co-authored-by` trailer when a triggering user exists.

### 3.9 Org-Level Configuration

PR authorship policy still lives in `organizations.settings`:

| Mode | Behavior |
|------|----------|
| `user_preferred` (default) | Use GitHub App user token if available, else fall back to installation token |
| `app_only` | Always use installation token |
| `user_required` | Require GitHub App user authorization; block PR creation otherwise |

No new org settings are needed beyond the already-existing `pr_authorship` and `pr_draft_default`.

### 3.10 PR Authorship UX

The UX should be **on-demand**, not a permanent setup hurdle. We should only ask for GitHub App user authorization the first time a user actually presses `Create PR` and needs user-authored PRs.

#### Session Detail

When connected:

```text
PR will be opened as @janedoe
```

When not connected, the default idle state should stay simple:

```text
[ Create PR ]
```

No persistent secondary CTA is required before the user clicks. The first click is the trigger point for auth if needed.

#### First Create PR Click: `user_preferred`

If the org is `user_preferred` and the user has not authorized the GitHub App yet, the backend should return a typed response and the frontend should show a modal like:

```text
Open this pull request as yourself?

Authorize GitHub once to open PRs as you. If you skip this, 143 can still open the PR as the app.

[ Continue with GitHub ] [ Create as 143 ]
```

Behavior:

- `Continue with GitHub` sends the user through the GitHub App authorization flow
- after callback, the session page auto-retries PR creation
- `Create as 143` immediately retries PR creation using the installation token

#### First Create PR Click: `user_required`

If the org is `user_required` and the user has not authorized yet, the modal becomes blocking:

```text
Authorize GitHub to create this pull request as yourself

You only need to do this the first time unless GitHub requires reauthorization later.

[ Continue with GitHub ]
```

The `Create PR` button does not need to start disabled in the steady state. The click itself can be the intercept point.

#### Automatic Resume After Callback

After successful authorization, the user should land back on the same session detail page and **not** need to click `Create PR` again.

The canonical request/response shapes live in the backend contract below. The important UX rule is: after callback, the user returns to the same session page and the frontend automatically retries PR creation using the short-lived resume token issued by the backend.

#### Backend Contract for PR Authorship Intercept

The auth-on-first-PR UX needs an explicit backend contract so the API, callback, and frontend resume behavior stay stable.

##### `POST /api/v1/sessions/{id}/pr`

Request body:

```json
{
  "draft": false,
  "author_mode": "auto"
}
```

`author_mode` values:

| Value | Meaning |
|------|---------|
| `auto` | Default. Use org policy and available credentials. May trigger auth-required response. |
| `user` | Caller explicitly wants a user-authored PR. If unavailable, return auth-required or auth-failed instead of falling back silently. |
| `app` | Caller explicitly wants an app-authored PR. Skip user-token lookup. |

`author_mode` is optional. If omitted, treat it as `auto`.

Success response remains the normal PR payload:

```json
{
  "id": "pr-db-id",
  "session_id": "session-123",
  "github_pr_number": 42,
  "github_pr_url": "https://github.com/acme/repo/pull/42",
  "authored_by": "user"
}
```

##### Auth Intercept Response

When the org is `user_preferred` or `user_required`, the request wants a user-authored PR, and no valid `github_app_user` credential is available, return `409 Conflict`:

```json
{
  "error": {
    "code": "GITHUB_PR_AUTHORSHIP_REQUIRED",
    "message": "Authorize GitHub to create this pull request as you."
  },
  "details": {
    "session_id": "session-123",
    "connect_url": "/api/v1/users/me/github/connect?flow=pr_authorship",
    "resume_token": "opaque-short-lived-token",
    "can_fallback_to_app": true,
    "suggested_author_mode": "user"
  }
}
```

Contract notes:

- `resume_token` is generated server-side, short-lived, single-use, and signed or stored server-side
- `resume_token` encodes the minimum state required to resume the flow:
  - `session_id`
  - requesting `user_id`
  - `org_id`
  - requested PR params such as `draft`
  - fallback eligibility
- `connect_url` should not carry raw session state directly; it should only reference `resume_token`

If the user explicitly requested `author_mode=app`, do not return this response.

##### Fallback-to-App Retry

If the modal offers `Create as 143`, the frontend should retry:

```json
{
  "draft": false,
  "author_mode": "app"
}
```

That makes the fallback explicit and avoids backend ambiguity about whether the second attempt should still try user auth.

##### `GET /api/v1/users/me/github/connect`

This endpoint starts GitHub App user authorization.

Required query params:

```text
flow=pr_authorship
resume_token=<opaque-token>
```

Behavior:

- validate the current authenticated user matches the intended flow owner
- validate the `resume_token` is still active
- store a short-lived CSRF state cookie
- bind the `resume_token` to the OAuth `state` (for example via a state-scoped cookie name) so concurrent tabs cannot overwrite one another
- redirect to GitHub's user authorization URL

##### `GET /api/v1/users/me/github/callback`

Behavior after GitHub redirects back:

1. Validate state cookie
2. Exchange code for GitHub App user token
3. Upsert `github_app_user` credential
4. Load the bound `resume_token`
5. Validate it has not expired and belongs to the current user/org
6. Redirect back to the originating session page with a lightweight resume marker

Recommended redirect:

```text
/sessions/{id}?github_pr=connected&resume_pr=<resume_token>
```

The callback should not directly create the PR. It should only restore the user to the product UI with enough state for the frontend to retry safely.

##### Frontend Resume Retry

When the frontend sees `resume_pr=<resume_token>` on the session page, it should automatically issue:

```json
{
  "draft": false,
  "author_mode": "user",
  "resume_token": "opaque-short-lived-token"
}
```

Why send `resume_token` back on the retry:

- prevents accidental auto-submit on unrelated page loads
- lets the backend correlate the resumed request to the original intercepted request
- allows single-use invalidation after successful retry

##### Resume Validation Rules

On resumed `POST /api/v1/sessions/{id}/pr`, the backend should verify:

- `resume_token` exists and is unexpired
- token `session_id` matches the route param
- token `user_id` matches the current authenticated user
- token `org_id` matches the current org context
- token has not already been consumed

If validation fails, return `409` with:

```json
{
  "error": {
    "code": "PR_RESUME_EXPIRED",
    "message": "GitHub authorization completed, but the PR resume request expired. Please click Create PR again."
  }
}
```

##### Typed Auth Failure Response

If a user had previously authorized GitHub but refresh/revalidation fails, return `403 Forbidden`:

```json
{
  "error": {
    "code": "GITHUB_PR_AUTHORSHIP_REAUTH_REQUIRED",
    "message": "GitHub needs you to reauthorize before this pull request can be opened as you."
  },
  "details": {
    "connect_url": "/api/v1/users/me/github-app/connect?flow=pr_authorship",
    "resume_token": "opaque-short-lived-token",
    "can_fallback_to_app": true
  }
}
```

This lets the frontend distinguish:

- first-time connect required
- reauthorization required after expiry/revocation
- normal app fallback path

#### Settings / Account

Show two separate statuses:

```text
GitHub login
Connected as @janedoe

GitHub PR authorship
Authorized for user-authored PRs
```

This avoids the current ambiguity where "signed in with GitHub" might be mistaken for "143 can create PRs as me."

Recommended copy:

- connected: `Authorized for PRs opened as you`
- disconnected: `Authorize when you first create a pull request`

#### Frontend Status API

Expand `GET /api/v1/users/me/github-status`:

```json
{
  "connected": true,
  "github_login": "janedoe",
  "pr_authorship_mode": "user_preferred",
  "pr_draft_default": false,
  "github_app_user_connected": true,
  "github_app_user_expires_at": "2026-04-21T22:10:00Z"
}
```

The frontend should use `github_app_user_connected`, not `github_oauth`, to decide whether the next PR can be user-authored.

### 3.11 Failure Modes

PR authorship now has a few explicit failure classes:

| Failure | Behavior |
|---------|----------|
| User never authorized the GitHub App | `user_preferred`: fall back to app. `user_required`: block and show connect CTA. |
| Access token expired but refresh succeeds | Refresh in-line, continue. |
| Access token expired and refresh fails | Disable credential, prompt reconnect. |
| User lost repo/org access | Treat as auth failure for that repo; do not use stale cached access. |
| Org uses SAML SSO and user lacks active SSO session | Show reconnect / reauthorize guidance. |
| App no longer installed on repo owner | Surface installation problem; user token cannot bypass it. |

The product should not promise "never authenticate again." The correct expectation is:

- authorize on first `Create PR`
- remain connected via silent refresh afterward
- rarely reauthorize only if GitHub invalidates the credential or org policy requires it

### 3.12 Migration Plan

1. Keep `github_oauth` for login unchanged.
2. Add `ProviderGitHubAppUser` and config parsing.
3. Add GitHub App user-authorization endpoints and UI CTA.
4. Add token refresh logic.
5. Switch `PRService.resolveToken` to prefer `github_app_user`.
6. Keep installation-token fallback for existing users.

This is fully backward-compatible. Existing orgs continue using bot-authored PRs until a user explicitly authorizes the GitHub App for PR authorship.

### 3.13 Security Properties

Compared with expanding login OAuth to `repo`, this design is materially safer:

- permissions remain fine-grained to the GitHub App
- access is limited to accounts where the app is installed
- tokens are short-lived and refreshable
- authorization is bounded by both app and user privileges

The main implementation responsibilities are:

- encrypt refresh tokens at rest
- refresh tokens atomically
- disable revoked credentials quickly
- avoid treating historical authorization as current org access

### 3.14 Implementation Notes for This Repo

Concrete code changes likely touch:

- `internal/models/credentials.go`
  add `ProviderGitHubAppUser` and `GitHubAppUserConfig`
- `internal/api/handlers/github_status.go`
  switch from OAuth-scope language to GitHub App user-auth language
- new service in `internal/services/github/`
  exchange + refresh GitHub App user tokens
- `internal/services/github/pr.go`
  replace `ProviderGitHubOAuth` lookup with `ProviderGitHubAppUser`
- `docs/self-hosting/github-app-setup.md`
  document enabling user authorization on the GitHub App

The existing login OAuth handler in `internal/api/handlers/auth.go` should remain login-only.

---

## 4. PR Template Support

### 4.1 Detecting Repo Templates

GitHub repos can have PR templates at several conventional paths:

```
.github/pull_request_template.md
.github/PULL_REQUEST_TEMPLATE.md
docs/pull_request_template.md
pull_request_template.md
PULL_REQUEST_TEMPLATE.md
.github/PULL_REQUEST_TEMPLATE/default.md
```

Fetch the template via the GitHub Contents API during PR creation:

```go
func (s *PRService) fetchPRTemplate(ctx context.Context, token, owner, repo, defaultBranch string) (string, error) {
    paths := []string{
        ".github/pull_request_template.md",
        ".github/PULL_REQUEST_TEMPLATE.md",
        "docs/pull_request_template.md",
        "pull_request_template.md",
    }
    for _, path := range paths {
        content, err := s.getFileContents(ctx, token, owner, repo, path, defaultBranch)
        if err == nil && content != "" {
            return content, nil
        }
    }
    return "", nil // no template found
}
```

### 4.2 Filling In Repo Templates

When a repo template is found, use the LLM to fill it in based on session context. This ensures the PR description matches the team's expected format regardless of what sections they have.

```go
func (s *PRService) fillRepoTemplate(ctx context.Context, template string, run *models.Session, issue *models.Issue) (string, error) {
    prompt := buildTemplateFillPrompt(template, run, issue)
    filled, err := s.llm.Complete(ctx, prompt)
    if err != nil {
        // Fallback: return template with a summary prepended
        return fmt.Sprintf("## Summary\n\n%s\n\n---\n\n%s", summaryText(run), template), nil
    }
    return filled, nil
}
```

The prompt provides:
- The raw template text
- The session's `ResultSummary` (what the agent did)
- The diff stat summary (files changed, lines added/removed)
- Issue context if available (title, source, severity)
- Instructions to be concise and fill in only what's relevant, leaving optional sections empty or removing them

Key principle: **the LLM fills in the team's template, it does not invent new sections.** The prompt explicitly instructs it to preserve the template's structure, headings, checklists, and field ordering, fill the existing fields with the available session context, and avoid adding any non-template sections.

### 4.3 Default Template (Fallback)

When no repo template exists, use a minimal default. The current template is too verbose — validation tables, occurrence counts, and agent metadata are noise for reviewers. Replace with:

```markdown
## Summary

{1-3 sentence description of what changed and why}

## Test plan

{How this was validated — tests run, manual verification, etc.}

[143.dev](https://143.dev) | [session {run.id-short}]({dashboard_url})
```

That's it. Short, scannable, useful. The session link in the 143 dashboard provides full detail for anyone who wants it.

When an issue is attached, append a single line:

```markdown
## Summary

{description}

**Issue**: {source} — {title} ({severity})

## Test plan

{validation summary}

[143.dev](https://143.dev) | [session {run.id-short}]({dashboard_url})
```

### 4.4 Generating the Summary and Test Plan

Use the session's `ResultSummary` as the basis for the summary. For the test plan, pull from validation results if they exist, otherwise summarize from the agent's output.

```go
func (s *PRService) generatePRBody(ctx context.Context, token, owner, repoName, defaultBranch string, run *models.Session, issue *models.Issue) string {
    // 1. Try repo template
    template, _ := s.fetchPRTemplate(ctx, token, owner, repoName, defaultBranch)
    if template != "" {
        filled, err := s.fillRepoTemplate(ctx, template, run, issue)
        if err == nil {
            return filled + "\n\n[143.dev](https://143.dev) | [session {run.id-short}]({dashboard_url})\n"
        }
    }

    // 2. Fall back to minimal default
    return s.buildDefaultBody(ctx, run, issue)
}
```

---

## 5. Issueless Sessions

### 5.1 Problem

`CreatePR` currently requires `run.IssueID` to be a valid UUID and fetches the issue for title, severity, source, customer count, etc. Manually created sessions often have no meaningful issue — the user just started a session with a prompt.

### 5.2 Changes

Make `IssueID` optional in the PR creation path. The session itself has enough context:

| Field | Source |
|-------|--------|
| PR title | First-line `ResultSummary` when available, with minimal cleanup (trim/collapse whitespace, strip surrounding quotes, cap length); otherwise the cleaned `session.Title`, falling back to `"Session {id[:8]}"` |
| PR body summary | `session.ResultSummary` |
| Branch name | `143/{id[:8]}/{slugified-title}` — drop the `fix/` prefix for non-issue sessions |
| Commit message | `session.Title` or `ResultSummary` first line |
| Labels | `143-generated` only (no severity/source labels without an issue) |

### 5.3 Updated Title and Branch Logic

```go
func formatPRTitle(session *models.Session, issue *models.Issue) string {
    if issue != nil {
        switch issue.Source {
        case models.IssueSourceLinear:
            return fmt.Sprintf("%s: %s", issue.ExternalID, normalizePRTitleCandidate(issue.Title))
        default:
            return fmt.Sprintf("fix: %s", bestPRTitleSubject(session, issue.Title))
        }
    }

    if session.ResultSummary != nil && *session.ResultSummary != "" {
        return normalizePRTitleCandidate(firstLine(*session.ResultSummary))
    }
    if session.Title != nil && *session.Title != "" {
        return normalizePRTitleCandidate(*session.Title)
    }
    return fmt.Sprintf("Session %s", session.ID.String()[:8])
}

func formatBranchName(session *models.Session, issue *models.Issue) string {
    short := session.ID.String()[:8]
    var title string
    if issue != nil {
        title = issue.Title
    } else if session.Title != nil {
        title = *session.Title
    }
    slug := slugify(title)
    if slug == "" {
        slug = "changes"
    }
    return fmt.Sprintf("143/%s/%s", short, slug)
}
```

`normalizePRTitleCandidate` is intentionally minimal. It collapses whitespace, trims surrounding quotes, strips trailing punctuation, and caps the final title length, but it does not try to paraphrase or reinterpret the title text. The LLM should generate the actual reviewer-facing phrasing; fallback logic should stay deterministic and simple.

### 5.4 Updated CreatePR Flow

```go
func (s *PRService) CreatePR(ctx context.Context, run *models.Session) (*models.PullRequest, error) {
    // Issue lookup is now optional
    var issue *models.Issue
    if run.IssueID != uuid.Nil {
        i, err := s.issues.GetByID(ctx, run.OrgID, run.IssueID)
        if err == nil {
            issue = &i
        }
    }

    // Resolve repo — must still exist
    repoID := run.RepositoryID
    if repoID == nil && issue != nil {
        repoID = issue.RepositoryID
    }
    if repoID == nil {
        return nil, fmt.Errorf("session %s has no repository", run.ID)
    }

    // ... rest of flow uses (run, issue) pair where issue may be nil ...
}
```

---

## 6. Implementation Plan

### Phase 1: Issueless PR Support + Minimal Default Template

Low-risk changes to unblock manually created sessions.

1. Make issue lookup optional in `PRService.CreatePR`
2. Update `formatPRTitle`, `formatBranchName`, `formatCommitMessage` to accept `nil` issue
3. Replace the verbose default PR body with the minimal template from section 4.3
4. Update tests

### Phase 2: Repo PR Template Detection and Fill

1. Add `fetchPRTemplate` to fetch repo templates via GitHub Contents API
2. Add LLM-based template fill with fallback to default
3. Cache template per repo (invalidate on push to default branch)

### Phase 3: User-Authored PRs

1. Add `ProviderGitHubAppUser` and `GitHubAppUserConfig`
2. Add GitHub App user-authorization connect/callback/disconnect endpoints
3. Implement token exchange + refresh logic for GitHub App user access tokens
4. Update `resolveToken` to prefer `github_app_user` over installation tokens
5. Set commit author when using a user token
6. Add UI indicator showing who the PR will be created as
7. Handle token expiry/revocation gracefully (refresh, disable credential, fall back or block depending on org mode)

### Phase 4: Polish

1. Add `Co-authored-by` trailer when using app token for users who have GitHub connected
2. Support draft PR creation (org setting: `pr_draft_default: true`)
3. Add link to 143 session in PR body footer
4. Support multiple PR templates (`.github/PULL_REQUEST_TEMPLATE/` directory) — let user pick or use default

---

## 7. API Changes

### New/Modified Endpoints

| Method | Endpoint | Change |
|--------|----------|--------|
| `POST /api/v1/sessions/{id}/pr` | Add optional `draft`, `author_mode`, and `resume_token`; may return typed auth-intercept responses |
| `GET /api/v1/users/me/github-status` | New — returns whether user has a valid GitHub App user token for PR creation |
| `GET /api/v1/users/me/github/connect` | Starts GitHub App user authorization for PR authorship |
| `GET /api/v1/users/me/github/callback` | Stores GitHub App user token after authorization |
| `POST /api/v1/users/me/github/disconnect` | Removes stored GitHub App user credential |

### New Org Settings Fields

```json
{
  "pr_authorship": "user_preferred",
  "pr_draft_default": false
}
```

---

## 8. Security Considerations

- **Token storage**: GitHub App user access tokens and refresh tokens are encrypted at rest using the same AES-GCM scheme as other credentials in `user_credentials`
- **Least privilege**: User-authored PRs rely on GitHub App user tokens, so access is limited to repos where the app is installed and bounded by both app and user privileges
- **Token revocation**: If a user revokes app authorization or refresh fails, the system disables the credential, falls back to the installation token in `user_preferred`, and prompts reconnect in `user_required`
- **Authorization drift**: The system should re-check installation/user access during PR creation rather than assuming an old authorization still implies current repo access
- **Audit trail**: PR creation already logs `AuditActionSessionPRRequested`. Add a field indicating whether the PR was created as the user or the app

---

## 9. Migration

- **Existing users**: No disruption. `pr_authorship` defaults to `user_preferred`, and users without `github_app_user` credentials continue creating PRs via the installation token
- **New user consent**: Users explicitly authorize the GitHub App for PR authorship when they click the connect CTA; GitHub login remains unchanged
- **Forced re-auth**: Optionally, orgs can set `pr_authorship: "user_required"` which blocks PR creation until the user authorizes the GitHub App
